-- Read-only verification for migration 0013 (outbox, webhooks, invite email). Run it right after applying 0013:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0013.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'webhook_endpoints and outbox exist',
           (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('webhook_endpoints', 'outbox')) = 2
    UNION ALL
    SELECT 11, 'workspace_id is NOT NULL with no default on both',
           (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
              AND table_name IN ('webhook_endpoints', 'outbox') AND column_name = 'workspace_id'
              AND is_nullable = 'NO' AND column_default IS NULL) = 2
    UNION ALL
    SELECT 20, 'row level security is on, with no policies (as in 0004)',
           (SELECT count(*) FROM pg_class WHERE relname IN ('webhook_endpoints', 'outbox') AND relkind = 'r' AND relrowsecurity) = 2
           AND NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename IN ('webhook_endpoints', 'outbox'))
    UNION ALL
    SELECT 30, 'composite foreign keys keep an outbox row''s endpoint and invite inside its workspace',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'outbox'::regclass AND contype = 'f'
              AND conname IN ('outbox_endpoint_workspace_fkey', 'outbox_invite_workspace_fkey')
              AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (%, workspace_id) REFERENCES %(id, workspace_id) ON DELETE CASCADE') = 2
    UNION ALL
    SELECT 31, 'an endpoint''s environment filter cannot leave its workspace',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'webhook_endpoints'::regclass AND conname = 'webhook_endpoints_environment_workspace_fkey'
                   AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (environment_id, workspace_id) REFERENCES environments(id, workspace_id)%')
    UNION ALL
    SELECT 32, 'a scheduled change''s token cannot leave its workspace, and the key is validated',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'scheduled_changes'::regclass AND conname = 'scheduled_changes_token_workspace_fkey' AND convalidated
                   AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (created_by_token_id, workspace_id) REFERENCES api_tokens(id, workspace_id)%')
    UNION ALL
    SELECT 33, 'delivery_id is unique (receivers dedupe on it)',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'outbox'::regclass AND conname = 'outbox_delivery_id_key' AND contype = 'u'
                   AND pg_get_constraintdef(oid) = 'UNIQUE (delivery_id)')
    UNION ALL
    SELECT 34, 'kind, status, target, lease and size checks exist and are validated',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'outbox'::regclass AND convalidated
              AND conname IN ('outbox_kind_valid', 'outbox_status_valid', 'outbox_target_matches_kind', 'outbox_lease_when_delivering', 'outbox_sizes')) = 5
    UNION ALL
    SELECT 35, 'the endpoint checks exist and are validated',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'webhook_endpoints'::regclass AND convalidated
              AND conname IN ('webhook_endpoints_sizes', 'webhook_endpoints_rotation_pair')) = 2
    UNION ALL
    SELECT 36, 'no column of either table is plain text for a secret (the signing secret is bytea)',
           (SELECT data_type FROM information_schema.columns WHERE table_name = 'webhook_endpoints' AND column_name = 'secret_enc') = 'bytea'
           AND (SELECT data_type FROM information_schema.columns WHERE table_name = 'webhook_endpoints' AND column_name = 'previous_secret_enc') = 'bytea'
           AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'webhook_endpoints' AND column_name IN ('secret', 'signing_secret'))
    UNION ALL
    SELECT 40, 'the per-workspace indexes lead with workspace_id',
           (SELECT count(*) FROM pg_indexes WHERE tablename IN ('outbox', 'webhook_endpoints')
              AND indexname IN ('idx_outbox_endpoint', 'idx_outbox_invite', 'idx_outbox_queue', 'idx_webhook_endpoints_workspace', 'idx_webhook_endpoints_enabled')
              AND indexdef LIKE '%(workspace_id%') = 5
    UNION ALL
    SELECT 41, 'the claim, lease, purge and recipient indexes exist (they look across workspaces by design)',
           (SELECT count(*) FROM pg_indexes WHERE tablename = 'outbox'
              AND indexname IN ('idx_outbox_claim', 'idx_outbox_lease', 'idx_outbox_purge', 'idx_outbox_recipient')) = 4
    UNION ALL
    SELECT 42, 'the claim index is partial on the claimable statuses and leads with next_attempt_at',
           EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_outbox_claim'
                   AND indexdef LIKE '%(next_attempt_at)%' AND indexdef LIKE '%pending%' AND indexdef LIKE '%failed%')
    UNION ALL
    SELECT 50, 'workspace_invites can be referenced by (id, workspace_id)',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'workspace_invites'::regclass AND conname = 'workspace_invites_id_workspace_unique' AND contype = 'u')
),
results AS (
    SELECT ord, name, CASE WHEN ok THEN 'PASS' ELSE 'FAIL' END AS result FROM checks
)
SELECT name, result FROM (
    SELECT ord, name, result FROM results
    UNION ALL
    SELECT 999, 'SUMMARY: ' || count(*) FILTER (WHERE result = 'PASS') || ' of ' || count(*) || ' checks passed',
           CASE WHEN bool_and(result = 'PASS') THEN 'PASS' ELSE 'FAIL' END
    FROM results
) r ORDER BY ord;

ROLLBACK;
