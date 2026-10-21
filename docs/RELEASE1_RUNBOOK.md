# Release 1 runbook: multi-tenancy (by hand, one step at a time)

For the owner doing the live release. It is a plain checklist made from `docs/ROLLOUT.md` and checked against the code.
**Release 1 only:** branch `feature/workspaces-ui` (HEAD `1e2f5e6`, 25 commits on top of `main` = `6c90844`), migrations **0004, 0006, 0007**.
Migration **0005 is NOT part of this**: it removes defaults the old backend needs. Nothing after release 1 is in here.

How to read a step: **Do** (one sentence) / **Type** / **Where** / **Good** / **Wrong -> then**.
`<ANGLE BRACKETS>` are placeholders you fill in. Never paste a password, key or URL with a password into chat, a file or a commit.
Rule for every step: if the result is not exactly "Good", **stop and ask**. Do not try fixes on production.

---

## Step 0. Get the release code into its own folder (no switching branches)
**Do:** make a second checkout of the release branch so every command below runs the release's own scripts.
**Type:** `cd <YOUR REPO FOLDER> && git worktree add ~/helios-release1 feature/workspaces-ui && cd ~/helios-release1 && git log --oneline -1`
**Where:** your terminal. **Good:** the last line starts with `1e2f5e6`. **Wrong:** a different id, or "already exists" -> stop and ask.
All "Type" lines below that say `~/helios-release1` run from that folder.

## Step 1. Read-only sanity check of production (G1, part 1)
**Do:** prove the connection works and write down how much data there is.
**Type:** `psql "service=helios_prod" -At -c "select 'environments',count(*) from environments union all select 'flags',count(*) from flags union all select 'flag_configs',count(*) from flag_configs union all select 'api_keys',count(*) from api_keys union all select 'experiments',count(*) from experiments union all select 'experiment_metrics',count(*) from experiment_metrics union all select 'events',count(*) from events union all select 'audit_logs',count(*) from audit_logs union all select 'user_environment_roles',count(*) from user_environment_roles" | tee ~/helios-release1-counts-before.txt`
**Where:** your terminal (`helios_prod` is your `~/.pg_service.conf` entry; the password comes from `~/.pgpass`). It only reads.
**Good:** nine `name|number` lines, saved in the file. Keep them: you compare against them later.
**Wrong:** "service not found", "password authentication failed", or an error naming a missing table -> stop and ask.

## Step 2. Take the production dump (G1, part 2)
**Do:** save a full copy of the data (the `public` schema, in a compressed format).
**Type:** `cd ~/helios-release1 && PGSERVICE=helios_prod backend/scripts/backup_db.sh`
**Where:** your terminal. **Good:** it prints `wrote .../backend/backups/helios-<time>.dump (<bytes> bytes)`, the size is more than a few KB, no error. The file is private (mode 600) and git-ignored: never share it, it holds your users' data.
**Wrong:** "pg_dump not found", a "server version mismatch" message, or a tiny file -> stop and ask (the dump tool must be the same major version as the server or newer).

## Step 3. Prove the dump can be restored (on your own computer only)
**Do:** restore it into a throwaway local database and compare the counts with Step 1.
The dump has no `auth.users` (that table belongs to Supabase), so the restore is done in three parts and fake users are filled in.
**Type**, one at a time (`<DUMP>` = the file from Step 2; the local database server must be running: `<YOUR REPO FOLDER>/backend/scripts/local_pg.sh ensure`):
```
P="-h 127.0.0.1 -p 55432 -U helios"          # the local throwaway server, never production
createdb $P helios_rehearsal
psql $P -d helios_rehearsal -c "CREATE SCHEMA IF NOT EXISTS auth; CREATE TABLE IF NOT EXISTS auth.users (id uuid primary key default gen_random_uuid(), email text unique)"
pg_restore $P -d helios_rehearsal --no-owner --no-privileges --exit-on-error --section=pre-data <DUMP>
pg_restore $P -d helios_rehearsal --no-owner --no-privileges --exit-on-error --section=data <DUMP>
psql $P -d helios_rehearsal -c "INSERT INTO auth.users (id, email) SELECT u, u||'@rehearsal.invalid' FROM (SELECT created_by u FROM flags UNION SELECT created_by FROM api_keys UNION SELECT created_by FROM experiments UNION SELECT user_id FROM user_environment_roles) x WHERE u IS NOT NULL ON CONFLICT DO NOTHING"
pg_restore $P -d helios_rehearsal --no-owner --no-privileges --exit-on-error --section=post-data <DUMP>
```
Then run the Step 1 query against it: replace `psql "service=helios_prod"` with `psql $P -d helios_rehearsal` and save to `~/helios-release1-counts-restored.txt`, then `diff ~/helios-release1-counts-before.txt ~/helios-release1-counts-restored.txt && echo SAME`.
**Where:** your terminal, local only. **Good:** the last command prints `SAME`. **Wrong:** any `pg_restore` error, or a difference -> stop; the dump is not proven. Do not go on.

