-- Lifecycle: ownership transfer, soft-deleted workspaces and their purge, and an explicit control
-- variation for experiments. NOT PART OF EARLIER RELEASES: see docs/ROLLOUT.md.
--
--   workspaces.deleted_at / deleted_by / purge_after
--       A workspace scheduled for deletion is invisible to everything that resolves workspaces
--       (the membership queries add "deleted_at IS NULL"), for about 14 days (purge_after), after
--       which cmd/purgeworkspaces removes its data in bounded batches. Restoring clears all three.
--   ownership_offers
--       An owner offers ownership to an admin of the same workspace; the target accepts in one
--       transaction (promote first, then demote, so the deferred last-owner trigger of 0007 never
--       sees zero owners). One open offer per workspace (partial unique index).
--   workspace_tombstones
--       The permanent, minimal record of a purged workspace: its id, who asked, when, and row
--       counts. No customer data. It is the one table with a workspace_id column that is exempt
--       from "no rows left for a purged workspace" (the purge test names it).
--   experiment_variations
--       Which variation is the control of an experiment, enforced by a partial unique index (at
--       most one) and, from the application and the backfill below, at least one. Existing
--       experiments get their flag's first variation as control, which is what results used, so no
--       result changes.
--   audit_logs_append_only()
--       Still forbids every UPDATE and TRUNCATE and every DELETE, EXCEPT deleting the rows of a
--       workspace that is scheduled for deletion AND past its grace period (purge_after <= now()),
--       so a purge can remove a workspace's audit rows and nothing else can.
--
-- LOCKS:
--   workspaces            three nullable columns (catalog only, no rewrite), one CHECK on them
--                         (added NOT VALID then validated: the table is small), one partial index:
--                         a brief ACCESS EXCLUSIVE for ADD COLUMN.
--   audit_logs            NOT altered. CREATE OR REPLACE FUNCTION takes no lock on the table.
--   events, experiments   not touched (experiment_variations is a new table).
--   workspace_members, experiments, workspaces, auth.users: SHARE ROW EXCLUSIVE for milliseconds
--                         while the new tables' foreign keys are created.
--
-- Rehearse: db/verify_0014.sql (read-only) and db/rehearsal.md.

BEGIN;

-- ---- soft delete -------------------------------------------------------------------------------
ALTER TABLE workspaces
    ADD COLUMN deleted_at  TIMESTAMPTZ,
    ADD COLUMN deleted_by  UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    ADD COLUMN purge_after TIMESTAMPTZ;
ALTER TABLE workspaces
    ADD CONSTRAINT workspaces_deletion_pair CHECK ((deleted_at IS NULL) = (purge_after IS NULL)) NOT VALID;
ALTER TABLE workspaces VALIDATE CONSTRAINT workspaces_deletion_pair;
-- the purge job's lookup: only the few scheduled workspaces are in this index
CREATE INDEX idx_workspaces_deleted ON workspaces (purge_after) WHERE deleted_at IS NOT NULL;

-- ---- ownership transfer ------------------------------------------------------------------------
CREATE TABLE ownership_offers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    from_user_id UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    to_user_id   UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    resolved_at  TIMESTAMPTZ,
    CONSTRAINT ownership_offers_status_valid CHECK (status IN ('pending', 'accepted', 'cancelled', 'declined', 'expired')),
    CONSTRAINT ownership_offers_different_people CHECK (from_user_id <> to_user_id),
    CONSTRAINT ownership_offers_resolved CHECK ((status = 'pending') = (resolved_at IS NULL)),
    CONSTRAINT ownership_offers_expiry CHECK (expires_at > created_at)
);
-- one open offer per workspace
CREATE UNIQUE INDEX one_pending_offer_per_workspace ON ownership_offers (workspace_id) WHERE status = 'pending';
CREATE INDEX idx_ownership_offers_target ON ownership_offers (to_user_id) WHERE status = 'pending';
-- the expiry sweep
CREATE INDEX idx_ownership_offers_expiry ON ownership_offers (expires_at) WHERE status = 'pending';

-- ---- tombstones --------------------------------------------------------------------------------
-- No foreign keys on purpose: the workspace and the people are gone or going. Only ids, times and counts.
CREATE TABLE workspace_tombstones (
    workspace_id UUID PRIMARY KEY,
    requested_by UUID,
    requested_at TIMESTAMPTZ NOT NULL,
    purged_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    summary      JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT workspace_tombstones_summary_small CHECK (jsonb_typeof(summary) = 'object' AND octet_length(summary::text) <= 4096)
);

-- ---- control variation -------------------------------------------------------------------------
CREATE TABLE experiment_variations (
    experiment_id UUID NOT NULL,
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    variation_id  TEXT NOT NULL,
    position      INTEGER NOT NULL,
    is_control    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (experiment_id, variation_id),
    CONSTRAINT experiment_variations_experiment_workspace_fkey
        FOREIGN KEY (experiment_id, workspace_id) REFERENCES experiments (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT experiment_variations_sizes CHECK (char_length(variation_id) BETWEEN 1 AND 128 AND position >= 0)
);
-- at most one control per experiment (the application and the backfill guarantee at least one)
CREATE UNIQUE INDEX one_control_per_experiment ON experiment_variations (experiment_id) WHERE is_control;
CREATE INDEX idx_experiment_variations_workspace ON experiment_variations (workspace_id, experiment_id);

-- Existing experiments: every variation of the flag, the first one is the control (what results used).
INSERT INTO experiment_variations (experiment_id, workspace_id, variation_id, position, is_control)
SELECT x.id, x.workspace_id, v.elem ->> 'id', (v.ord - 1)::int, v.ord = 1
FROM experiments x
JOIN flags f ON f.id = x.flag_id AND f.workspace_id = x.workspace_id
CROSS JOIN LATERAL jsonb_array_elements(f.variations) WITH ORDINALITY AS v(elem, ord);

ALTER TABLE ownership_offers ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE experiment_variations ENABLE ROW LEVEL SECURITY;

-- ---- the audit log stays append-only, except for a workspace whose purge is due ------------------
CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW' AND EXISTS (
        SELECT 1 FROM workspaces w
        WHERE w.id = OLD.workspace_id AND w.deleted_at IS NOT NULL AND w.purge_after <= now()
    ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_logs is append-only';
END $$;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand, back up first)
-- ============================================================
-- Deploy a backend without these features first. Discards offers, tombstones and control designations;
-- pending-deletion workspaces become ordinary again (their schedule is forgotten).
-- BEGIN;
-- CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger LANGUAGE plpgsql AS $f$
-- BEGIN RAISE EXCEPTION 'audit_logs is append-only'; END $f$;
-- DROP TABLE experiment_variations;
-- DROP TABLE workspace_tombstones;
-- DROP TABLE ownership_offers;
-- DROP INDEX idx_workspaces_deleted;
-- ALTER TABLE workspaces DROP CONSTRAINT workspaces_deletion_pair;
-- ALTER TABLE workspaces DROP COLUMN purge_after, DROP COLUMN deleted_by, DROP COLUMN deleted_at;
-- COMMIT;
