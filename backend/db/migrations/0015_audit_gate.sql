-- 0015: the audit log's one delete exception is gated on the purge's own transaction.
--
-- Migration 0014 let audit_logs rows be deleted when their workspace was past purge_after. That was a gate on DATA
-- (the workspace is due), not on WHO deletes. This migration adds the second gate: the deleting transaction must
-- have declared itself, with a transaction-local setting that names exactly one workspace AND the transaction's own
-- id:
--
--     SELECT set_config('helios.audit_purge', '<workspace id>:' || txid_current(), true)   -- is_local = true
--
-- Only internal/controlplane/wspurge runs that statement (a test greps the source tree to keep it so). The value is
-- bound to the transaction id, so a setting leaked on a pooled connection (a session-level set) never matches a
-- later transaction; it is bound to one workspace id, so it cannot be used to delete another workspace's rows; and the
-- trigger still requires that workspace to be scheduled for deletion and past purge_after (the database clock).
-- UPDATE and TRUNCATE are refused for everyone, as before.
--
-- Lock impact: CREATE OR REPLACE FUNCTION takes no lock on audit_logs. No table is altered.
BEGIN;

CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW'
       AND current_setting('helios.audit_purge', true) = OLD.workspace_id::text || ':' || txid_current()::text
       AND EXISTS (
            SELECT 1 FROM workspaces w
            WHERE w.id = OLD.workspace_id AND w.deleted_at IS NOT NULL AND w.purge_after <= now())
    THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_logs is append-only';
END $$;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand). Restores the 0014 body (data gate only).
-- ============================================================
-- CREATE OR REPLACE FUNCTION audit_logs_append_only() RETURNS trigger LANGUAGE plpgsql AS $f$
-- BEGIN
--     IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW' AND EXISTS (
--         SELECT 1 FROM workspaces w WHERE w.id = OLD.workspace_id AND w.deleted_at IS NOT NULL AND w.purge_after <= now()
--     ) THEN RETURN OLD; END IF;
--     RAISE EXCEPTION 'audit_logs is append-only';
-- END $f$;
