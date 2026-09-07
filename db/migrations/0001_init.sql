-- Helios: A/B Testing & Feature Flag Management Console
-- Migration 0001: core schema
-- Design choices are called out inline where they map directly to a PRD requirement.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ============================================================
-- ORGANIZATIONS / ENVIRONMENTS
-- ============================================================
-- Environment isolation is structural (separate rows / FKs), not procedural,
-- so no code path can leak a Dev toggle into Prod (PRD §4, flags/flag_configs).

CREATE TABLE environments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT NOT NULL UNIQUE, -- e.g. 'dev', 'staging', 'production'
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- USERS / RBAC  (US-06)
-- ============================================================

CREATE TYPE role_type AS ENUM ('viewer', 'editor', 'approver', 'admin');

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Role is scoped per-environment: an Editor in Staging is not implicitly an Editor in Prod.
CREATE TABLE user_environment_roles (
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    role           role_type NOT NULL,
    PRIMARY KEY (user_id, environment_id)
);

-- SDK keys: read-only, safe for public exposure. Server keys: secret, role-bearing.
-- Key material is never stored in plaintext (PRD §5) — only an Argon2id hash + display prefix.
CREATE TYPE api_key_kind AS ENUM ('sdk', 'server');

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    kind            api_key_kind NOT NULL,
    key_prefix      TEXT NOT NULL,        -- shown in UI/audit, e.g. 'sdk_live_8f2a'
    key_hash        TEXT NOT NULL,        -- Argon2id hash, never plaintext
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ
);

-- ============================================================
-- SEGMENTS  (US-02)
-- ============================================================

CREATE TABLE segments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    name            TEXT NOT NULL,
    -- ordered array of {attribute, operator, value} clauses, top-down, first match wins (US-02 AC-1)
    rules           JSONB NOT NULL DEFAULT '[]',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, key)
);

-- ============================================================
-- FLAGS  (US-01)
-- ============================================================
-- flags: definition (cross-environment identity, variation types)
-- flag_configs: per-environment behaviour (enabled, rules, rollout, salt)
-- This split is the PRD's core environment-isolation mechanism.

CREATE TYPE variation_type AS ENUM ('boolean', 'string', 'number', 'json');

CREATE TABLE flags (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key             TEXT NOT NULL UNIQUE,           -- duplicate key -> 409 FLAG_KEY_EXISTS (US-01 AC-2)
    name            TEXT NOT NULL,
    description     TEXT,
    variation_type  variation_type NOT NULL,
    -- 2-20 typed variations (US-01): [{ "id": "on", "value": true }, ...]
    variations      JSONB NOT NULL,
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT variations_bounds CHECK (jsonb_array_length(variations) BETWEEN 2 AND 20)
);

CREATE TABLE flag_configs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flag_id         UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    enabled         BOOLEAN NOT NULL DEFAULT false,  -- new flag = false in every env by default (US-01 AC-1)
    -- targeting rules, evaluated top-down before falling through to rollout (US-02/US-03)
    -- each rule: { "segment_id": ..., "variation_id": ... } OR inline clauses
    targeting_rules JSONB NOT NULL DEFAULT '[]',
    -- percentage rollout per variation, integer basis points summing to 100000
    rollout         JSONB,
    -- per flag+environment salt: prevents one user always landing "included" across every flag (US-03 AC-4)
    salt            TEXT NOT NULL DEFAULT encode(gen_random_bytes(16), 'hex'),
    fallthrough_variation_id TEXT NOT NULL,
    version         BIGINT NOT NULL DEFAULT 1,       -- bumped on every write; SDKs use this to detect staleness
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (flag_id, environment_id)
);

CREATE INDEX idx_flag_configs_environment ON flag_configs(environment_id);

-- ============================================================
-- EXPERIMENTS  (US-04, US-05)
-- ============================================================

CREATE TYPE experiment_status AS ENUM ('draft', 'running', 'stopped', 'completed');

CREATE TABLE experiments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flag_id             UUID NOT NULL REFERENCES flags(id) ON DELETE CASCADE,
    environment_id      UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    status              experiment_status NOT NULL DEFAULT 'draft',
    primary_metric_key  TEXT NOT NULL,
    -- integer weights per variation summing to 100000 basis points (US-04 AC-1)
    traffic_split       JSONB NOT NULL,
    baseline_rate       NUMERIC,          -- for required-sample-size calc (US-04 AC-3)
    min_detectable_effect NUMERIC,
    required_sample_size_per_arm INTEGER,
    started_at          TIMESTAMPTZ,
    stopped_at          TIMESTAMPTZ,
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only one running experiment per flag (US-04 edge case: 409 CONFLICTING_EXPERIMENT).
-- Enforced with a partial unique index rather than application logic alone.
CREATE UNIQUE INDEX uq_one_running_experiment_per_flag
    ON experiments(flag_id) WHERE status = 'running';

-- First-exposure record. UNIQUE(experiment_id, subject_key) so SDK retries
-- can't inflate the denominator (PRD §4 data model table).
CREATE TABLE exposures (
    id              BIGSERIAL PRIMARY KEY,
    experiment_id   UUID NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    subject_key     TEXT NOT NULL,        -- userKey / anonymous id
    variation_id    TEXT NOT NULL,
    exposed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (experiment_id, subject_key)
);

CREATE INDEX idx_exposures_experiment ON exposures(experiment_id);

-- Custom metric events (US-10). Deduplicated within 24h server-side via client_uuid.
CREATE TABLE metric_events (
    id              BIGSERIAL PRIMARY KEY,
    experiment_id   UUID REFERENCES experiments(id) ON DELETE CASCADE,
    client_uuid     UUID NOT NULL,        -- dedupe key (US-10 AC-2)
    subject_key     TEXT NOT NULL,
    event_name      TEXT NOT NULL,
    properties      JSONB NOT NULL DEFAULT '{}',
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (client_uuid)
);

CREATE INDEX idx_metric_events_experiment ON metric_events(experiment_id);

-- ============================================================
-- AUDIT LOG  (US-07)
-- ============================================================
-- Append-only by construction: UPDATE/DELETE are revoked at the DB role level below.
-- Every mutating handler MUST insert here in the SAME transaction as the change (US-07 AC-1).

CREATE TABLE audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    actor_id        UUID REFERENCES users(id),
    actor_email     TEXT NOT NULL,          -- denormalized so log survives user deletion
    environment_id  UUID REFERENCES environments(id),
    action          TEXT NOT NULL,          -- e.g. 'flag.toggle', 'flag.kill', 'experiment.start'
    resource_type   TEXT NOT NULL,          -- 'flag' | 'segment' | 'experiment' | 'api_key' | ...
    resource_id     TEXT NOT NULL,
    severity        TEXT NOT NULL DEFAULT 'info',  -- 'info' | 'critical' (kill switch is always critical, US-08 AC-3)
    diff_before     JSONB,                  -- secrets redacted to "[REDACTED]" at write time (US-07 AC-3)
    diff_after      JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
CREATE INDEX idx_audit_logs_environment ON audit_logs(environment_id);

-- Enforce INSERT/SELECT-only at the DB role level (US-07 AC-2).
-- Run once the application role exists, e.g.:
--   REVOKE UPDATE, DELETE ON audit_logs FROM helios_app;
--   GRANT INSERT, SELECT ON audit_logs TO helios_app;
