-- Read-only verification for migration 0007. Run it AFTER applying 0007:
--
--   psql "$DATABASE_URL" -f backend/db/verify_0007.sql
--
-- One PASS/FAIL row per check, then a summary. The transaction is READ ONLY.

BEGIN READ ONLY;

WITH checks (ord, name, ok) AS (
    SELECT 1, 'function workspace_keeps_an_owner exists',
           EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'workspace_keeps_an_owner' AND pronamespace = 'public'::regnamespace) UNION ALL
    SELECT 2, 'constraint trigger workspace_keeps_an_owner exists on workspace_members',
           EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'workspace_keeps_an_owner' AND tgrelid = to_regclass('public.workspace_members') AND NOT tgisinternal) UNION ALL
    SELECT 3, 'it is a constraint trigger (checked at commit)',
           COALESCE((SELECT tgconstraint <> 0 FROM pg_trigger WHERE tgname = 'workspace_keeps_an_owner' AND tgrelid = to_regclass('public.workspace_members')), false) UNION ALL
    SELECT 4, 'it is deferrable and initially deferred',
           COALESCE((SELECT tgdeferrable AND tginitdeferred FROM pg_trigger WHERE tgname = 'workspace_keeps_an_owner' AND tgrelid = to_regclass('public.workspace_members')), false) UNION ALL
    SELECT 5, 'it is enabled (not disabled or replica-only)',
           COALESCE((SELECT tgenabled = 'O' FROM pg_trigger WHERE tgname = 'workspace_keeps_an_owner' AND tgrelid = to_regclass('public.workspace_members')), false) UNION ALL
    SELECT 6, 'it fires on UPDATE and DELETE only (an INSERT of the first owner is fine)',
           COALESCE((SELECT (tgtype & 16) <> 0 AND (tgtype & 8) <> 0 AND (tgtype & 4) = 0
                     FROM pg_trigger WHERE tgname = 'workspace_keeps_an_owner' AND tgrelid = to_regclass('public.workspace_members')), false) UNION ALL
    SELECT 7, 'no workspace with members lacks an owner (unowned legacy workspaces excepted)',
           NOT EXISTS (SELECT 1 FROM workspaces w
                       WHERE w.owner_id IS NOT NULL
                         AND EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id = w.id)
                         AND NOT EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id = w.id AND m.role = 'owner'))
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
