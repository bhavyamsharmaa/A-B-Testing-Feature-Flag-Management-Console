-- Read-only verification for migration 0014 (lifecycle). Run it right after applying 0014:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0014.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 10, 'workspaces has the three deletion columns, all nullable',
           (SELECT count(*) FROM information_schema.columns WHERE table_name = 'workspaces'
              AND column_name IN ('deleted_at', 'deleted_by', 'purge_after') AND is_nullable = 'YES') = 3
    UNION ALL
    SELECT 11, 'deleted_at and purge_after are set together (validated CHECK)',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'workspaces'::regclass AND conname = 'workspaces_deletion_pair' AND convalidated)
    UNION ALL
    SELECT 12, 'the purge lookup index is partial on scheduled workspaces',
           EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_workspaces_deleted' AND indexdef LIKE '%purge_after%' AND indexdef LIKE '%deleted_at IS NOT NULL%')
    UNION ALL
    SELECT 20, 'ownership_offers, workspace_tombstones and experiment_variations exist',
           (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'
              AND table_name IN ('ownership_offers', 'workspace_tombstones', 'experiment_variations')) = 3
    UNION ALL
    SELECT 21, 'row level security is on, with no policies (as in 0004)',
           (SELECT count(*) FROM pg_class WHERE relname IN ('ownership_offers', 'workspace_tombstones', 'experiment_variations') AND relkind = 'r' AND relrowsecurity) = 3
           AND NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename IN ('ownership_offers', 'workspace_tombstones', 'experiment_variations'))
    UNION ALL
    SELECT 22, 'workspace_id is NOT NULL on ownership_offers and experiment_variations',
           (SELECT count(*) FROM information_schema.columns WHERE table_name IN ('ownership_offers', 'experiment_variations')
              AND column_name = 'workspace_id' AND is_nullable = 'NO' AND column_default IS NULL) = 2
    UNION ALL
    SELECT 30, 'at most one open ownership offer per workspace (partial unique index)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'one_pending_offer_per_workspace' AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%(workspace_id)%' AND indexdef LIKE '%pending%')
    UNION ALL
    SELECT 31, 'offer status, resolution and distinct-people checks exist and are validated',
           (SELECT count(*) FROM pg_constraint WHERE conrelid = 'ownership_offers'::regclass AND convalidated
              AND conname IN ('ownership_offers_status_valid', 'ownership_offers_different_people', 'ownership_offers_resolved', 'ownership_offers_expiry')) = 4
    UNION ALL
    SELECT 40, 'at most one control per experiment (partial unique index)',
           EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'one_control_per_experiment' AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%(experiment_id)%' AND indexdef LIKE '%is_control%')
    UNION ALL
    SELECT 41, 'a variation row cannot leave its experiment''s workspace (composite foreign key)',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'experiment_variations'::regclass AND contype = 'f'
                   AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (experiment_id, workspace_id) REFERENCES experiments(id, workspace_id) ON DELETE CASCADE')
    UNION ALL
    SELECT 42, 'every experiment has exactly one control',
           NOT EXISTS (SELECT 1 FROM experiments x
                       WHERE (SELECT count(*) FROM experiment_variations v WHERE v.experiment_id = x.id AND v.is_control) <> 1)
    UNION ALL
    SELECT 43, 'every experiment''s control is its flag''s first variation unless it was changed later (backfill)',
           NOT EXISTS (SELECT 1 FROM experiments x
                       JOIN flags f ON f.id = x.flag_id
                       JOIN experiment_variations v ON v.experiment_id = x.id AND v.is_control
                       WHERE v.position = 0 AND v.variation_id <> f.variations -> 0 ->> 'id')
    UNION ALL
    SELECT 50, 'the tombstone table has no foreign keys (it outlives the workspace)',
           NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'workspace_tombstones'::regclass AND contype = 'f')
    UNION ALL
    SELECT 60, 'the audit log still refuses updates and truncation, and deletion unless a purge is due',
           (SELECT prosrc FROM pg_proc WHERE proname = 'audit_logs_append_only') LIKE '%purge_after <= now()%'
           AND (SELECT prosrc FROM pg_proc WHERE proname = 'audit_logs_append_only') LIKE '%audit_logs is append-only%'
           AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'audit_logs'::regclass AND tgname = 'audit_logs_no_update_delete')
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
