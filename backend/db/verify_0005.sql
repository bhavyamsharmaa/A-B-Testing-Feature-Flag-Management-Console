-- Read-only verification for migration 0005. Run it right after applying
-- 0005 (which must itself run right after the new backend is deployed):
--
--   psql "$DATABASE_URL" -f backend/db/verify_0005.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH tenant_tables (tbl) AS (VALUES
    ('environments'), ('flags'), ('flag_configs'), ('experiments'),
    ('user_environment_roles'), ('audit_logs')
),
checks (ord, name, ok) AS (
    SELECT 10 + row_number() OVER (ORDER BY t.tbl), 'no DEFAULT on ' || t.tbl || '.workspace_id',
           COALESCE((SELECT c.column_default IS NULL
                     FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = t.tbl AND c.column_name = 'workspace_id'), false)
    FROM tenant_tables t UNION ALL
    SELECT 20 + row_number() OVER (ORDER BY t.tbl), t.tbl || '.workspace_id is still NOT NULL',
           COALESCE((SELECT c.is_nullable = 'NO'
                     FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = t.tbl AND c.column_name = 'workspace_id'), false)
    FROM tenant_tables t UNION ALL
    SELECT 30, 'composite foreign keys from 0004 are still in place',
           (SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conname IN (
               'flag_configs_flag_workspace_fkey', 'flag_configs_environment_workspace_fkey',
               'experiments_flag_workspace_fkey', 'experiments_environment_workspace_fkey',
               'user_environment_roles_environment_workspace_fkey', 'user_environment_roles_member_workspace_fkey')) = 6
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
