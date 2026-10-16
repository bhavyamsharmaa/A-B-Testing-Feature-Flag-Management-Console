-- Scheduled changes: enable, disable, kill or patch (rollout, fallthrough,
-- rules) ONE flag in ONE environment at a future time.
--
-- NOT PART OF THE MULTI-TENANCY, EXPERIMENTS OR SEGMENTS RELEASES. It needs the
-- workspaces, flags and environments of 0004 and nothing from 0008-0010. See
-- docs/ROLLOUT.md: it ships with a later release than multi-tenancy. No
-- existing table changes, and no earlier backend reads or writes this one.
--
-- A row is one intended change and its whole life. The worker (cmd/scheduler,
-- or POST /internal/scheduler/run) claims due rows with a LEASE: it moves a
-- row pending -> running and stamps lease_owner and lease_expires_at, in one
-- statement that uses FOR UPDATE SKIP LOCKED so two workers never claim the
-- same row. A worker that dies leaves a 'running' row whose lease runs out;
-- the next worker takes it over. attempts counts claims, so a row that keeps
-- killing its worker stops after a bound. See docs/SCHEDULING.md.
--
--   status      pending | running | succeeded | failed | missed | cancelled
--   run_at      when the creator asked for it (UTC); due_at is when it may next
--               be claimed (run_at, then pushed out by the retry backoff)
--   base_version, base_config
--               the flag configuration's version when scheduled and, for a
--               patch, the parts the patch touches as they were then: a patch
--               is applied only if those parts are still the same
--   idempotency_key
--               supplied by the creator, unique per workspace: a retried
--               request cannot schedule the same change twice
--
-- workspace_id is on the table, composite foreign keys keep the flag and the
-- environment inside the same workspace, and indexes lead with workspace_id,
-- except the two partial indexes the worker's claim query uses (it looks across
-- workspaces by time). RLS is on with no policies, as in 0004.
--
-- LOCKS: one new, empty table. The foreign keys take SHARE ROW EXCLUSIVE on
-- workspaces, flags, environments and auth.users for milliseconds while created
-- (blocks writes to those tables, not reads). Nothing is rewritten or scanned.
--
-- Rehearse: db/verify_0011.sql (read-only) and db/rehearsal.md.

BEGIN;

CREATE TABLE scheduled_changes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    flag_id          UUID NOT NULL,
    environment_id   UUID NOT NULL,
    kind             TEXT NOT NULL,
    -- For a patch: {"rollout": ..., "fallthroughVariationId": ..., "targetingRules": ...}; otherwise {}.
    change           JSONB NOT NULL DEFAULT '{}'::jsonb,
    base_version     BIGINT NOT NULL,
    base_config      JSONB NOT NULL DEFAULT '{}'::jsonb,
    run_at           TIMESTAMPTZ NOT NULL,
    due_at           TIMESTAMPTZ NOT NULL,
    idempotency_key  TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    created_by       UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    -- Kept so the audit trail still names the creator after they leave.
    created_by_email TEXT NOT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    lease_owner      TEXT,
    lease_expires_at TIMESTAMPTZ,
    result_code      TEXT,
    result_message   TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    CONSTRAINT scheduled_changes_idempotency_unique UNIQUE (workspace_id, idempotency_key),
    CONSTRAINT scheduled_changes_id_workspace_unique UNIQUE (id, workspace_id),
    CONSTRAINT scheduled_changes_flag_workspace_fkey
        FOREIGN KEY (flag_id, workspace_id) REFERENCES flags (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT scheduled_changes_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT scheduled_changes_kind_valid CHECK (kind IN ('enable', 'disable', 'kill', 'patch')),
    CONSTRAINT scheduled_changes_status_valid CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'missed', 'cancelled')),
    CONSTRAINT scheduled_changes_sizes CHECK (
        char_length(idempotency_key) BETWEEN 1 AND 128
        AND jsonb_typeof(change) = 'object' AND octet_length(change::text) <= 32768
        AND jsonb_typeof(base_config) = 'object' AND octet_length(base_config::text) <= 32768
        AND (result_message IS NULL OR char_length(result_message) <= 1000)
        AND attempts >= 0),
    -- A running row always has a lease; a finished one always has a finish time.
    CONSTRAINT scheduled_changes_running_has_lease CHECK (
        status <> 'running' OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CONSTRAINT scheduled_changes_finished_has_time CHECK (
        status IN ('pending', 'running') OR finished_at IS NOT NULL)
);

-- Lists in the console (by environment and flag), per-flag and per-workspace pending counts.
CREATE INDEX idx_scheduled_changes_flag ON scheduled_changes (workspace_id, environment_id, flag_id, status);
CREATE INDEX idx_scheduled_changes_run_at ON scheduled_changes (workspace_id, run_at DESC);
-- The claim query: what is due now, and what a dead worker left running.
CREATE INDEX idx_scheduled_changes_due ON scheduled_changes (due_at) WHERE status = 'pending';
CREATE INDEX idx_scheduled_changes_lease ON scheduled_changes (lease_expires_at) WHERE status = 'running';

ALTER TABLE scheduled_changes ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Deploy a backend without scheduling first (it would otherwise serve routes
-- over a missing table). Dropping the table DISCARDS every scheduled change and
-- its history; pending changes will simply never run. Back up first
-- (backend/scripts/backup_db.sh).
--
-- BEGIN;
-- DROP TABLE scheduled_changes;
-- COMMIT;
