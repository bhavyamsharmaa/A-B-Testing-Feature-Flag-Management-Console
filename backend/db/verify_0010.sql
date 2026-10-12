-- Read-only verification for migration 0010 (segments). Run it right after applying 0010:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0010.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'segments and segment_members exist',
           (SELECT count(*) FROM information_schema.tables
            WHERE table_schema = 'public' AND table_name IN ('segments', 'segment_members')) = 2
    UNION ALL
    SELECT 11, 'workspace_id is NOT NULL with no default on both tables',
           (SELECT count(*) FROM information_schema.columns c
            WHERE c.table_schema = 'public' AND c.table_name IN ('segments', 'segment_members')
              AND c.column_name = 'workspace_id' AND c.is_nullable = 'NO' AND c.column_default IS NULL) = 2
    UNION ALL
    SELECT 20, 'row level security is enabled on both tables (as in 0004)',
           (SELECT count(*) FROM pg_class WHERE relname IN ('segments', 'segment_members') AND relkind = 'r' AND relrowsecurity) = 2
    UNION ALL
    SELECT 21, 'no RLS policies were added (same as the other tenant tables)',
           NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename IN ('segments', 'segment_members'))
    UNION ALL
    SELECT 30, 'segment keys are unique per workspace',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'segments'::regclass AND conname = 'segments_key_unique' AND contype = 'u'
                   AND pg_get_constraintdef(oid) = 'UNIQUE (workspace_id, key)')
    UNION ALL
    SELECT 31, 'segment_members has a composite foreign key to (id, workspace_id) of segments, ON DELETE CASCADE',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'segment_members'::regclass AND conname = 'segment_members_segment_workspace_fkey'
                   AND contype = 'f' AND confrelid = 'segments'::regclass
                   AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (segment_id, workspace_id) REFERENCES segments(id, workspace_id) ON DELETE CASCADE%')
    UNION ALL
    SELECT 32, 'the members primary key leads with workspace_id and ends with subject_key (one index probe per subject)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'segment_members' AND indexname = 'segment_members_pkey'
                   AND indexdef LIKE '%(workspace_id, segment_id, subject_key)%')
    UNION ALL
    SELECT 33, 'every index on both tables leads with workspace_id, except the segments primary key (looked up by id)',
           NOT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename IN ('segments', 'segment_members')
                       AND indexname NOT IN ('segments_pkey', 'segments_id_workspace_unique')
                       AND indexdef NOT LIKE '%(workspace_id,%')
    UNION ALL
    SELECT 40, 'the size and format checks exist',
           (SELECT count(*) FROM pg_constraint WHERE conname IN
              ('segments_key_format', 'segments_sizes', 'segment_members_subject_size') AND convalidated) = 3
    UNION ALL
    SELECT 50, 'no member points at a segment of another workspace',
           NOT EXISTS (SELECT 1 FROM segment_members m JOIN segments s ON s.id = m.segment_id WHERE s.workspace_id <> m.workspace_id)
    UNION ALL
    SELECT 51, 'no existing table was changed: flag_configs still has exactly the columns it had at 0004-0009',
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
