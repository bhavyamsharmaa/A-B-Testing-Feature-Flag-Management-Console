-- Multi-tenancy, stage 1: workspaces.
--
-- Every user gets a private workspace holding its own environments, flags,
-- experiments and audit log. This migration:
--   * creates workspaces and workspace_members (one workspace per user: the
--     primary key on workspace_members.user_id),
--   * moves ALL existing data into ONE "legacy" workspace owned by the
--     existing admin. Environment UUIDs, API key rows and flag ids are not
--     touched, so live SDK keys keep resolving to the same environments,
--   * makes flag keys and environment keys unique per workspace,
--   * adds composite foreign keys so a flag, an environment, a role and a
--     membership can never belong to different workspaces.
--
-- DEPLOY ORDER (no downtime):
--   1. apply this file (the old backend keeps working, see DEFAULT below)
--   2. run db/verify_0004.sql
--   3. deploy the new backend
--   4. apply 0005_drop_workspace_defaults.sql IMMEDIATELY after step 3.
--      Until 0005 runs, an insert that forgets workspace_id silently lands
--      in the legacy workspace instead of failing.
--
-- Every new workspace_id column is NOT NULL with DEFAULT <legacy workspace>.
-- That is a constant default, so Postgres adds it without rewriting the
-- table and without UPDATE-ing audit_logs (its append-only trigger forbids
-- updates). The old backend inserts rows without workspace_id; the default
-- sends them to the only workspace that exists at that point.
--
-- The unique constraints keep their old names (flags_key_key,
-- environments_key_key) so the old backend's duplicate-key handling still
-- recognises them.
--
-- Rehearse this on a copy first: see db/rehearsal.md. RLS is enabled with no
-- policies on the new tables, as in 0001-0003.

BEGIN;

CREATE TABLE workspaces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    -- NULL only for the legacy workspace of a database that had no users yet.
    owner_id   UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    -- Primary key on user_id = one workspace per user. To allow several
    -- later, change the key to (user_id, workspace_id).
    user_id      UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Target of the composite foreign key on user_environment_roles.
    CONSTRAINT workspace_members_user_workspace_unique UNIQUE (user_id, workspace_id)
);
CREATE INDEX idx_workspace_members_workspace ON workspace_members (workspace_id);

-- ============================================================
-- Legacy workspace, backfill, and the workspace_id columns
-- ============================================================
DO $$
DECLARE
    legacy_id   UUID := gen_random_uuid();
    owner_user  UUID;
    owner_label TEXT;
    env_count   BIGINT;
    role_count  BIGINT;
