-- Read-only verification for migration 0008. Run it right after applying 0008:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0008.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'events.workspace_id exists, NOT NULL, no default',
           COALESCE((SELECT c.is_nullable = 'NO' AND c.column_default IS NULL FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = 'events' AND c.column_name = 'workspace_id'), false)
    UNION ALL
    SELECT 11, 'events.experiment_id and variation_id are nullable',
           (SELECT count(*) FROM information_schema.columns c
            WHERE c.table_schema = 'public' AND c.table_name = 'events'
              AND c.column_name IN ('experiment_id', 'variation_id') AND c.is_nullable = 'YES') = 2
    UNION ALL
    SELECT 12, 'events.dedup is gone',
           NOT EXISTS (SELECT 1 FROM information_schema.columns c
                       WHERE c.table_schema = 'public' AND c.table_name = 'events' AND c.column_name = 'dedup')
    UNION ALL
    SELECT 20, 'composite foreign keys on events are in place',
           (SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conname IN (
               'events_environment_workspace_fkey', 'events_experiment_environment_workspace_fkey')) = 2
    UNION ALL
    SELECT 21, 'unique index on (workspace, environment, event_id)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'events' AND indexname = 'events_event_id_unique'
                   AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%(workspace_id, environment_id, event_id)%')
    UNION ALL
    SELECT 22, 'unique index: one $exposure per (workspace, environment, experiment, subject)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'events' AND indexname = 'events_one_exposure_per_subject'
                   AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%$exposure%')
    UNION ALL
    SELECT 23, 'CHECK constraints (finite value, exposure complete, sizes) are in place',
           (SELECT count(*) FROM pg_constraint WHERE contype = 'c' AND conrelid = 'events'::regclass
              AND conname IN ('events_value_finite', 'events_exposure_complete', 'events_sizes')) = 3
    UNION ALL
    SELECT 24, 'every events index leads with workspace_id',
           NOT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'events'
                       AND indexname NOT IN ('events_pkey') AND indexdef NOT LIKE '%(workspace_id,%')
    UNION ALL
    SELECT 30, 'row level security is still enabled on events',
           (SELECT relrowsecurity FROM pg_class WHERE oid = 'events'::regclass)
    UNION ALL
    SELECT 31, 'no events row sits in a different workspace than its environment',
           NOT EXISTS (SELECT 1 FROM events ev JOIN environments e ON e.id = ev.environment_id WHERE e.workspace_id <> ev.workspace_id)
    UNION ALL
    SELECT 32, 'no events row sits in a different workspace or environment than its experiment',
           NOT EXISTS (SELECT 1 FROM events ev JOIN experiments x ON x.id = ev.experiment_id
                       WHERE x.workspace_id <> ev.workspace_id OR x.environment_id <> ev.environment_id)
    UNION ALL
    SELECT 33, 'no duplicate exposures',
           NOT EXISTS (SELECT 1 FROM events WHERE event_name = '$exposure'
                       GROUP BY workspace_id, environment_id, experiment_id, subject_key HAVING count(*) > 1)
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
