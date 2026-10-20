-- Read-only verification for migration 0015 (audit gate). Run it right after applying 0015:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0015.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'the append-only function requires the purge transaction''s own setting',
           (SELECT prosrc FROM pg_proc WHERE proname = 'audit_logs_append_only') LIKE '%helios.audit_purge%'
           AND (SELECT prosrc FROM pg_proc WHERE proname = 'audit_logs_append_only') LIKE '%txid_current()%'
    UNION ALL
    SELECT 11, 'and still requires the workspace to be past its purge date',
           (SELECT prosrc FROM pg_proc WHERE proname = 'audit_logs_append_only') LIKE '%purge_after <= now()%'
    UNION ALL
    SELECT 12, 'the update/delete and truncate triggers are still attached',
           EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'audit_logs'::regclass AND tgname = 'audit_logs_no_update_delete')
           AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'audit_logs'::regclass AND tgname = 'audit_logs_no_truncate')
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
