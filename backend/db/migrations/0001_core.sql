-- Helios core schema: environments, flags, per-environment flag config,
-- API keys, and the append-only audit log.
--
-- Supabase note: every table here has row-level security enabled with no
-- policies. That locks them out of Supabase's auto-generated REST API (the
-- anon/authenticated roles), which would otherwise let anyone holding the
-- public anon key read api_keys or rewrite flags. The Go backend connects as
-- the table owner, which RLS does not apply to.

-- Supabase provides auth.users; plain Postgres (docker-compose) does not.
-- Create a minimal stand-in only when it's missing, so this file runs
-- unchanged in both places without touching Supabase's own auth schema.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'auth' AND table_name = 'users'
    ) THEN
        CREATE SCHEMA IF NOT EXISTS auth;
        CREATE TABLE auth.users (
            id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            email TEXT UNIQUE
        );
    END IF;
END $$;

-- ============================================================
-- ENVIRONMENTS
-- ============================================================
-- is_production drives RBAC (production toggles need an approver), so it's
-- an explicit column rather than a string match on the key.

CREATE TABLE environments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key           TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    is_production BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO environments (key, name, is_production) VALUES
    ('dev',        'Development', false),
    ('staging',    'Staging',     false),
    ('production', 'Production',  true);

-- ============================================================
-- FLAGS (US-01)
-- ============================================================
-- flags holds the cross-environment definition; flag_configs holds
-- per-environment behaviour. The split makes environment isolation
-- structural: no code path can leak a dev toggle into production.

CREATE TYPE variation_type AS ENUM ('boolean', 'string', 'number', 'json');

CREATE TABLE flags (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key            TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    description    TEXT,
    variation_type variation_type NOT NULL,
    -- ordered [{ "id": "on", "value": true }, ...]; order defines rollout bucket ranges
    variations     JSONB NOT NULL,
    created_by     UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT variations_bounds CHECK (jsonb_array_length(variations) BETWEEN 2 AND 20)
);

CREATE TABLE flag_configs (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flag_id                  UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    environment_id           UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    enabled                  BOOLEAN NOT NULL DEFAULT false,
    -- [{ "clauses": [{attribute, operator, value}], "variationId": "on" }], top-down, first match wins
    targeting_rules          JSONB NOT NULL DEFAULT '[]',
    -- { variationId: basisPoints } summing to 100000, or NULL for no rollout
    rollout                  JSONB,
    -- per flag+environment, so two flags at 50% bucket the same user independently (US-03 AC-4)
    salt                     TEXT NOT NULL,
    fallthrough_variation_id TEXT NOT NULL,
    version                  BIGINT NOT NULL DEFAULT 1,
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (flag_id, environment_id)
);

CREATE INDEX idx_flag_configs_environment ON flag_configs(environment_id);

-- ============================================================
-- API KEYS
-- ============================================================
-- Only an Argon2id hash and a display prefix are stored, never the key.

CREATE TYPE api_key_kind AS ENUM ('sdk', 'server');

CREATE TABLE api_keys (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    kind           api_key_kind NOT NULL,
    key_prefix     TEXT NOT NULL UNIQUE,
    key_hash       TEXT NOT NULL,
    created_by     UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ
);

-- ============================================================
-- AUDIT LOG (US-07)
-- ============================================================
-- Written in the same transaction as the change it records. actor_id and
-- environment_id are deliberately not foreign keys: an ON DELETE action
-- would have to UPDATE audit rows, which the trigger below forbids.

CREATE TABLE audit_logs (
    id             BIGSERIAL PRIMARY KEY,
    actor_id       UUID,
    actor_email    TEXT NOT NULL,
    environment_id UUID,
    action         TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_id    TEXT NOT NULL,
    severity       TEXT NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'critical')),
    diff_before    JSONB,
    diff_after     JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
CREATE INDEX idx_audit_logs_environment ON audit_logs(environment_id);

-- A trigger rather than REVOKE: on Supabase the backend connects as the
-- table owner, and an owner can simply re-grant itself revoked privileges.
CREATE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only';
END $$;

CREATE TRIGGER audit_logs_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_append_only();

CREATE TRIGGER audit_logs_no_truncate
    BEFORE TRUNCATE ON audit_logs
    FOR EACH STATEMENT EXECUTE FUNCTION audit_logs_append_only();

ALTER TABLE environments ENABLE ROW LEVEL SECURITY;
ALTER TABLE flags        ENABLE ROW LEVEL SECURITY;
ALTER TABLE flag_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys     ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_logs   ENABLE ROW LEVEL SECURITY;
