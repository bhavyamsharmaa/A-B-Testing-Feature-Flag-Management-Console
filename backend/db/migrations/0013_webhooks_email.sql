-- Outbox, webhooks and invite email. NOT PART OF EARLIER RELEASES: see docs/ROLLOUT.md.
--
-- One shared outbox serves two kinds of delivery, 'webhook' (an HTTPS POST to a
-- customer's endpoint) and 'email' (an invite). A row is written in the SAME
-- transaction as the change that caused it, so a change is never lost and a
-- rolled-back change never sends anything. The worker (cmd/outbox, or
-- POST /internal/outbox/run) claims due rows with a LEASE, FOR UPDATE SKIP LOCKED
-- in the claim statement only, makes the network call OUTSIDE any transaction, and
-- records the result. A crash mid-delivery means the row is delivered again later:
-- delivery is AT LEAST ONCE, and delivery_id is stable across every attempt so a
-- receiver can dedupe. See docs/WEBHOOKS.md and docs/OUTBOX.md.
--
--   outbox.status   pending | delivering | delivered | failed | dead
--                   failed = an attempt failed and a retry is scheduled
--                   (next_attempt_at); dead = out of attempts, kept for inspection
--   outbox.payload  webhook: the event envelope (ids, names, no secrets);
--                   email: the recipient and the invite token while the row is
--                   open. The token is REMOVED from the payload once delivered
--                   or dead (the application does it; verify_0013.sql cannot).
--   webhook_endpoints.secret_enc / previous_secret_enc
--                   AES-256-GCM sealed signing secrets (key in the environment,
--                   never here). previous_* is the old secret during a rotation's
--                   grace period.
--
-- PART 1 (separate, earlier commit): scheduled changes remember the API token that
-- created them (created_by_token_id) so revoking or expiring it can cancel what it
-- left pending and the executor can refuse a change whose token is gone.
--
-- Every new table has workspace_id (NOT NULL, no default), RLS on with no policies
-- (as in 0004/0005), composite foreign keys that keep each row's references inside
-- its workspace, and indexes that lead with workspace_id, except the three the
-- workers use across workspaces by time or recipient (marked below).
--
-- LOCKS (existing tables):
--   scheduled_changes   one nullable column (catalog only, no rewrite); the composite
--                       FK is added NOT VALID then VALIDATEd (no rows reference a
--                       token yet: instant); brief ACCESS EXCLUSIVE for ADD COLUMN.
--   workspace_invites   ADD CONSTRAINT UNIQUE (id, workspace_id) builds an index:
--                       SHARE lock, blocks invite writes (not reads) for the build,
--                       milliseconds on a table this small. Needed for the outbox's
--                       composite FK.
--   workspaces, environments, auth.users, api_tokens
--                       SHARE ROW EXCLUSIVE for milliseconds while FKs are created.
-- New tables are empty. Nothing is rewritten.
--
-- Rehearse: db/verify_0013.sql (read-only) and db/rehearsal.md.

BEGIN;

-- ---- part 1 -------------------------------------------------------------------------------
ALTER TABLE scheduled_changes ADD COLUMN created_by_token_id UUID;
ALTER TABLE scheduled_changes
    ADD CONSTRAINT scheduled_changes_token_workspace_fkey
    FOREIGN KEY (created_by_token_id, workspace_id) REFERENCES api_tokens (id, workspace_id) NOT VALID;
ALTER TABLE scheduled_changes VALIDATE CONSTRAINT scheduled_changes_token_workspace_fkey;
CREATE INDEX idx_scheduled_changes_token ON scheduled_changes (workspace_id, created_by_token_id) WHERE created_by_token_id IS NOT NULL;

-- ---- invites can be referenced by (id, workspace_id) --------------------------------------
ALTER TABLE workspace_invites ADD CONSTRAINT workspace_invites_id_workspace_unique UNIQUE (id, workspace_id);

-- ---- webhook endpoints --------------------------------------------------------------------
CREATE TABLE webhook_endpoints (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id                UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    url                         TEXT NOT NULL,
    description                 TEXT NOT NULL DEFAULT '',
    event_types                 TEXT[] NOT NULL,
    -- NULL: every environment of the workspace.
    environment_id              UUID,
    enabled                     BOOLEAN NOT NULL DEFAULT true,
    secret_enc                  BYTEA NOT NULL,
    -- the last characters of the secret, to tell secrets apart in the console
    secret_hint                 TEXT NOT NULL,
    previous_secret_enc         BYTEA,
    previous_secret_expires_at  TIMESTAMPTZ,
    consecutive_failures        INTEGER NOT NULL DEFAULT 0,
    failing_since               TIMESTAMPTZ,
    last_success_at             TIMESTAMPTZ,
    last_failure_at             TIMESTAMPTZ,
    disabled_reason             TEXT,
    disabled_at                 TIMESTAMPTZ,
    created_by                  UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT webhook_endpoints_id_workspace_unique UNIQUE (id, workspace_id),
    CONSTRAINT webhook_endpoints_environment_workspace_fkey
        FOREIGN KEY (environment_id, workspace_id) REFERENCES environments (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT webhook_endpoints_sizes CHECK (
        char_length(url) BETWEEN 1 AND 2048
        AND char_length(description) <= 200
        AND cardinality(event_types) BETWEEN 1 AND 20
        AND char_length(secret_hint) <= 16
        AND (disabled_reason IS NULL OR char_length(disabled_reason) <= 300)
        AND consecutive_failures >= 0),
    CONSTRAINT webhook_endpoints_rotation_pair CHECK ((previous_secret_enc IS NULL) = (previous_secret_expires_at IS NULL))
);
CREATE INDEX idx_webhook_endpoints_workspace ON webhook_endpoints (workspace_id, created_at);
-- emit: the enabled endpoints of one workspace
CREATE INDEX idx_webhook_endpoints_enabled ON webhook_endpoints (workspace_id) WHERE enabled;

-- ---- the outbox ---------------------------------------------------------------------------
CREATE TABLE outbox (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL,
    event_type        TEXT NOT NULL,
    -- one id per event, shared by every endpoint's delivery of it
    event_id          UUID NOT NULL,
    -- one id per delivery, stable across all of its attempts: receivers dedupe on this
    delivery_id       UUID NOT NULL DEFAULT gen_random_uuid(),
    endpoint_id       UUID,
    invite_id         UUID,
    -- webhook: the endpoint id; email: the recipient (lower case). Per-target limits use it.
    target_key        TEXT NOT NULL,
    payload           JSONB NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner       TEXT,
    lease_expires_at  TIMESTAMPTZ,
    last_error        TEXT,
    last_http_status  INTEGER,
    last_duration_ms  INTEGER,
    last_response_hint TEXT,
    last_attempt_at   TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at      TIMESTAMPTZ,
    CONSTRAINT outbox_delivery_id_key UNIQUE (delivery_id),
    CONSTRAINT outbox_id_workspace_unique UNIQUE (id, workspace_id),
    CONSTRAINT outbox_endpoint_workspace_fkey
        FOREIGN KEY (endpoint_id, workspace_id) REFERENCES webhook_endpoints (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT outbox_invite_workspace_fkey
        FOREIGN KEY (invite_id, workspace_id) REFERENCES workspace_invites (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT outbox_kind_valid CHECK (kind IN ('webhook', 'email')),
    CONSTRAINT outbox_status_valid CHECK (status IN ('pending', 'delivering', 'delivered', 'failed', 'dead')),
    CONSTRAINT outbox_target_matches_kind CHECK (
        (kind = 'webhook' AND endpoint_id IS NOT NULL AND invite_id IS NULL)
        OR (kind = 'email' AND invite_id IS NOT NULL AND endpoint_id IS NULL)),
    CONSTRAINT outbox_lease_when_delivering CHECK (status <> 'delivering' OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CONSTRAINT outbox_sizes CHECK (
        jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536
        AND (last_error IS NULL OR char_length(last_error) <= 500)
        AND (last_response_hint IS NULL OR char_length(last_response_hint) <= 300)
        AND char_length(target_key) BETWEEN 1 AND 320
        AND attempts >= 0)
);
-- The worker's claim looks across workspaces by time (like scheduled_changes): not workspace-leading.
CREATE INDEX idx_outbox_claim ON outbox (next_attempt_at) WHERE status IN ('pending', 'failed');
CREATE INDEX idx_outbox_lease ON outbox (lease_expires_at) WHERE status = 'delivering';
-- the purge job looks across workspaces by age
CREATE INDEX idx_outbox_purge ON outbox (updated_at) WHERE status IN ('delivered', 'dead');
-- per-recipient daily limit on invite email counts across workspaces by recipient
CREATE INDEX idx_outbox_recipient ON outbox (target_key, created_at) WHERE kind = 'email';
-- workspace-leading: the delivery log of an endpoint, the status of an invite's email, queue-size and quota counts
CREATE INDEX idx_outbox_endpoint ON outbox (workspace_id, endpoint_id, created_at DESC) WHERE endpoint_id IS NOT NULL;
CREATE INDEX idx_outbox_invite ON outbox (workspace_id, invite_id, created_at DESC) WHERE invite_id IS NOT NULL;
CREATE INDEX idx_outbox_queue ON outbox (workspace_id, kind, status, created_at);

ALTER TABLE webhook_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand, back up first)
-- ============================================================
-- Deploy a backend without webhooks and invite email first. Discards every endpoint,
-- every secret and every queued or logged delivery.
-- BEGIN;
-- DROP TABLE outbox;
-- DROP TABLE webhook_endpoints;
-- ALTER TABLE workspace_invites DROP CONSTRAINT workspace_invites_id_workspace_unique;
-- DROP INDEX idx_scheduled_changes_token;
-- ALTER TABLE scheduled_changes DROP CONSTRAINT scheduled_changes_token_workspace_fkey;
-- ALTER TABLE scheduled_changes DROP COLUMN created_by_token_id;
-- COMMIT;
