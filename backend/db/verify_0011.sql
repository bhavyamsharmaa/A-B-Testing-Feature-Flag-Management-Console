-- Read-only verification for migration 0011 (scheduled changes). Run it right after applying 0011:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0011.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'scheduled_changes exists',
           to_regclass('public.scheduled_changes') IS NOT NULL
    UNION ALL
    SELECT 11, 'workspace_id is NOT NULL with no default',
           COALESCE((SELECT is_nullable = 'NO' AND column_default IS NULL FROM information_schema.columns
                     WHERE table_schema = 'public' AND table_name = 'scheduled_changes' AND column_name = 'workspace_id'), false)
    UNION ALL
    SELECT 20, 'row level security is on, with no policies (as in 0004)',
           COALESCE((SELECT relrowsecurity FROM pg_class WHERE relname = 'scheduled_changes' AND relkind = 'r'), false)
           AND NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = 'scheduled_changes')
    UNION ALL
    SELECT 30, 'composite foreign keys keep the flag and the environment in one workspace, ON DELETE CASCADE',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'scheduled_changes'::regclass AND contype = 'f'
              AND conname IN ('scheduled_changes_flag_workspace_fkey', 'scheduled_changes_environment_workspace_fkey')
              AND pg_get_constraintdef(oid) LIKE '%(%_id, workspace_id) REFERENCES %(id, workspace_id) ON DELETE CASCADE%') = 2
    UNION ALL
    SELECT 31, 'idempotency keys are unique per workspace',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'scheduled_changes'::regclass AND conname = 'scheduled_changes_idempotency_unique'
                   AND pg_get_constraintdef(oid) = 'UNIQUE (workspace_id, idempotency_key)')
    UNION ALL
    SELECT 40, 'the kind, status, size, lease and finish-time checks exist and are validated',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'scheduled_changes'::regclass AND convalidated AND conname IN
              ('scheduled_changes_kind_valid', 'scheduled_changes_status_valid', 'scheduled_changes_sizes',
               'scheduled_changes_running_has_lease', 'scheduled_changes_finished_has_time')) = 5
    UNION ALL
    SELECT 50, 'the listing indexes lead with workspace_id',
           (SELECT count(*) FROM pg_indexes WHERE tablename = 'scheduled_changes'
              AND indexname IN ('idx_scheduled_changes_flag', 'idx_scheduled_changes_run_at')
              AND indexdef LIKE '%(workspace_id, %') = 2
    UNION ALL
    SELECT 51, 'the claim query has its partial indexes on due_at (pending) and lease_expires_at (running)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'scheduled_changes' AND indexname = 'idx_scheduled_changes_due'
                   AND indexdef LIKE '%(due_at)%' AND indexdef LIKE '%status = ''pending''%')
           AND EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'scheduled_changes' AND indexname = 'idx_scheduled_changes_lease'
                   AND indexdef LIKE '%(lease_expires_at)%' AND indexdef LIKE '%status = ''running''%')
    UNION ALL
    SELECT 60, 'every row belongs to a flag and an environment of its own workspace',
           NOT EXISTS (SELECT 1 FROM scheduled_changes s
                       JOIN flags f ON f.id = s.flag_id JOIN environments e ON e.id = s.environment_id
                       WHERE f.workspace_id <> s.workspace_id OR e.workspace_id <> s.workspace_id)
    UNION ALL
    SELECT 61, 'no running row lacks a lease, no finished row lacks a finish time',
           NOT EXISTS (SELECT 1 FROM scheduled_changes WHERE (status = 'running' AND lease_owner IS NULL)
                       OR (status NOT IN ('pending', 'running') AND finished_at IS NULL))
    UNION ALL
    SELECT 70, 'no existing table was changed: flag_configs still has 11 columns',
           (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'flag_configs') = 11
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
