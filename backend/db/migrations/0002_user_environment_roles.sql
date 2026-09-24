-- Per-environment RBAC (US-06). Users come from Supabase Auth; this table
-- only records which role each user holds in each environment. An editor in
-- staging is not implicitly an editor in production.

CREATE TYPE role_type AS ENUM ('viewer', 'editor', 'approver', 'admin');

CREATE TABLE user_environment_roles (
    user_id        UUID NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    role           role_type NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, environment_id)
);

CREATE INDEX idx_user_environment_roles_environment ON user_environment_roles(environment_id);

ALTER TABLE user_environment_roles ENABLE ROW LEVEL SECURITY;

-- Bootstrap: the members endpoints are admin-only, so the first admin has to
-- be granted directly. After signing up through Supabase Auth, run once:
--
--   INSERT INTO user_environment_roles (user_id, environment_id, role)
--   SELECT u.id, e.id, 'admin'
--   FROM auth.users u CROSS JOIN environments e
--   WHERE u.email = 'you@example.com';
