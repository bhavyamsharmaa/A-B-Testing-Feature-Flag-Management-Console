# Rehearsing migrations 0001-0005 on a second Supabase project

Goal: prove 0004 (workspaces) and 0005 on a throwaway copy of your setup before
touching the live database. Nothing here uses your production project.

Rules: never put the production `DATABASE_URL` in your shell for this. Keep the
rehearsal URL in `REHEARSAL_DATABASE_URL`, and tokens in environment variables
(never on a command line you paste anywhere).

## 0. Prepare

1. Create a new, empty Supabase project (free tier is fine). Copy its Postgres
   connection string (Project Settings → Database) into `REHEARSAL_DATABASE_URL`,
   and its URL into `SUPABASE_URL` for the backend.
2. In its dashboard, Authentication → Users → **Add user**, create three users
   with passwords: `owner@test.dev`, `editor@test.dev`, `other@test.dev`.
3. Check out the **old** backend next to the new one, so you can run both:
   `git worktree add ../helios-old 6c90844`.

## 1. Build the "before" database (0001 + 0002 + realistic data)

```bash
for f in 0001_core 0002_user_environment_roles 0003_experiments; do
  psql "$REHEARSAL_DATABASE_URL" -v ON_ERROR_STOP=1 -f backend/db/migrations/$f.sql
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
(cd ../helios-old/backend && DATABASE_URL="$REHEARSAL_DATABASE_URL" go run ./cmd/mkkey -env production)
(cd ../helios-old/backend && DATABASE_URL="$REHEARSAL_DATABASE_URL" go run ./cmd/mkkey -env dev)
```

Save the printed keys as `SDK_PROD` and `SDK_DEV` (they are shown once).

## 2. Record the "before" baseline

```bash
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, key FROM environments ORDER BY id"   > /tmp/envs.before
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, environment_id, key_prefix, revoked_at FROM api_keys ORDER BY id" > /tmp/keys.before
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, key FROM flags ORDER BY id"          > /tmp/flags.before
# A live evaluation through the OLD backend (run it with DATABASE_URL=$REHEARSAL_DATABASE_URL):
curl -s -X POST localhost:8080/evaluate -H "X-Helios-SDK-Key: $SDK_PROD" -H 'Content-Type: application/json' \
  -d '{"context":{"subjectKey":"u1"},"flagKeys":["demo-banner","checkout-v2"]}' > /tmp/eval.before
```

Back up and prove the backup restores (this rehearses `scripts/backup_db.sh`):

```bash
DATABASE_URL="$REHEARSAL_DATABASE_URL" backend/scripts/backup_db.sh
```

## 3. Apply 0004 and verify

```bash
psql "$REHEARSAL_DATABASE_URL" -v ON_ERROR_STOP=1 -f backend/db/migrations/0004_workspaces.sql
psql "$REHEARSAL_DATABASE_URL" -f backend/db/verify_0004.sql      # every row PASS
```

Expect: one workspace owned by `owner@test.dev`; `owner` and `editor` are its
members; `other` is not in any workspace. Then compare:

```bash
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, key FROM environments ORDER BY id" | diff - /tmp/envs.before && echo envs identical
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, environment_id, key_prefix, revoked_at FROM api_keys ORDER BY id" | diff - /tmp/keys.before && echo keys identical
psql "$REHEARSAL_DATABASE_URL" -At -c "SELECT id, key FROM flags ORDER BY id" | diff - /tmp/flags.before && echo flags identical
```

All three must print "identical". Also check the NULL-environment audit row now
carries the legacy workspace id: `SELECT action, workspace_id FROM audit_logs;`.

## 4. The overlap window: OLD backend on the migrated database

With 0004 applied and 0005 NOT applied, run the old backend against the
database and confirm: `/evaluate` with `$SDK_PROD` still returns the same
result as `/tmp/eval.before` (`diff`), the console (pointed at it) still logs
in, and creating a flag through the old backend works (it lands in the legacy
workspace through the temporary default).

## 5. Deploy the new backend, then 0005 immediately

1. Start the new backend (`cd backend && go run ./cmd/api`) against the database.
2. `GET /me` as `owner@test.dev`: must return the **legacy** workspace and the
   original three environment UUIDs, not a new workspace.
3. Apply 0005 right away and verify:

```bash
psql "$REHEARSAL_DATABASE_URL" -v ON_ERROR_STOP=1 -f backend/db/migrations/0005_drop_workspace_defaults.sql
psql "$REHEARSAL_DATABASE_URL" -f backend/db/verify_0005.sql      # every row PASS
```

4. Confirm the old backend can no longer insert (expected after 0005): that is
   why 0005 follows the deploy immediately.

## 6. Provisioning, isolation and concurrency

- `GET /me` as `other@test.dev`: a new workspace with `dev`, `staging` and
  `production`, admin in each. `SELECT count(*) FROM workspaces;` is now 2.
- Concurrency: sign in a fourth user and fire two `/me` calls at once with the
  same token (e.g. `curl ... & curl ... & wait`). The workspace count must grow
  by exactly one, and both calls return the same workspace id.
- `A_TOKEN` = owner's token, `B_TOKEN` = other's token:
  `API_BASE=http://localhost:8080 A_TOKEN=... B_TOKEN=... backend/scripts/smoke_tenancy.sh`
  (all PASS). Then `smoke_experiments.sh` for the experiments lifecycle.
- Quotas: create 50 flags in one workspace (a loop of POSTs), the 51st must
  answer 409 `QUOTA_EXCEEDED`; mint 10 SDK keys with
  `go run ./cmd/mkkey -workspace <id> -env dev`, the 11th must fail.
- SDK continuity: `/evaluate` with `$SDK_PROD` must still equal `/tmp/eval.before`.

## 7. Negative rehearsals (each on a fresh restore of the dump from step 2)

Restore with `pg_restore` (see the comment in `backup_db.sh`) into a clean
project or schema, then:

1. **No all-environment admin:** demote the owner to `editor` in `staging`, run
   0004. It must stop with the "no user is admin in every environment" error and
   leave the database unchanged (`verify_0004.sql` reports the tables missing).
2. **Fresh database, no users:** apply 0001-0004 on an empty project. The
   legacy workspace is created with no owner and keeps the seed environments;
   the first user to call `/me` still gets their own workspace.
3. **Rollback warning:** after step 6 (two workspaces exist), try the rollback
   block at the bottom of 0004. It must fail or merge tenants, which is why the
   file says to restore from backup instead.

## 8. Clean up

Delete the rehearsal Supabase project, the `../helios-old` worktree
(`git worktree remove ../helios-old`), and `backend/backups/*.dump`.
