-- Experiments tracking: turn the (so far unused) events table from 0003 into
-- the target of POST /track.
--
-- NOT PART OF THE MULTI-TENANCY RELEASE. Apply it only AFTER 0004-0007 are
-- live (it needs workspace_id on experiments and environments). See
-- docs/ROLLOUT.md. The multi-tenancy backend does not use it, and this
-- migration does not change anything the multi-tenancy backend reads.
--
-- What changes on events:
--   * workspace_id (NOT NULL, no default), with composite foreign keys so an
--     event can only reference an environment and an experiment of its own
--     workspace, and an experiment of its own environment;
--   * event_id: the client-supplied id that makes SDK retries idempotent;
--   * properties (small JSONB) and occurred_at (the client's timestamp;
--     created_at stays the server's receipt time);
--   * experiment_id and variation_id become NULLABLE: an event need not belong
--     to an experiment, and only $exposure rows carry the variation (results
--     join other events to the subject's exposure);
--   * the 0003 "dedup" column, its CHECK and its unique index are dropped:
--     idempotency is by event_id, exposures by their own index below, and
--     "count once per subject" for conversions is decided when results are
--     computed, from the raw rows;
--   * value must be finite; sizes of text columns are bounded.
--
-- LOCKS: events has never been written to (nothing inserted into it before
-- /track), so every statement below touches an empty table and holds its
-- ACCESS EXCLUSIVE lock for milliseconds. The two new unique constraints on
-- experiments scan only the (small) experiments table. Plain CREATE INDEX is
-- therefore fine; if events ever holds data, build the indexes with
-- CREATE INDEX CONCURRENTLY outside this transaction first.
--
-- Rehearse: db/verify_0008.sql (read-only checks) and db/rehearsal.md.

BEGIN;

-- Targets of the composite foreign keys below.
ALTER TABLE experiments ADD CONSTRAINT experiments_id_workspace_unique UNIQUE (id, workspace_id);
ALTER TABLE experiments ADD CONSTRAINT experiments_id_environment_workspace_unique UNIQUE (id, environment_id, workspace_id);

-- Refuse to continue on a database where events already holds rows: this
-- migration was written for an empty table (see LOCKS above).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM events) THEN
        RAISE EXCEPTION '0008: events is not empty; backfill workspace_id and build the indexes concurrently by hand, then re-run';
    END IF;
END $$;

ALTER TABLE events DROP CONSTRAINT events_value_matches_dedup;
DROP INDEX one_dedup_event_per_subject;
DROP INDEX idx_events_results;
DROP INDEX idx_events_time;
ALTER TABLE events DROP COLUMN dedup;

ALTER TABLE events
    ADD COLUMN workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    ADD COLUMN event_id     TEXT,
    ADD COLUMN properties   JSONB,
    ADD COLUMN occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE events
    ALTER COLUMN experiment_id DROP NOT NULL,
    ALTER COLUMN variation_id  DROP NOT NULL;

-- Same workspace or the row cannot exist. With experiment_id NULL the
-- experiment key is skipped (MATCH SIMPLE) and only the environment is checked.
ALTER TABLE events
    ADD CONSTRAINT events_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE,
    ADD CONSTRAINT events_experiment_environment_workspace_fkey
        FOREIGN KEY (experiment_id, environment_id, workspace_id)
        REFERENCES experiments (id, environment_id, workspace_id) ON DELETE CASCADE;

ALTER TABLE events
    ADD CONSTRAINT events_value_finite CHECK (
        value IS NULL OR (value <> 'NaN'::float8 AND value <> 'Infinity'::float8 AND value <> '-Infinity'::float8)),
    ADD CONSTRAINT events_exposure_complete CHECK (
        event_name <> '$exposure' OR (experiment_id IS NOT NULL AND variation_id IS NOT NULL AND value IS NULL)),
    ADD CONSTRAINT events_sizes CHECK (
        char_length(subject_key) BETWEEN 1 AND 256
        AND char_length(event_name) BETWEEN 1 AND 128
        AND (event_id IS NULL OR char_length(event_id) BETWEEN 1 AND 128)
        AND (variation_id IS NULL OR char_length(variation_id) <= 128)
        AND (properties IS NULL OR (jsonb_typeof(properties) = 'object' AND pg_column_size(properties) <= 4096)));

-- Idempotent retries: one row per client event id within an environment.
CREATE UNIQUE INDEX events_event_id_unique
    ON events (workspace_id, environment_id, event_id) WHERE event_id IS NOT NULL;

-- At most one exposure per subject per experiment; the first one wins
-- (INSERT ... ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX events_one_exposure_per_subject
    ON events (workspace_id, environment_id, experiment_id, subject_key) WHERE event_name = '$exposure';

-- Index plan for the results phase (all lead with workspace_id):
--   exposures per variation / metric counts per variation  -> by experiment, event, variation
--   join events to a subject's exposure                    -> by experiment, subject
--   time series and retention purges                       -> by environment, time
CREATE INDEX idx_events_results ON events (workspace_id, experiment_id, event_name, variation_id) WHERE experiment_id IS NOT NULL;
CREATE INDEX idx_events_subject ON events (workspace_id, experiment_id, subject_key) WHERE experiment_id IS NOT NULL;
CREATE INDEX idx_events_time    ON events (workspace_id, environment_id, occurred_at);

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Deploy the previous backend first (it never reads events). This DISCARDS
-- all ingested events.
--
-- BEGIN;
-- DROP TABLE events;
-- CREATE TABLE events (... as in 0003, with its indexes and RLS ...);
-- ALTER TABLE experiments DROP CONSTRAINT experiments_id_environment_workspace_unique,
--                         DROP CONSTRAINT experiments_id_workspace_unique;
-- COMMIT;
