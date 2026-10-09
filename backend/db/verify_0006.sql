-- Read-only verification for migration 0006. Run it AFTER applying 0006:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0006.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 1, 'enum workspace_role = owner,admin,editor,viewer',
           COALESCE((SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder) = ARRAY['owner','admin','editor','viewer']
                     FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
                     WHERE t.typname = 'workspace_role' AND t.typnamespace = 'public'::regnamespace), false) UNION ALL
    SELECT 2, 'workspaces.slug is NOT NULL',
           COALESCE((SELECT is_nullable = 'NO' FROM information_schema.columns
                     WHERE table_schema = 'public' AND table_name = 'workspaces' AND column_name = 'slug'), false) UNION ALL
    SELECT 3, 'workspaces_slug_key is UNIQUE (slug)',
           COALESCE((SELECT pg_get_constraintdef(oid) = 'UNIQUE (slug)' FROM pg_constraint
                     WHERE conname = 'workspaces_slug_key' AND conrelid = to_regclass('public.workspaces')), false) UNION ALL
    SELECT 4, 'workspace_members.role is NOT NULL',
           COALESCE((SELECT is_nullable = 'NO' FROM information_schema.columns
                     WHERE table_schema = 'public' AND table_name = 'workspace_members' AND column_name = 'role'), false) UNION ALL
    SELECT 5, 'workspace_members primary key is (user_id, workspace_id)',
           COALESCE((SELECT pg_get_constraintdef(oid) = 'PRIMARY KEY (user_id, workspace_id)' FROM pg_constraint
                     WHERE conrelid = to_regclass('public.workspace_members') AND contype = 'p'), false) UNION ALL
    SELECT 6, 'workspace_members_user_workspace_unique still exists (target of a foreign key)',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_members_user_workspace_unique' AND contype = 'u') UNION ALL
    SELECT 7, 'table workspace_invites exists', to_regclass('public.workspace_invites') IS NOT NULL UNION ALL
    SELECT 8, 'RLS enabled on workspace_invites',
           COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.workspace_invites')), false) UNION ALL
    SELECT 9, 'one_open_invite_per_email is partial unique',
           COALESCE((SELECT i.indisunique AND i.indpred IS NOT NULL FROM pg_index i
                     WHERE i.indexrelid = to_regclass('public.one_open_invite_per_email')), false) UNION ALL
    SELECT 10, 'invites cannot be for the owner role',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = to_regclass('public.workspace_invites') AND contype = 'c'
                   AND pg_get_constraintdef(oid) LIKE '%owner%') UNION ALL
    SELECT 11, 'api_keys.workspace_id is uuid NOT NULL',
           COALESCE((SELECT data_type = 'uuid' AND is_nullable = 'NO' FROM information_schema.columns
                     WHERE table_schema = 'public' AND table_name = 'api_keys' AND column_name = 'workspace_id'), false) UNION ALL
    SELECT 12, 'api_keys_environment_workspace_fkey exists',
           EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_environment_workspace_fkey' AND contype = 'f') UNION ALL
    SELECT 13, 'every api_key shares its environment''s workspace',
           NOT EXISTS (SELECT 1 FROM api_keys k JOIN environments e ON e.id = k.environment_id WHERE k.workspace_id <> e.workspace_id) UNION ALL
    SELECT 14, 'every workspace has at least one owner or admin',
           NOT EXISTS (SELECT 1 FROM workspaces w
                       WHERE w.owner_id IS NOT NULL
                         AND NOT EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id = w.id AND m.role IN ('owner','admin'))) UNION ALL
    SELECT 15, 'every workspace owner_id is a member with role owner',
           NOT EXISTS (SELECT 1 FROM workspaces w
                       WHERE w.owner_id IS NOT NULL
                         AND NOT EXISTS (SELECT 1 FROM workspace_members m
                                         WHERE m.workspace_id = w.id AND m.user_id = w.owner_id AND m.role = 'owner'))
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
