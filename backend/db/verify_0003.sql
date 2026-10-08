-- Read-only verification for migration 0003. Run AFTER applying it:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0003.sql
--
-- Prints one PASS/FAIL row per check, then a summary row. The transaction is
-- READ ONLY, so any accidental write would error rather than happen.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (VALUES
    -- tables
    ( 1, 'table experiments exists',        to_regclass('public.experiments')        IS NOT NULL),
    ( 2, 'table experiment_metrics exists', to_regclass('public.experiment_metrics') IS NOT NULL),
    ( 3, 'table events exists',             to_regclass('public.events')             IS NOT NULL),

    -- enum types and their labels
    ( 4, 'enum experiment_status = draft,running,stopped', COALESCE((
            SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder) = ARRAY['draft','running','stopped']
            FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
            WHERE t.typname = 'experiment_status' AND t.typnamespace = 'public'::regnamespace), false)),
    ( 5, 'enum metric_type = conversion,value', COALESCE((
            SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder) = ARRAY['conversion','value']
            FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
            WHERE t.typname = 'metric_type' AND t.typnamespace = 'public'::regnamespace), false)),

    -- every index (primary keys and unique constraints create indexes too)
    ( 6, 'index experiments_pkey',                  to_regclass('public.experiments_pkey')                  IS NOT NULL),
    ( 7, 'index experiments_env_key_unique',        to_regclass('public.experiments_env_key_unique')        IS NOT NULL),
    ( 8, 'index one_running_per_flag_env',          to_regclass('public.one_running_per_flag_env')          IS NOT NULL),
    ( 9, 'index idx_experiments_flag',              to_regclass('public.idx_experiments_flag')              IS NOT NULL),
    (10, 'index experiment_metrics_pkey',           to_regclass('public.experiment_metrics_pkey')           IS NOT NULL),
    (11, 'index experiment_metrics_event_unique',   to_regclass('public.experiment_metrics_event_unique')   IS NOT NULL),
    (12, 'index one_primary_metric',                to_regclass('public.one_primary_metric')                IS NOT NULL),
    (13, 'index events_pkey',                       to_regclass('public.events_pkey')                       IS NOT NULL),
    (14, 'index one_dedup_event_per_subject',       to_regclass('public.one_dedup_event_per_subject')       IS NOT NULL),
    (15, 'index idx_events_results',                to_regclass('public.idx_events_results')                IS NOT NULL),
    (16, 'index idx_events_time',                   to_regclass('public.idx_events_time')                   IS NOT NULL),

    -- the three partial unique indexes: unique AND have a WHERE predicate
    (17, 'one_running_per_flag_env is partial unique', COALESCE((
            SELECT i.indisunique AND i.indpred IS NOT NULL
            FROM pg_index i WHERE i.indexrelid = to_regclass('public.one_running_per_flag_env')), false)),
    (18, 'one_primary_metric is partial unique', COALESCE((
            SELECT i.indisunique AND i.indpred IS NOT NULL
            FROM pg_index i WHERE i.indexrelid = to_regclass('public.one_primary_metric')), false)),
    (19, 'one_dedup_event_per_subject is partial unique', COALESCE((
            SELECT i.indisunique AND i.indpred IS NOT NULL
            FROM pg_index i WHERE i.indexrelid = to_regclass('public.one_dedup_event_per_subject')), false)),

    -- row-level security
    (20, 'RLS enabled on experiments',        COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.experiments')),        false)),
    (21, 'RLS enabled on experiment_metrics', COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.experiment_metrics')), false)),
    (22, 'RLS enabled on events',             COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.events')),             false)),

    -- check constraints
    (23, 'check experiments_status_timestamps',          EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'experiments_status_timestamps' AND contype = 'c')),
    (24, 'check experiment_metrics_event_not_reserved',  EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'experiment_metrics_event_not_reserved' AND contype = 'c')),
    (25, 'check events_value_matches_dedup',             EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'events_value_matches_dedup' AND contype = 'c'))
),
results AS (
    SELECT ord, name, CASE WHEN ok THEN 'PASS' ELSE 'FAIL' END AS result FROM checks
)
SELECT name, result FROM (
    SELECT ord, name, result FROM results
    UNION ALL
    SELECT 99, 'SUMMARY: ' || count(*) FILTER (WHERE result = 'PASS') || ' of ' || count(*) || ' checks passed',
           CASE WHEN bool_and(result = 'PASS') THEN 'PASS' ELSE 'FAIL' END
    FROM results
) r ORDER BY ord;

ROLLBACK;
