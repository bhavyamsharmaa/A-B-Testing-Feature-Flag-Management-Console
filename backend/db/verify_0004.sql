-- Read-only verification for migration 0004. Run it AFTER applying 0004 and
-- BEFORE 0005 (it expects the transitional defaults to still be present):
--
--   psql "$DATABASE_URL" -f backend/db/verify_0004.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.
-- It cannot prove that environment UUIDs are unchanged: compare
-- `SELECT id, key FROM environments ORDER BY id` and `SELECT id FROM api_keys
-- ORDER BY id` from before and after (see db/rehearsal.md).

BEGIN READ ONLY;

WITH tenant_tables (tbl) AS (VALUES
    ('environments'), ('flags'), ('flag_configs'), ('experiments'),
    ('user_environment_roles'), ('audit_logs')
),
checks (ord, name, ok) AS (
    SELECT 1, 'table workspaces exists',        to_regclass('public.workspaces')        IS NOT NULL UNION ALL
    SELECT 2, 'table workspace_members exists', to_regclass('public.workspace_members') IS NOT NULL UNION ALL

    -- workspace_id column on every tenant table: present, uuid, NOT NULL
    SELECT 10 + row_number() OVER (ORDER BY t.tbl), 'workspace_id is uuid NOT NULL on ' || t.tbl,
           COALESCE((SELECT c.data_type = 'uuid' AND c.is_nullable = 'NO'
                     FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = t.tbl AND c.column_name = 'workspace_id'), false)
    FROM tenant_tables t UNION ALL

    -- transitional defaults present (0005 removes them)
    SELECT 20 + row_number() OVER (ORDER BY t.tbl), 'transitional DEFAULT present on ' || t.tbl || '.workspace_id (removed by 0005)',
           COALESCE((SELECT c.column_default IS NOT NULL
                     FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = t.tbl AND c.column_name = 'workspace_id'), false)
    FROM tenant_tables t UNION ALL

    -- one workspace per user
    SELECT 30, 'workspace_members.user_id is the primary key (one workspace per user)',
           COALESCE((SELECT pg_get_constraintdef(oid) = 'PRIMARY KEY (user_id)'
                     FROM pg_constraint WHERE conrelid = to_regclass('public.workspace_members') AND contype = 'p'), false) UNION ALL

    -- per-workspace uniqueness, same names as before
    SELECT 31, 'flags_key_key is UNIQUE (workspace_id, key)',
           COALESCE((SELECT pg_get_constraintdef(oid) = 'UNIQUE (workspace_id, key)'
                     FROM pg_constraint WHERE conname = 'flags_key_key' AND conrelid = to_regclass('public.flags')), false) UNION ALL
    SELECT 32, 'environments_key_key is UNIQUE (workspace_id, key)',
           COALESCE((SELECT pg_get_constraintdef(oid) = 'UNIQUE (workspace_id, key)'
                     FROM pg_constraint WHERE conname = 'environments_key_key' AND conrelid = to_regclass('public.environments')), false) UNION ALL
    SELECT 33, 'environments_key_not_uuid check exists',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'environments_key_not_uuid' AND contype = 'c') UNION ALL
    SELECT 34, 'no environment key looks like a UUID',
           NOT EXISTS (SELECT 1 FROM environments WHERE key ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') UNION ALL
    SELECT 35, 'flags_id_workspace_unique exists',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'flags_id_workspace_unique' AND contype = 'u') UNION ALL
    SELECT 36, 'environments_id_workspace_unique exists',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'environments_id_workspace_unique' AND contype = 'u') UNION ALL

    -- composite foreign keys
    SELECT 40 + row_number() OVER (ORDER BY n), 'foreign key ' || n || ' exists',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = n AND contype = 'f')
    FROM unnest(ARRAY[
        'flag_configs_flag_workspace_fkey', 'flag_configs_environment_workspace_fkey',
        'experiments_flag_workspace_fkey', 'experiments_environment_workspace_fkey',
        'user_environment_roles_environment_workspace_fkey', 'user_environment_roles_member_workspace_fkey'
    ]) AS n UNION ALL

    -- RLS
    SELECT 50, 'RLS enabled on workspaces',
           COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.workspaces')), false) UNION ALL
    SELECT 51, 'RLS enabled on workspace_members',
           COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.workspace_members')), false) UNION ALL

    -- index
    SELECT 52, 'index idx_audit_logs_workspace exists', to_regclass('public.idx_audit_logs_workspace') IS NOT NULL UNION ALL

    -- data consistency
    SELECT 60, 'no environment points at a missing workspace',
           NOT EXISTS (SELECT 1 FROM environments e LEFT JOIN workspaces w ON w.id = e.workspace_id WHERE w.id IS NULL) UNION ALL
    SELECT 61, 'every flag_config: flag, environment and row share one workspace',
           NOT EXISTS (SELECT 1 FROM flag_configs fc
                       JOIN flags f ON f.id = fc.flag_id
                       JOIN environments e ON e.id = fc.environment_id
                       WHERE f.workspace_id <> e.workspace_id OR fc.workspace_id <> e.workspace_id) UNION ALL
    SELECT 62, 'every experiment: flag, environment and row share one workspace',
           NOT EXISTS (SELECT 1 FROM experiments x
                       JOIN flags f ON f.id = x.flag_id
                       JOIN environments e ON e.id = x.environment_id
                       WHERE f.workspace_id <> e.workspace_id OR x.workspace_id <> e.workspace_id) UNION ALL
    SELECT 63, 'every role: environment and row share one workspace',
           NOT EXISTS (SELECT 1 FROM user_environment_roles r
                       JOIN environments e ON e.id = r.environment_id
                       WHERE r.workspace_id <> e.workspace_id) UNION ALL
    SELECT 64, 'every role holder is a member of that workspace',
           NOT EXISTS (SELECT 1 FROM user_environment_roles r
                       LEFT JOIN workspace_members m ON m.user_id = r.user_id AND m.workspace_id = r.workspace_id
                       WHERE m.user_id IS NULL) UNION ALL
    SELECT 65, 'every audit_logs.workspace_id names an existing workspace',
           NOT EXISTS (SELECT 1 FROM audit_logs a LEFT JOIN workspaces w ON w.id = a.workspace_id WHERE w.id IS NULL) UNION ALL
    SELECT 66, 'every api_key belongs to an environment in some workspace',
           NOT EXISTS (SELECT 1 FROM api_keys k LEFT JOIN environments e ON e.id = k.environment_id WHERE e.id IS NULL) UNION ALL
    SELECT 67, 'every workspace has at least one environment',
           NOT EXISTS (SELECT 1 FROM workspaces w WHERE NOT EXISTS (SELECT 1 FROM environments e WHERE e.workspace_id = w.id)) UNION ALL
    -- Before the new backend runs there is exactly one workspace (the legacy
    -- one). After it has provisioned users there are more: expect FAIL then.
    SELECT 68, 'exactly one workspace exists (right after 0004, before new users sign in)',
           (SELECT count(*) FROM workspaces) = 1
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
