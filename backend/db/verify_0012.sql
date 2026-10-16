-- Read-only verification for migration 0012 (API tokens). Run it right after applying 0012:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0012.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'api_tokens and api_token_environments exist',
           (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('api_tokens', 'api_token_environments')) = 2
    UNION ALL
    SELECT 11, 'workspace_id is NOT NULL with no default on both',
           (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
              AND table_name IN ('api_tokens', 'api_token_environments') AND column_name = 'workspace_id'
              AND is_nullable = 'NO' AND column_default IS NULL) = 2
    UNION ALL
    SELECT 20, 'row level security is on, with no policies (as in 0004)',
           (SELECT count(*) FROM pg_class WHERE relname IN ('api_tokens', 'api_token_environments') AND relkind = 'r' AND relrowsecurity) = 2
           AND NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename IN ('api_tokens', 'api_token_environments'))
    UNION ALL
    SELECT 30, 'the token hash has a unique index',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'api_tokens'::regclass AND conname = 'api_tokens_hash_key' AND contype = 'u'
                   AND pg_get_constraintdef(oid) = 'UNIQUE (token_hash)')
    UNION ALL
    SELECT 31, 'an environment restriction cannot leave the token''s workspace (composite foreign keys, ON DELETE CASCADE)',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'api_token_environments'::regclass AND contype = 'f'
              AND pg_get_constraintdef(oid) LIKE '%, workspace_id) REFERENCES %(id, workspace_id) ON DELETE CASCADE%') = 2
    UNION ALL
    SELECT 32, 'the role, size and expiry checks exist and are validated',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'api_tokens'::regclass AND convalidated
              AND conname IN ('api_tokens_role_valid', 'api_tokens_sizes', 'api_tokens_expiry')) = 3
    UNION ALL
    SELECT 33, 'the expiry check forbids "never": expires_at is NOT NULL and bounded by a year',
           COALESCE((SELECT is_nullable = 'NO' FROM information_schema.columns WHERE table_name = 'api_tokens' AND column_name = 'expires_at'), false)
           AND EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_tokens_expiry' AND pg_get_constraintdef(oid) LIKE '%366 days%')
    UNION ALL
    SELECT 40, 'the listing indexes lead with workspace_id',
           (SELECT count(*) FROM pg_indexes WHERE tablename = 'api_tokens'
              AND indexname IN ('idx_api_tokens_workspace', 'idx_api_tokens_active') AND indexdef LIKE '%(workspace_id, %') = 2
    UNION ALL
    SELECT 50, 'audit_logs has actor_type (NOT NULL, default user) and actor_detail',
           COALESCE((SELECT is_nullable = 'NO' AND column_default LIKE '%user%' FROM information_schema.columns
                     WHERE table_name = 'audit_logs' AND column_name = 'actor_type'), false)
           AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'audit_logs' AND column_name = 'actor_detail')
    UNION ALL
    SELECT 51, 'the actor_type check exists and is validated',
           COALESCE((SELECT convalidated FROM pg_constraint WHERE conrelid = 'audit_logs'::regclass AND conname = 'audit_logs_actor_type_valid'), false)
    UNION ALL
    SELECT 52, 'every audit row written before tokens existed is a user action',
           NOT EXISTS (SELECT 1 FROM audit_logs WHERE actor_type = 'user' AND actor_detail IS NOT NULL)
    UNION ALL
    SELECT 60, 'every token belongs to a workspace and no restriction names another workspace''s environment',
           NOT EXISTS (SELECT 1 FROM api_token_environments r JOIN environments e ON e.id = r.environment_id WHERE e.workspace_id <> r.workspace_id)
    UNION ALL
    SELECT 61, 'no token outlives a year or has a bad hash length',
           NOT EXISTS (SELECT 1 FROM api_tokens WHERE octet_length(token_hash) <> 32 OR expires_at > created_at + interval '366 days')
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