## Step 4. Turn off automatic deploys
**Do:** make sure nothing deploys by itself while you work.
**Where:** Render dashboard -> your backend service -> Settings -> Build & Deploy -> Auto-Deploy = **Off**. Vercel dashboard -> your project -> Settings -> Git: check production deploys come only from `main` (menu names can differ slightly).
**Good:** Render shows Auto-Deploy off. **Wrong:** you cannot find it -> stop and ask. (ROLLOUT.md notes Render auto-deploy was already off and Vercel previews skip non-main branches; confirm, do not assume.)

## Step 5. Set the environment variables (names only; you type the values)
**Do:** add the settings the release code reads. **Where:** Render dashboard -> Environment (backend); Vercel -> Settings -> Environment Variables (console). Save, but on Render choose **not** to redeploy yet.

| Where | Name | Needed? |
|---|---|---|
| Render | `DATABASE_URL`, `SUPABASE_URL`, `CORS_ALLOWED_ORIGINS` | **required**, already set: check they still exist |
| Render | `REDIS_URL` | already set (optional, flag-change push uses it) |
| Render | `SUPABASE_ANON_KEY` | recommended: the project's **public** anon key; with it, email checking is strict |
| Render | `INVITES_BY_ID` = `false` | recommended (invites only by their one-time link) |
| Render | `TRUSTED_PROXY_HOPS` = `1` | recommended (also defaults to 1 on Render) |
| Render | `HELIOS_ENV` = `production` | you asked for it; **release 1 ignores it** (see QUESTIONS); harmless, used by later releases |
| Render | `EMAIL_CONFIRMATION`, `RATE_LIMITS`, `RATE_LIMIT_DISABLED`, `STREAMS_PER_KEY` | leave **unset** |
| Vercel | `VITE_API_BASE_URL`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, `VITE_HELIOS_SDK_KEY` | required, already set; **no new ones** |

**Good:** every "required" name is present. **Wrong:** one is missing, or `RATE_LIMIT_DISABLED` / `EMAIL_CONFIRMATION=off` is set -> fix or ask before going on.

## Step 6. Rehearse the migrations on the restored copy
**Do:** run exactly the three migrations, in order, on the Step 3 copy. **Where:** your terminal, local only.
**Type:**
```
cd ~/helios-release1 && P="-h 127.0.0.1 -p 55432 -U helios -d helios_rehearsal"
psql $P -v ON_ERROR_STOP=1 -f backend/db/migrations/0004_workspaces.sql && psql $P -f backend/db/verify_0004.sql
psql $P -v ON_ERROR_STOP=1 -f backend/db/migrations/0006_workspace_roles_invites.sql && psql $P -f backend/db/verify_0006.sql
psql $P -v ON_ERROR_STOP=1 -f backend/db/migrations/0007_workspace_keeps_an_owner.sql && psql $P -f backend/db/verify_0007.sql
```
Then repeat the Step 1 query on the copy and `diff` it against `~/helios-release1-counts-before.txt`; also write down how long each file took.
**Good:** every verify row says `PASS` and ends `SUMMARY: N of N checks passed`; the count lines are identical.
**Wrong:** a migration prints `ERROR` or `RAISE`, or any `FAIL` row -> stop and ask. One known error: 0004 says `user_environment_roles has rows but no user is admin in every environment` -> production has no single admin of all environments; stop, do not edit data yourself. (Do **not** run 0005 or its verify: `verify_0005` is expected to fail.)

