-- API tokens: scoped bearer tokens so CI pipelines and scripts can call the
-- control plane without a person's login session.
--
-- NOT PART OF THE MULTI-TENANCY, EXPERIMENTS, SEGMENTS OR SCHEDULING RELEASES. It
-- needs workspaces and environments (0004) and audit_logs (0001), nothing else.
-- See docs/ROLLOUT.md: it ships with a later release than multi-tenancy.
--
-- api_tokens             one row per token: only a SHA-256 hash of it, a display
--                        prefix, the workspace, the creator, a role (never above
--                        the creator's, and capped again at every request by the
--                        creator's CURRENT role), an expiry (never "never"), and
--                        last-used and revoked times.
-- api_token_environments the optional environment restriction: a token with no
--                        rows here reaches every environment of its workspace.
-- audit_logs             two new columns say WHO acted: actor_type ('user', the
--                        default for every existing row, or 'api_token') and
--                        actor_detail (for a token: its id, name, display prefix
--                        and creator). Existing rows and writers are unchanged.
--
-- Everything is workspace-scoped: workspace_id on both new tables, composite
-- foreign keys so a restriction can only name an environment of the token's own
-- workspace, and indexes that lead with workspace_id, except the unique index on
-- token_hash, which is how a presented token is found BEFORE its workspace is
-- known. RLS is on with no policies, as in 0004.
--
-- LOCKS: two new, empty tables. The foreign keys take SHARE ROW EXCLUSIVE on
-- workspaces, environments and auth.users for milliseconds while created (writes
-- to those tables wait, reads do not). audit_logs: ADD COLUMN with a constant
-- default is catalog-only on PostgreSQL 11+ (no rewrite, no row is updated, so the
-- append-only trigger does not fire); it takes ACCESS EXCLUSIVE for a moment. The
-- CHECK on actor_type is added NOT VALID and then validated, so the validating scan
-- takes only SHARE UPDATE EXCLUSIVE (inserts continue), as in 0009.
--
-- Rehearse: db/verify_0012.sql (read-only) and db/rehearsal.md.

BEGIN;

CREATE TABLE api_tokens (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    -- "hlt_" and 8 hex characters: shown in lists and audit entries; not secret.
    token_prefix     TEXT NOT NULL,
    -- SHA-256 of the whole token. The token carries 256 random bits, so a fast
    -- hash is enough (see docs/API_TOKENS.md); the raw value is never stored.
    token_hash       BYTEA NOT NULL,
    -- The highest role the token may use. owner is not offered: nothing a token
    -- may reach needs it. The effective role is the LOWER of this and the
    -- creator's current role.
    role             TEXT NOT NULL,
    -- NULL after the creator's account is deleted: the token then fails.
    created_by       UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_by_email TEXT NOT NULL,
    expires_at       TIMESTAMPTZ NOT NULL,
    last_used_at     TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT api_tokens_hash_key UNIQUE (token_hash),
    CONSTRAINT api_tokens_id_workspace_unique UNIQUE (id, workspace_id),
    CONSTRAINT api_tokens_role_valid CHECK (role IN ('viewer', 'editor', 'admin')),
    CONSTRAINT api_tokens_sizes CHECK (
        char_length(name) BETWEEN 1 AND 60 AND octet_length(token_hash) = 32 AND char_length(token_prefix) = 12),
    -- Never "never expires", and never more than a year (366 days: a leap year).
    CONSTRAINT api_tokens_expiry CHECK (expires_at > created_at AND expires_at <= created_at + interval '366 days')
);

CREATE TABLE api_token_environments (
    workspace_id   UUID NOT NULL,
    token_id       UUID NOT NULL,
    environment_id UUID NOT NULL,
    PRIMARY KEY (workspace_id, token_id, environment_id),
    CONSTRAINT api_token_environments_token_fkey
        FOREIGN KEY (token_id, workspace_id) REFERENCES api_tokens (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT api_token_environments_environment_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX idx_api_tokens_workspace ON api_tokens (workspace_id, created_at DESC);
-- Per-workspace and per-user caps count the active ones.
CREATE INDEX idx_api_tokens_active ON api_tokens (workspace_id, created_by) WHERE revoked_at IS NULL;

ALTER TABLE api_tokens             ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_token_environments ENABLE ROW LEVEL SECURITY;

ALTER TABLE audit_logs ADD COLUMN actor_type   TEXT NOT NULL DEFAULT 'user';
ALTER TABLE audit_logs ADD COLUMN actor_detail JSONB;
ALTER TABLE audit_logs
    ADD CONSTRAINT audit_logs_actor_type_valid CHECK (actor_type IN ('user', 'api_token')) NOT VALID;
ALTER TABLE audit_logs VALIDATE CONSTRAINT audit_logs_actor_type_valid;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Deploy a backend without API tokens FIRST (it would otherwise serve routes
-- over missing tables, and write audit rows with columns that are gone).
-- Dropping discards every token (CI pipelines using them stop working) and the
-- actor type of audit rows that tokens wrote. Back up first.
--
-- BEGIN;
-- ALTER TABLE audit_logs DROP CONSTRAINT audit_logs_actor_type_valid;
-- ALTER TABLE audit_logs DROP COLUMN actor_detail, DROP COLUMN actor_type;
-- DROP TABLE api_token_environments;
-- DROP TABLE api_tokens;
-- COMMIT;
