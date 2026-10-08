-- Experiments foundation: experiments, their metrics, and the events table
-- that results will be computed from.
--
-- An experiment is scoped to one environment and one flag. At most one
-- experiment per flag per environment may be 'running' (partial unique index).
-- Variation ids live inside flags.variations (JSONB, no table), so
-- events.variation_id is plain TEXT and is validated by the ingestion code.
--
-- RLS is enabled with no policies, as in 0001/0002: the Go backend connects
-- as the table owner; Supabase's REST roles get nothing.

BEGIN;

CREATE TYPE experiment_status AS ENUM ('draft', 'running', 'stopped');
CREATE TYPE metric_type       AS ENUM ('conversion', 'value');

CREATE TABLE experiments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- Deleting a flag deletes its draft/stopped experiments. A running one
    -- blocks the delete in flags.Delete (409 HAS_RUNNING_EXPERIMENT).
    flag_id        UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    key            TEXT NOT NULL,
    name           TEXT NOT NULL,
    hypothesis     TEXT,
    status         experiment_status NOT NULL DEFAULT 'draft',
    started_at     TIMESTAMPTZ,
    stopped_at     TIMESTAMPTZ,
    -- Assignment-affecting config of the flag in this environment, captured
    -- at start and stop: { targetingRules, rollout, fallthroughVariationId, version }.
    -- Compared with each other (or with the live config while running) to
    -- report configChangedSinceStart. Nothing enforces a freeze.
    start_config   JSONB,
    stop_config    JSONB,
    created_by     UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT experiments_env_key_unique UNIQUE (environment_id, key),
    CONSTRAINT experiments_status_timestamps CHECK (
           (status = 'draft'   AND started_at IS NULL     AND stopped_at IS NULL     AND start_config IS NULL     AND stop_config IS NULL)
        OR (status = 'running' AND started_at IS NOT NULL AND stopped_at IS NULL     AND start_config IS NOT NULL AND stop_config IS NULL)
        OR (status = 'stopped' AND started_at IS NOT NULL AND stopped_at IS NOT NULL AND start_config IS NOT NULL AND stop_config IS NOT NULL)
    )
);

CREATE UNIQUE INDEX one_running_per_flag_env
    ON experiments (environment_id, flag_id) WHERE status = 'running';
CREATE INDEX idx_experiments_flag ON experiments (flag_id);

CREATE TABLE experiment_metrics (
    experiment_id UUID NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    event_name    TEXT NOT NULL,
    type          metric_type NOT NULL,
    is_primary    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (experiment_id, name),
    -- An event feeds at most one metric, so a conversion's dedup key is
    -- (experiment, subject, event_name).
    CONSTRAINT experiment_metrics_event_unique UNIQUE (experiment_id, event_name),
    -- The "$" prefix is reserved for system events such as $exposure.
    CONSTRAINT experiment_metrics_event_not_reserved CHECK (event_name NOT LIKE '$%')
);

CREATE UNIQUE INDEX one_primary_metric
    ON experiment_metrics (experiment_id) WHERE is_primary;

CREATE TABLE events (
    id             BIGSERIAL PRIMARY KEY,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    experiment_id  UUID NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    subject_key    TEXT NOT NULL,
    variation_id   TEXT NOT NULL,
    event_name     TEXT NOT NULL,
    -- NULL for conversions and exposures; required for value metrics.
    value          DOUBLE PRECISION,
    -- true for rows that count once per subject: conversions and '$exposure'.
    -- The ingestion code inserts these with ON CONFLICT DO NOTHING.
    dedup          BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT events_value_matches_dedup CHECK (dedup = (value IS NULL))
);

CREATE UNIQUE INDEX one_dedup_event_per_subject
    ON events (experiment_id, subject_key, event_name) WHERE dedup;
CREATE INDEX idx_events_results ON events (experiment_id, event_name, variation_id);
CREATE INDEX idx_events_time    ON events (experiment_id, created_at);

ALTER TABLE experiments        ENABLE ROW LEVEL SECURITY;
ALTER TABLE experiment_metrics ENABLE ROW LEVEL SECURITY;
ALTER TABLE events             ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand to undo 0003)
-- ============================================================
-- Deploy the previous backend first: flags.Delete queries `experiments`.
--
-- DROP TABLE events;
-- DROP TABLE experiment_metrics;
-- DROP TABLE experiments;
-- DROP TYPE metric_type;
-- DROP TYPE experiment_status;
