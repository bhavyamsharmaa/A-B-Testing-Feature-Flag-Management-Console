# Rehearsing the release 1 migrations on a second Supabase project

Goal: prove **0004, 0006 and 0007** (and, separately, **0005**) on a throwaway copy of your setup before touching the live database. Nothing here uses
your production project. This is the optional, longer rehearsal against a real Supabase project; the release itself follows
`docs/RELEASE1_RUNBOOK.md`, which rehearses on a **local restore of the production dump** and is not replaced by this file.

Rules: never use the production connection here. Describe the rehearsal database once, as a service called `helios_rehearsal` in `~/.pg_service.conf`
(host, port, dbname, user, sslmode) with its password in `~/.pgpass`; then no URL or password ever appears on a command line. `psql`, `backup_db.sh`
and the Go programs (`DATABASE_URL="service=helios_rehearsal"`) all read it. Keep tokens and SDK keys in environment variables.
Run the commands from the repository root, on branch `release/1`.

## 0. Prepare

1. Create a new, empty Supabase project (free tier is fine). Add its connection (Project Settings → Database) as the `helios_rehearsal` service, and set
   `SUPABASE_URL` (the project URL) and `SUPABASE_ANON_KEY` (its **public** key) in your shell.
2. In its dashboard, Authentication → Users → **Add user**, create three users with passwords, each marked confirmed:
   `owner@test.dev`, `editor@test.dev`, `other@test.dev`.
3. Check out the **old** backend next to the new one, so you can run both: `git worktree add --detach ../helios-old 6c90844`.

## 1. Build the "before" database (0001-0003 + realistic data)

```bash
for f in 0001_core 0002_user_environment_roles 0003_experiments; do
  psql "service=helios_rehearsal" -v ON_ERROR_STOP=1 -f backend/db/migrations/$f.sql
done
```

Then, in the SQL editor, insert data that exercises every part of the backfill:

```sql
-- Roles: owner is admin everywhere (so becomes the legacy workspace owner);
-- editor has dev only; other (a third user) gets nothing yet.
INSERT INTO user_environment_roles (user_id, environment_id, role)
SELECT u.id, e.id, 'admin' FROM auth.users u CROSS JOIN environments e WHERE u.email = 'owner@test.dev';
INSERT INTO user_environment_roles (user_id, environment_id, role)
SELECT u.id, e.id, 'editor' FROM auth.users u JOIN environments e ON e.key = 'dev' WHERE u.email = 'editor@test.dev';

-- Two flags with a config in every environment.
INSERT INTO flags (key, name, variation_type, variations, created_by)
SELECT k, k, 'boolean', '[{"id":"on","value":true},{"id":"off","value":false}]'::jsonb, u.id
FROM (VALUES ('demo-banner'), ('checkout-v2')) AS f(k), auth.users u WHERE u.email = 'owner@test.dev';
INSERT INTO flag_configs (flag_id, environment_id, salt, fallthrough_variation_id, enabled)
SELECT f.id, e.id, md5(random()::text), 'off', true FROM flags f CROSS JOIN environments e;

-- An experiment (draft) with a metric, and audit rows incl. NULL-environment ones.
INSERT INTO experiments (environment_id, flag_id, key, name)
SELECT e.id, f.id, 'banner-test', 'Banner test' FROM environments e, flags f WHERE e.key = 'dev' AND f.key = 'demo-banner';
INSERT INTO experiment_metrics (experiment_id, name, event_name, type, is_primary)
SELECT id, 'Clicks', 'banner_click', 'conversion', true FROM experiments;
INSERT INTO audit_logs (actor_email, environment_id, action, resource_type, resource_id)
VALUES ('owner@test.dev', NULL, 'flag.create', 'flag', 'demo-banner'),
       ('owner@test.dev', (SELECT id FROM environments WHERE key = 'dev'), 'flag.update', 'flag', 'demo-banner');
```

Mint two real SDK keys with the OLD code (it has no `-workspace` flag):

