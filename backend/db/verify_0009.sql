-- Read-only verification for migration 0009. Run it right after applying 0009:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0009.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'events.exposure_verified exists, is boolean and nullable, has no default',
           COALESCE((SELECT c.data_type = 'boolean' AND c.is_nullable = 'YES' AND c.column_default IS NULL
                     FROM information_schema.columns c
                     WHERE c.table_schema = 'public' AND c.table_name = 'events' AND c.column_name = 'exposure_verified'), false)
    UNION ALL
    SELECT 20, 'the CHECK exists and is validated',
           COALESCE((SELECT convalidated FROM pg_constraint
                     WHERE conrelid = 'events'::regclass AND conname = 'events_exposure_verified_only_on_exposures'), false)
    UNION ALL
    SELECT 21, 'no non-exposure row carries a verification result',
           NOT EXISTS (SELECT 1 FROM events WHERE exposure_verified IS NOT NULL AND event_name <> '$exposure')
    UNION ALL
    SELECT 30, 'the partial index for unverified exposures exists and leads with workspace_id',
           EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'events' AND indexname = 'idx_events_unverified_exposures'
                   AND indexdef LIKE '%(workspace_id, experiment_id)%' AND indexdef LIKE '%exposure_verified = false%')
    UNION ALL
    SELECT 40, '0008 is in place (events.workspace_id is NOT NULL)',
           COALESCE((SELECT is_nullable = 'NO' FROM information_schema.columns
                     WHERE table_schema = 'public' AND table_name = 'events' AND column_name = 'workspace_id'), false)
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
