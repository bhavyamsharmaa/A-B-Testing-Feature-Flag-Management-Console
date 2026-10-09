-- Multi-tenancy, stage 2: workspace roles, several workspaces per user,
-- invites, slugs and tenant-scoped API keys.
--
-- APPLIES ON TOP OF 0004 AND 0005 (workspaces, workspace_members, the
-- workspace_id columns). None of 0004-0006 has been applied to any shared
-- database yet; apply them in order, on a copy first.
--
-- What changes:
--   * workspace_members gets a role (owner | admin | editor | viewer) and its
--     primary key becomes (user_id, workspace_id), so a user can belong to
--     many workspaces. Existing members get a role from what they held:
--     the workspace owner -> owner, an env admin/approver -> admin,
--     editor -> editor, otherwise viewer.
--   * workspaces get a unique slug.
--   * workspace_invites: pending invitations by email, accepted with a
--     one-time token (only its SHA-256 hash is stored).
--   * api_keys gets workspace_id (+ a composite foreign key to its
--     environment) and a label, so keys are tenant-scoped like everything else.
--   * user_environment_roles is no longer written or read by the backend;
--     the workspace role decides what a user may do in every environment of
--     the workspace. The table is kept (nothing is dropped here).
--
-- RLS is enabled on the new table with no policies, as in 0001-0005.

BEGIN;

CREATE TYPE workspace_role AS ENUM ('owner', 'admin', 'editor', 'viewer');

-- ============================================================
-- Slugs
-- ============================================================
ALTER TABLE workspaces ADD COLUMN slug TEXT;
-- lower-case letters, digits and dashes from the name, plus a short id suffix
-- so existing rows are unique without a retry loop.
UPDATE workspaces
SET slug = COALESCE(NULLIF(trim(both '-' from lower(regexp_replace(name, '[^a-zA-Z0-9]+', '-', 'g'))), ''), 'workspace')
           || '-' || substr(replace(id::text, '-', ''), 1, 6);
ALTER TABLE workspaces ALTER COLUMN slug SET NOT NULL;
ALTER TABLE workspaces ADD CONSTRAINT workspaces_slug_key UNIQUE (slug);

-- ============================================================
-- Membership roles; several workspaces per user
-- ============================================================
ALTER TABLE workspace_members ADD COLUMN role workspace_role;
ALTER TABLE workspace_members ADD COLUMN last_active_at TIMESTAMPTZ;

UPDATE workspace_members m
SET role = CASE
    WHEN w.owner_id = m.user_id THEN 'owner'::workspace_role
    ELSE CASE COALESCE((
            SELECT max(CASE r.role WHEN 'admin' THEN 4 WHEN 'approver' THEN 3 WHEN 'editor' THEN 2 ELSE 1 END)
            FROM user_environment_roles r
            WHERE r.user_id = m.user_id AND r.workspace_id = m.workspace_id), 1)
        WHEN 4 THEN 'admin'::workspace_role
        WHEN 3 THEN 'admin'::workspace_role
        WHEN 2 THEN 'editor'::workspace_role
        ELSE 'viewer'::workspace_role
    END
END
FROM workspaces w
WHERE w.id = m.workspace_id;

ALTER TABLE workspace_members ALTER COLUMN role SET NOT NULL;

-- user_id alone was the key (one workspace per user). The unique constraint
-- (user_id, workspace_id) from 0004 stays: user_environment_roles references it.
ALTER TABLE workspace_members DROP CONSTRAINT workspace_members_pkey;
ALTER TABLE workspace_members ADD CONSTRAINT workspace_members_pkey PRIMARY KEY (user_id, workspace_id);

-- ============================================================
-- Invites
-- ============================================================
CREATE TABLE workspace_invites (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email        TEXT NOT NULL CHECK (email = lower(email)),
    role         workspace_role NOT NULL CHECK (role <> 'owner'),
    -- SHA-256 (hex) of the one-time token; the token itself is shown once.
    token_hash   TEXT NOT NULL UNIQUE,
    invited_by   UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    accepted_by  UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    revoked_at   TIMESTAMPTZ
);
-- One open invite per email per workspace. "Open" ignores expiry (now() is
-- not allowed in an index predicate); the backend revokes an expired invite
-- before issuing a new one.
CREATE UNIQUE INDEX one_open_invite_per_email
    ON workspace_invites (workspace_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX idx_workspace_invites_email
    ON workspace_invites (email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- ============================================================
-- API keys belong to a workspace
-- ============================================================
ALTER TABLE api_keys ADD COLUMN workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE;
UPDATE api_keys k SET workspace_id = e.workspace_id FROM environments e WHERE e.id = k.environment_id;
ALTER TABLE api_keys ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_environment_workspace_fkey
    FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE;
ALTER TABLE api_keys ADD COLUMN name TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_api_keys_workspace ON api_keys (workspace_id) WHERE revoked_at IS NULL;

ALTER TABLE workspace_invites ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
-- !!  DESTRUCTIVE once users belong to several workspaces: restoring    !!
-- !!  the (user_id) primary key FAILS then, and roles and invites are   !!
-- !!  lost. Restore from backup instead of rolling back in production. !!
-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
--
-- BEGIN;
-- DROP INDEX idx_api_keys_workspace;
-- ALTER TABLE api_keys DROP COLUMN name;
-- ALTER TABLE api_keys DROP CONSTRAINT api_keys_environment_workspace_fkey;
-- ALTER TABLE api_keys DROP COLUMN workspace_id;
-- DROP TABLE workspace_invites;
-- ALTER TABLE workspace_members DROP CONSTRAINT workspace_members_pkey;
-- ALTER TABLE workspace_members ADD CONSTRAINT workspace_members_pkey PRIMARY KEY (user_id);  -- fails if a user is in 2+ workspaces
-- ALTER TABLE workspace_members DROP COLUMN last_active_at;
-- ALTER TABLE workspace_members DROP COLUMN role;
-- ALTER TABLE workspaces DROP CONSTRAINT workspaces_slug_key;
-- ALTER TABLE workspaces DROP COLUMN slug;
-- DROP TYPE workspace_role;
-- COMMIT;