```bash
(cd ../helios-old/backend && DATABASE_URL="service=helios_rehearsal" go run ./cmd/mkkey -env production)
(cd ../helios-old/backend && DATABASE_URL="service=helios_rehearsal" go run ./cmd/mkkey -env dev)
```

Save the printed keys as `SDK_PROD` and `SDK_DEV` (they are shown once).

## 2. Record the "before" baseline

```bash
psql "service=helios_rehearsal" -At -c "SELECT id, key FROM environments ORDER BY id"   > /tmp/envs.before
psql "service=helios_rehearsal" -At -c "SELECT id, environment_id, key_prefix, revoked_at FROM api_keys ORDER BY id" > /tmp/keys.before
psql "service=helios_rehearsal" -At -c "SELECT id, key FROM flags ORDER BY id"          > /tmp/flags.before
```

Row counts are **a query you run yourself** (`backup_db.sh` writes only the dump):

```bash
psql "service=helios_rehearsal" -At -c "select 'environments',count(*) from environments union all select 'flags',count(*) from flags union all select 'flag_configs',count(*) from flag_configs union all select 'api_keys',count(*) from api_keys union all select 'experiments',count(*) from experiments union all select 'experiment_metrics',count(*) from experiment_metrics union all select 'events',count(*) from events union all select 'audit_logs',count(*) from audit_logs union all select 'user_environment_roles',count(*) from user_environment_roles order by 1" > /tmp/counts.before
```

In a second terminal start the OLD backend (`cd ../helios-old/backend && DATABASE_URL="service=helios_rehearsal" go run ./cmd/api`, with `SUPABASE_URL`
set), then record a live evaluation through it:

```bash
curl -s -X POST localhost:8080/evaluate -H "X-Helios-SDK-Key: $SDK_PROD" -H 'Content-Type: application/json' \
  -d '{"context":{"subjectKey":"u1"},"flagKeys":["demo-banner","checkout-v2"]}' > /tmp/eval.before
```

Back up (this rehearses `scripts/backup_db.sh`, which takes the connection from `PGSERVICE` only and ignores `DATABASE_URL`):

```bash
PGSERVICE=helios_rehearsal backend/scripts/backup_db.sh
```

Prove the dump restores with the runbook's Step 3 (or `tools/release1-dryrun/restore_copy.sh <DUMP> <NEW_LOCAL_DATABASE_NAME>`).

## 3. Apply 0004, 0006 and 0007 and verify (NOT 0005)

```bash
for m in 0004_workspaces 0006_workspace_roles_invites 0007_workspace_keeps_an_owner; do
  psql "service=helios_rehearsal" -v ON_ERROR_STOP=1 -f backend/db/migrations/$m.sql || break
  psql "service=helios_rehearsal" -f backend/db/verify_${m%%_*}.sql      # every row PASS
done
```

(The comments at the top of the migration files predate this order: ignore "apply 0005 immediately".)
Expect: one workspace owned by `owner@test.dev`; `owner` and `editor` are its members; `other` is not in any workspace. Then compare:

```bash
psql "service=helios_rehearsal" -At -c "SELECT id, key FROM environments ORDER BY id" | diff - /tmp/envs.before && echo envs identical
psql "service=helios_rehearsal" -At -c "SELECT id, environment_id, key_prefix, revoked_at FROM api_keys ORDER BY id" | diff - /tmp/keys.before && echo keys identical
psql "service=helios_rehearsal" -At -c "SELECT id, key FROM flags ORDER BY id" | diff - /tmp/flags.before && echo flags identical
```

All three must print "identical", and the count query of section 2 must give the same numbers. Also check the NULL-environment audit row now
carries the legacy workspace id: `SELECT action, workspace_id FROM audit_logs;`.

## 4. The overlap window: OLD backend on the migrated database

With the old backend still running, confirm: `/evaluate` with `$SDK_PROD` still returns the same result as `/tmp/eval.before` (`diff`), the console
(pointed at it) still logs in, and creating a flag through the old backend works (it lands in the legacy workspace through the temporary default).
**Expected failure:** adding a member through the old backend answers 500 from 0006 on. Then stop the old backend.

