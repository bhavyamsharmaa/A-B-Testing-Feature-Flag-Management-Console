-- Multi-tenancy, stage 1, step 2: remove the transitional defaults.
--
-- !! Apply this IMMEDIATELY after the new backend is deployed. !!
--
-- 0004 gave every workspace_id column DEFAULT <legacy workspace> so the old
-- backend (which does not know about workspaces) kept working while 0004
-- was applied. The new backend passes workspace_id explicitly on every
-- insert. With the defaults gone, any insert that forgets it fails with a
-- NOT NULL violation instead of silently landing in the legacy workspace.
--
-- Do NOT apply this while the old backend is still serving traffic: its
-- flag, audit and role inserts would start failing.
--
-- Check afterwards with db/verify_0005.sql.

BEGIN;

ALTER TABLE environments          ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE flags                 ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE flag_configs          ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE experiments           ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE user_environment_roles ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE audit_logs            ALTER COLUMN workspace_id DROP DEFAULT;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Only needed if you must put the OLD backend back. It restores the default
-- pointing at the legacy workspace (the one created by 0004: the oldest).
-- Safe to do at any time, but it re-opens the "forgotten workspace_id lands
-- in the legacy workspace" hole, so remove it again with this file.
--
-- DO $$
-- DECLARE legacy UUID := (SELECT id FROM workspaces ORDER BY created_at, id LIMIT 1);
-- BEGIN
--   EXECUTE format('ALTER TABLE environments ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
--   EXECUTE format('ALTER TABLE flags ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
--   EXECUTE format('ALTER TABLE flag_configs ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
--   EXECUTE format('ALTER TABLE experiments ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
--   EXECUTE format('ALTER TABLE user_environment_roles ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
--   EXECUTE format('ALTER TABLE audit_logs ALTER COLUMN workspace_id SET DEFAULT %L', legacy);
-- END $$;