BEGIN
    SELECT count(*) INTO env_count  FROM environments;
    SELECT count(*) INTO role_count FROM user_environment_roles;

    IF role_count > 0 THEN
        -- The owner is the user who is admin in EVERY environment; the
        -- earliest grant wins a tie.
        SELECT r.user_id INTO owner_user
        FROM user_environment_roles r
        WHERE r.role = 'admin'
        GROUP BY r.user_id
        HAVING count(DISTINCT r.environment_id) = env_count
        ORDER BY min(r.created_at), r.user_id
        LIMIT 1;

        IF owner_user IS NULL THEN
            RAISE EXCEPTION '0004: user_environment_roles has rows but no user is admin in every environment; cannot choose the owner of the legacy workspace. Grant one user admin everywhere (see 0002) and re-run.';
        END IF;
        SELECT COALESCE(email, id::text) INTO owner_label FROM auth.users WHERE id = owner_user;
    END IF;

    INSERT INTO workspaces (id, name, owner_id)
    VALUES (legacy_id, COALESCE(owner_label || '''s workspace', 'Helios workspace'), owner_user);

    -- Everyone who already holds a role becomes a member of the legacy workspace.
    INSERT INTO workspace_members (user_id, workspace_id)
    SELECT DISTINCT user_id, legacy_id FROM user_environment_roles;

    EXECUTE format('ALTER TABLE environments ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L REFERENCES workspaces(id) ON DELETE CASCADE', legacy_id);
    EXECUTE format('ALTER TABLE flags ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L REFERENCES workspaces(id) ON DELETE CASCADE', legacy_id);
    EXECUTE format('ALTER TABLE flag_configs ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L REFERENCES workspaces(id) ON DELETE CASCADE', legacy_id);
    EXECUTE format('ALTER TABLE experiments ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L REFERENCES workspaces(id) ON DELETE CASCADE', legacy_id);
    EXECUTE format('ALTER TABLE user_environment_roles ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L REFERENCES workspaces(id) ON DELETE CASCADE', legacy_id);
    -- No foreign key on audit_logs: like actor_id and environment_id, a
    -- cascade would have to modify rows the append-only trigger protects.
    EXECUTE format('ALTER TABLE audit_logs ADD COLUMN workspace_id UUID NOT NULL DEFAULT %L', legacy_id);
END $$;

-- ============================================================
-- Per-workspace uniqueness (same constraint names as before)
-- ============================================================
ALTER TABLE environments DROP CONSTRAINT environments_key_key;
ALTER TABLE environments ADD CONSTRAINT environments_key_key UNIQUE (workspace_id, key);
ALTER TABLE environments ADD CONSTRAINT environments_id_workspace_unique UNIQUE (id, workspace_id);
-- An environment key must never look like a UUID: the API accepts either an
-- environment UUID or (temporarily) a key in the same path position.
ALTER TABLE environments ADD CONSTRAINT environments_key_not_uuid
    CHECK (key !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');

ALTER TABLE flags DROP CONSTRAINT flags_key_key;
ALTER TABLE flags ADD CONSTRAINT flags_key_key UNIQUE (workspace_id, key);
ALTER TABLE flags ADD CONSTRAINT flags_id_workspace_unique UNIQUE (id, workspace_id);

-- ============================================================
-- Composite foreign keys: same workspace or the row cannot exist
-- ============================================================
ALTER TABLE flag_configs
    ADD CONSTRAINT flag_configs_flag_workspace_fkey
        FOREIGN KEY (flag_id, workspace_id) REFERENCES flags (id, workspace_id) ON DELETE CASCADE,
    ADD CONSTRAINT flag_configs_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE;

ALTER TABLE experiments
    ADD CONSTRAINT experiments_flag_workspace_fkey
        FOREIGN KEY (flag_id, workspace_id) REFERENCES flags (id, workspace_id) ON DELETE CASCADE,
    ADD CONSTRAINT experiments_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE;

ALTER TABLE user_environment_roles
    ADD CONSTRAINT user_environment_roles_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE,
    ADD CONSTRAINT user_environment_roles_member_workspace_fkey
        FOREIGN KEY (user_id, workspace_id) REFERENCES workspace_members (user_id, workspace_id) ON DELETE CASCADE;

CREATE INDEX idx_audit_logs_workspace ON audit_logs (workspace_id, environment_id, id DESC);

ALTER TABLE workspaces        ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_members ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
-- !!  DESTRUCTIVE ONCE ANY WORKSPACE OTHER THAN THE LEGACY ONE EXISTS.  !!
-- !!  Dropping workspace_id merges every tenant's environments, flags, !!
-- !!  experiments, roles and audit rows into one pool, and re-adding    !!
-- !!  the global unique constraints FAILS if two tenants share a flag   !!
-- !!  or environment key. Only roll back before the new backend has    !!
-- !!  provisioned its first new workspace, or restore from backup       !!
-- !!  (scripts/backup_db.sh) instead.                                   !!
-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
-- Deploy the previous backend first. If 0005 was applied, the old backend
-- cannot insert (no default) until 0004 is rolled back.
--
-- BEGIN;
-- DROP INDEX idx_audit_logs_workspace;
-- ALTER TABLE user_environment_roles DROP CONSTRAINT user_environment_roles_member_workspace_fkey,
--                                    DROP CONSTRAINT user_environment_roles_environment_workspace_fkey;
-- ALTER TABLE experiments DROP CONSTRAINT experiments_environment_workspace_fkey,
--                         DROP CONSTRAINT experiments_flag_workspace_fkey;
-- ALTER TABLE flag_configs DROP CONSTRAINT flag_configs_environment_workspace_fkey,
--                          DROP CONSTRAINT flag_configs_flag_workspace_fkey;
-- ALTER TABLE flags DROP CONSTRAINT flags_id_workspace_unique;
-- ALTER TABLE flags DROP CONSTRAINT flags_key_key;
-- ALTER TABLE flags ADD CONSTRAINT flags_key_key UNIQUE (key);              -- fails on duplicate keys
-- ALTER TABLE environments DROP CONSTRAINT environments_key_not_uuid;
-- ALTER TABLE environments DROP CONSTRAINT environments_id_workspace_unique;
-- ALTER TABLE environments DROP CONSTRAINT environments_key_key;
-- ALTER TABLE environments ADD CONSTRAINT environments_key_key UNIQUE (key); -- fails on duplicate keys
-- ALTER TABLE audit_logs DROP COLUMN workspace_id;
-- ALTER TABLE user_environment_roles DROP COLUMN workspace_id;
-- ALTER TABLE experiments DROP COLUMN workspace_id;
-- ALTER TABLE flag_configs DROP COLUMN workspace_id;
-- ALTER TABLE flags DROP COLUMN workspace_id;
-- ALTER TABLE environments DROP COLUMN workspace_id;
-- DROP TABLE workspace_members;
-- DROP TABLE workspaces;
-- COMMIT;