## Step 7. Announce, then the window: migrate production (G4)
**Do:** post the "before" message (below), then apply the three files **one at a time, in this order**, each in its own run (never `psql -1`).
**Where:** your terminal. **Type**, one line at a time, reading the result before the next:
```
cd ~/helios-release1
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0004_workspaces.sql   && psql "service=helios_prod" -f backend/db/verify_0004.sql
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0006_workspace_roles_invites.sql && psql "service=helios_prod" -f backend/db/verify_0006.sql
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0007_workspace_keeps_an_owner.sql && psql "service=helios_prod" -f backend/db/verify_0007.sql
```
Then run the Step 1 query again into `~/helios-release1-counts-after.txt` and `diff` it with the "before" file.
**Good:** each migration ends `COMMIT` with no `ERROR`; every verify row is `PASS`; counts are identical to Step 1. The old site keeps working (rehearsed: old backend runs fine on 0004+0006+0007).
**Wrong:** any `ERROR`, `FAIL`, or a count that differs -> **stop; do not run the next file**; ask. Each file is one transaction, so a failing file changes nothing, but a count difference after a "successful" run means ask before anything else.

## Step 8. Deploy backend and console together (G5)
**Do:** merge the release into `main` on GitHub, then deploy Render by hand at once.
**Where:** GitHub: open a pull request `feature/workspaces-ui` -> `main`, check it shows the 25 commits, merge it **in GitHub** (never `git push` to main: your local hook blocks it on purpose). Vercel starts its own build from the merge. Render dashboard -> Manual Deploy -> **Deploy latest commit**.
**Good:** both deploys finish "Live"; `curl -i https://<API URL>/healthz` returns `200` and `ok`; the console loads.
**Wrong:** a failed build, or `/healthz` not 200 after 5 minutes -> go to Rollback, Step 8 row.

## Step 9. Click through the live site (smoke list)
1. Open `https://<CONSOLE URL>` in a private window and **sign in as an existing user**: login works; their old flags, environments (dev, staging, production), experiments and audit log are all still there (compare a flag you know).
2. As that user: open the workspace menu: one workspace (the old data lives in it); you are Owner.
3. Create a flag `smoke-test-1`; toggle it on in dev; delete it.
4. **Sign up a brand-new account** (a fresh email). It lands in an empty private workspace with dev, staging, production, owned by the new user.
5. As the new user, confirm none of the old user's flags show. (Old user's data must be invisible to them.)
6. From your terminal, use an **existing SDK key**: `curl -s -X POST https://<API URL>/evaluate -H "Content-Type: application/json" -H "X-Helios-SDK-Key: <EXISTING SDK KEY>" -d '{"context":{"subjectKey":"smoke"},"flagKeys":["<EXISTING FLAG KEY>"]}'`. **Good:** JSON with an `evaluations` list and the same value as before. **Wrong:** 401/500 -> Rollback.
7. Delete the test account and `smoke-test-1` if left (Step 9 is also the point to remove test data).
**Wrong anywhere** -> Rollback table, row "Step 8".

## Step 10. The first hour
Stay for **about 30-60 minutes** (ROLLOUT: ~30 minutes clean, then you may relax). Every 10 minutes: Render -> Logs: look for `500`, `panic`, `error`; `curl -i https://<API URL>/healthz` is 200; do one SDK `evaluate` (Step 9.6); open the console. Watch for: a flood of 500s, users reporting they "see no data" or "cannot log in", SDK 401s. **Any of these -> Rollback.** Calm for an hour -> post the "after" message. **Do not apply 0005 now** (it is a separate, later decision after a clean observation period).