## 5. Start the new backend

```bash
cd backend && DATABASE_URL="service=helios_rehearsal" CORS_ALLOWED_ORIGINS=http://localhost:5173 go run ./cmd/api
```

`GET /me` as `owner@test.dev` must return the **legacy** workspace, role `owner`, and the original three environment UUIDs, not a new workspace.
A token for a test user (type the password when asked; the value is not shown):

```bash
read -rs "PW?password: "; echo
TOKEN=$(curl -s -X POST "$SUPABASE_URL/auth/v1/token?grant_type=password" -H "apikey: $SUPABASE_ANON_KEY" -H 'Content-Type: application/json' \
  -d "{\"email\":\"owner@test.dev\",\"password\":\"$PW\"}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
curl -s localhost:8080/me -H "Authorization: Bearer $TOKEN"
```

(`read -rs "PW?password: "` is zsh, the macOS default; in bash use `read -rsp "password: " PW`.) **Do not apply 0005 here**: see section 8.

## 6. Provisioning, isolation and concurrency

- `GET /me` as `other@test.dev`: a new workspace with `dev`, `staging` and `production`, role `owner`. `SELECT count(*) FROM workspaces;` is now 2.
- Concurrency: sign in a fourth user and fire two `/me` calls at once with the same token (e.g. `curl ... & curl ... & wait`). The workspace count must
  grow by exactly one, and both calls return the same workspace id.
- Isolation, and the rest of the product by hand: **the click-through in `docs/RELEASE1_RUNBOOK.md`, Step 9**, with the console pointed at this
  backend. Then `API_BASE=http://localhost:8080 ENV_NAME=dev TOKEN="$TOKEN" backend/scripts/smoke_experiments.sh` for the experiments lifecycle.
- Quotas: create 50 flags in one workspace (a loop of POSTs), the 51st must answer 409 `QUOTA_EXCEEDED`; mint 10 SDK keys with
  `go run ./cmd/mkkey -workspace <id> -env dev` (in `backend/`, with `DATABASE_URL` as above), the 11th must fail.
- SDK continuity: `/evaluate` with `$SDK_PROD` must still equal `/tmp/eval.before`.

## 7. Negative rehearsals (each on a fresh local restore of the dump from section 2)

1. **No all-environment admin:** demote the owner to `editor` in `staging`, run 0004. It must stop with the "no user is admin in every environment" error
   and leave the database unchanged (`verify_0004.sql` reports the tables missing).
2. **Fresh database, no users:** apply 0001-0004, 0006, 0007 to an empty database. The legacy workspace is created with no owner and keeps the seed
   environments; the first user to call `/me` still gets their own workspace.
3. **Rollback warning:** with two workspaces present, try the rollback block at the bottom of 0004. It must fail or merge tenants, which is why the file
   says to restore from backup instead.

## 8. 0005: a later, separate step (gate G6b)

In a real release 0005 is applied **only after the new backend has been live and observed**, on an explicit "go" (`docs/ROLLOUT.md`, gate G6b), never right
after the deploy. Before it, redeploying the old backend is possible (with the limits in the runbook's rollback table); after it, the old backend cannot
write. To see this on the rehearsal database:

```bash
psql "service=helios_rehearsal" -v ON_ERROR_STOP=1 -f backend/db/migrations/0005_drop_workspace_defaults.sql
psql "service=helios_rehearsal" -f backend/db/verify_0005.sql      # every row PASS
```

Then start the old backend again: sign-in, reads and `/evaluate` still work, and every write answers 500 (`null value in column "workspace_id"`), even
toggling an existing flag, because its audit row has no workspace. The commented ROLLBACK block at the bottom of `0005` re-adds the defaults.

## 9. Clean up

Delete the rehearsal Supabase project, the `../helios-old` worktree (`git worktree remove ../helios-old`), `backend/backups/*.dump`, the files in `/tmp`
and the `helios_rehearsal` entry in `~/.pg_service.conf` and `~/.pgpass`.