## Rollback: pick the row for how far you got
| You are at | Undo with | Data restore needed? |
|---|---|---|
| Steps 0-6 (nothing live changed) | nothing to undo; drop the local copy: `dropdb -h 127.0.0.1 -p 55432 -U helios helios_rehearsal` | no |
| Step 7, a file failed | that file made no change (one transaction each). Stop, ask | no |
| Step 7 done, new code **not** deployed | nothing: the old backend runs on the migrated database (rehearsed). Just leave it, or ask before undoing the migrations | no (a restore is only for putting the schema itself back) |
| Step 8, deployed, **no new signups yet** | Render: redeploy the previous deploy (Events/Deploys -> previous -> Rollback/Redeploy); Vercel: promote the previous production deployment; revert the new variables if you like (inert for old code). The old code works on the new schema | no |
| Step 8, new users have signed up and **made their own workspaces** | **Point of no return for a simple redeploy.** The old code can run, but the commented rollback block in `0004` is destructive once any other workspace exists. Stop and ask: the clean way back is the Step 2 dump, which loses everything written since | **yes: restore from the dump** (people who signed up since would be lost) |
| Anything after 0005 | not in this release; if you ever applied 0005 by mistake: stop, ask immediately | yes |

Whenever you roll back: write down what was created since the dump (new users, workspaces, flags) first. Never restore over production without asking (see QUESTIONS 5).

## Timing, what users see, messages
- **Expected total:** about **2.5-3.5 hours** end to end: Steps 1-3 about 30 min, Step 5 about 15, Step 6 about 20, Step 7 about 10-15 (the files only touch small tables; real timings come from Step 6), Step 8 about 10-20, Step 9 about 15, Step 10 about 60. Estimates, not measured on production.
- **During Step 7 users see:** normally nothing; writes (saving a flag) may pause for a moment while a file runs. **During Step 8:** the console may reload; the API may be briefly unavailable while Render swaps instances (SDKs keep their last values).
- **Before** (post 24 h ahead): "Helios maintenance on <DATE> at <TIME> <TIMEZONE>, about 30 minutes. The console and API may be briefly slow or unavailable. SDKs keep serving their last known flag values. No action needed."
- **After:** "Helios maintenance is complete. New: every account now has its own private workspace; existing flags, SDK keys and experiments are unchanged. Tell us if anything looks missing."

## Do not do this
- Do not `git push` to `main` or force-push anything; merge in GitHub. Do not run commands from a different branch's folder (use `~/helios-release1`).
- **Do not apply 0005 now**, and do not run 0008 or any later migration (0008-0015 belong to later releases and the new code needs them only then).
- Do not run a migration with `psql -1`, a pasted URL containing a password, or against `helios_prod` from a script you have not read; do not run Step 6 commands on `helios_prod`.
- Do not deploy the console and backend on different days; do not skip Steps 2-3 (a dump you never restored is not a backup).
- Do not edit production rows by hand to make a migration pass; do not delete the dump after the release.

## QUESTIONS (unclear or contradicting the code; please answer before the live day)
1. **`HELIOS_ENV=production`** does not exist in release 1's code (it was added later, on `feature/ops-readiness`), so setting it now does nothing. Keep it in the list anyway?
2. **Maintenance banner:** ROLLOUT.md plans a "banner on/off", but the console has no maintenance banner (the only banner is the invite one). Do you post only the messages above?
3. **Counts file:** ROLLOUT G1 says per-table counts and max ids are written next to the dump, but `backup_db.sh` only writes the dump. I made the counts a manual query (Step 1); say if you want max ids too.
4. **Restore needs `auth.users`:** the repo says so but gives no recipe (`rehearsal.md` assumes a second Supabase project). The three-part restore in Step 3 is mine and was not run against your real dump.
5. **Restoring over production** is not documented anywhere (`backup_db.sh` says restore into an empty local database only). I gave no command on purpose.
6. **`rehearsal.md` step 5 says apply 0005 "immediately" after the deploy**, contradicting ROLLOUT.md (0005 is a later, separate gate). I followed ROLLOUT.md.
7. **ROLLOUT G2** asks for three heavier rehearsals (old and new backend binaries, `/sdk/stream` on Redis); `rehearse_migration.sh` needs those binaries built. This runbook uses the simpler migration-only rehearsal. Do you want the full one?
8. **Render and Vercel menu names** and the Render rollback button are written from memory of the dashboards, not from the repo; check them on the day. Whether Vercel can hold its production deploy until Render is ready is unknown (it builds on merge).
9. **Server version:** unknown from the repo; `pg_dump` must be at least the production major version.
