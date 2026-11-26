# Release 1 runbook: multi-tenancy (by hand, one step at a time)

For the owner doing the live release. A plain checklist made from `docs/ROLLOUT.md`, **executed end to end on a local copy on
2026-10-21** (a synthetic "old production", see the last section) and corrected where reality differed. Times marked *measured* were
measured on this laptop with 250,000 audit rows and 250,000 event rows: production will differ, Step 6 gives you your own numbers.
**Release 1 only:** branch `release/1`: the code of `feature/workspaces-ui` (`1e2f5e6`, on top of `main` = `6c90844`) plus documents, scripts and CI; **no application code or migration differs from `1e2f5e6`**. Migrations **0004, 0006, 0007**.
Migration **0005 is NOT part of this** (it removes defaults the old backend needs; a later, separate gate, G6b in `docs/ROLLOUT.md`). Everything else belongs to later releases.

How to read a step: **Do** / **Type** / **Where** / **Good** / **Wrong -> then**. `<ANGLE BRACKETS>` are placeholders you fill in.
Never paste a password, key or URL with a password into chat, a file or a commit. If a result is not exactly "Good": **stop and ask**.
The commands work in macOS's default shell (zsh) and in bash.

---

## Step 0. Get the release code into its own folder
**Do:** make a second checkout of the release branch so every command below runs the release's own scripts.
**Type:** `cd <YOUR REPO FOLDER> && git worktree add --detach ~/helios-release1 release/1 && cd ~/helios-release1 && git log --oneline -1`
then: `git diff --stat 1e2f5e6 -- backend/internal backend/cmd frontend/src backend/db/migrations && python3 tools/release1-docs-check/check.py`
**Good:** the log line is the commit id you were given for `release/1` (write it down); `git diff` prints **nothing** (the code and migrations are exactly the rehearsed ones); the check ends with `OK`.
**Wrong:** a different id, any line from `git diff`, a failed check, or "already exists" -> stop and ask.
All commands below that mention `~/helios-release1` run from that folder.

## Step 1. Read-only sanity check of production (G1, part 1)
**Do:** prove the connection works, write down how much data there is, and see who and what the release will change.
**Type:** `psql "service=helios_prod" -At -c "select 'environments',count(*) from environments union all select 'flags',count(*) from flags union all select 'flag_configs',count(*) from flag_configs union all select 'api_keys',count(*) from api_keys union all select 'experiments',count(*) from experiments union all select 'experiment_metrics',count(*) from experiment_metrics union all select 'events',count(*) from events union all select 'audit_logs',count(*) from audit_logs union all select 'user_environment_roles',count(*) from user_environment_roles order by 1" | tee ~/helios-release1-counts-before.txt`
**Where:** your terminal (`helios_prod` is your `~/.pg_service.conf` entry; the password comes from `~/.pgpass`). It only reads. *(Measured: 0.04 s.)*
**Good:** nine `name|number` lines, in alphabetical order (the `order by 1` matters: without it the two files you compare later can list the same numbers in a different order).
Now read three **limits** the new backend enforces on every workspace, including the one your old data moves into: **at most 50 flags**, **3 environments when a workspace is created**, **20 members plus open invites**, **10 active SDK keys**. Existing data is never deleted for being over a limit, but you then cannot *add* to it. If `flags` is 50 or more, creating a new flag will be refused (Step 9 expects that); decide if that is acceptable.
Then count users whose role breadth changes when roles are flattened (see "Decide before" below); report counts only, never email addresses:
`psql "service=helios_prod" -At -c "select count(*) from (select r.user_id from user_environment_roles r join environments e on e.id=r.environment_id group by r.user_id having count(distinct r.role) > 1 or count(*) < (select count(*) from environments)) affected"`
**Wrong:** "service not found", "password authentication failed", or an error naming a missing table -> stop and ask.

## Step 2. Take the production dump (G1, part 2)
**Do:** save a full copy of the data (the `public` schema, compressed) outside the repository. Do not use `backend/scripts/backup_db.sh` here: it writes inside the checkout, even though that directory is git-ignored.
**Type:** run this from `~/helios-release1`, replacing `<UTC_TIMESTAMP>` with a unique value such as `20261126T134700Z`:
```
python3 -c 'import os, pathlib, subprocess; d=pathlib.Path.home()/"helios-release1-backups"; d.mkdir(mode=0o700,exist_ok=True); d.chmod(0o700); p=d/"helios-<UTC_TIMESTAMP>.dump"; assert not p.exists(), f"refusing to overwrite {p}"; e={k:v for k,v in os.environ.items() if k not in {"DATABASE_URL","PGHOST","PGPORT","PGUSER","PGPASSWORD","PGDATABASE"}}; e["PGCONNECT_TIMEOUT"]="15"; os.umask(0o077); subprocess.run(["pg_dump","--dbname","service=helios_prod","--schema=public","--format=custom","--no-owner","--no-privileges","--file",str(p)],env=e,check=True); p.chmod(0o600); print(f"wrote {p} ({p.stat().st_size} bytes)")'
```
**Good:** `pg_dump` succeeds and the dump is private (mode 600) under `~/helios-release1-backups/`, not in or under the repository. Record the exact path and size locally; never share it because it holds your users' data. *(Measured: 1.2 s, 4.4 MB.)*
**Wrong:** "pg_dump not found", a "server version mismatch", or a tiny file -> stop and ask (the dump tool must be the same major version as the server or newer; check with `pg_dump --version` and `psql "service=helios_prod" -Atc "show server_version"`).

## Step 3. Prove the dump can be restored (on your own computer only)
**Do:** restore it into a throwaway local database and compare the counts with Step 1. The dump has no `auth.users` (Supabase owns that table), so the restore runs in three parts with a stand-in table filled with the user ids the data refers to.
**Where:** your terminal, local only. Start the local database server with `~/helios-release1/backend/scripts/local_pg.sh ensure`. `<DUMP>` = the file from Step 2. **Type** one line at a time:
```
LOCAL="postgres://helios@127.0.0.1:55432/helios_rehearsal"; ADMIN="postgres://helios@127.0.0.1:55432/postgres"
psql "$ADMIN" -c "DROP DATABASE IF EXISTS helios_rehearsal WITH (FORCE)" -c "CREATE DATABASE helios_rehearsal"
psql "$LOCAL" -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA auth" -c "CREATE TABLE auth.users (id uuid primary key default gen_random_uuid(), email text unique)"
pg_restore --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=pre-data <DUMP>
pg_restore --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=data <DUMP>
psql "$LOCAL" -c "INSERT INTO auth.users (id, email) SELECT u, u||'@rehearsal.invalid' FROM (SELECT created_by u FROM flags UNION SELECT created_by FROM api_keys UNION SELECT created_by FROM experiments UNION SELECT user_id FROM user_environment_roles) x WHERE u IS NOT NULL ON CONFLICT DO NOTHING"
pg_restore --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=post-data <DUMP>
```
Then the same Step 1 count query with `psql "$LOCAL" -At -c "..."` into `~/helios-release1-counts-restored.txt` and `diff ~/helios-release1-counts-before.txt ~/helios-release1-counts-restored.txt && echo SAME`. *(Measured: 1.2 s in total.)*
**Good:** the last command prints `SAME`. **Wrong:** any `pg_restore` error or a difference -> stop; the dump is not proven. (`DROP SCHEMA public` is needed because the dump creates it; the first recipe in this file did not have that line and failed on "schema public already exists".)
Notes: only users that appear in the data are recreated, with placeholder emails; people who signed up but never held a role are not in the copy (they exist in Supabase, so production is unaffected). The legacy workspace is named after its owner's email *at migration time*, so on the copy it will show a placeholder; that does not matter for the migrations.

## Step 4. Turn off automatic deploys, then prepare the branch for review (G3)
**Do:** make sure nothing deploys by itself, then verify the existing GitHub branch and prepare the draft pull request. Nothing here changes production.
**Where:** Render dashboard -> your backend service -> Settings -> Build & Deploy -> Auto-Deploy = **Off**. Vercel -> project -> Settings -> Git: production deploys come only from `main` (menu names may differ).
**Then:** do not push `release/1` again; it is already pushed. Open a **draft** pull request `release/1` -> `main` on GitHub if one is not already open. Do not merge it yet.
**Good:** Auto-Deploy shows off; the pull request's `CI` checks are all green. CI has already passed on GitHub for `release/1` at `a0f105f`; confirm the checks on the draft pull request as well.
**Wrong:** you cannot find the setting, or a check is red -> stop and ask.

## Step 5. Set the environment variables (names only; you type the values)
**Do:** add the settings the release code reads. **Where:** Render -> Environment (backend); Vercel -> Settings -> Environment Variables (console). Save; on Render choose **not** to redeploy yet.

| Where | Name | Needed? |
|---|---|---|
| Render | `DATABASE_URL`, `SUPABASE_URL`, `CORS_ALLOWED_ORIGINS` | **required**, already set: check they exist |
| Render | `REDIS_URL` | already set (optional) |
| Render | `SUPABASE_ANON_KEY` | recommended: the project's **public** anon key; with it, email checking is strict (the rehearsal ran in this mode) |
| Render | `INVITES_BY_ID` = `false`, `TRUSTED_PROXY_HOPS` = `1` | recommended |
| Render | `HELIOS_ENV` = `production` | you asked for it; **release 1's code ignores it** (checked: no log line, no effect). Harmless |
| Render | `EMAIL_CONFIRMATION`, `RATE_LIMITS`, `RATE_LIMIT_DISABLED`, `STREAMS_PER_KEY` | leave **unset** |
| Vercel | `VITE_API_BASE_URL`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, `VITE_HELIOS_SDK_KEY` | required, already set; **no new ones** |

**Wrong:** a required name is missing, or `RATE_LIMIT_DISABLED` / `EMAIL_CONFIRMATION=off` is set -> fix or ask.

## Step 6. Rehearse the migrations on the restored copy
**Do:** run exactly the three migrations, in order, on the Step 3 copy. **Where:** your terminal, local only.
```
cd ~/helios-release1 && LOCAL="postgres://helios@127.0.0.1:55432/helios_rehearsal"
psql "$LOCAL" -v ON_ERROR_STOP=1 -f backend/db/migrations/0004_workspaces.sql && psql "$LOCAL" -f backend/db/verify_0004.sql
psql "$LOCAL" -v ON_ERROR_STOP=1 -f backend/db/migrations/0006_workspace_roles_invites.sql && psql "$LOCAL" -f backend/db/verify_0006.sql
psql "$LOCAL" -v ON_ERROR_STOP=1 -f backend/db/migrations/0007_workspace_keeps_an_owner.sql && psql "$LOCAL" -f backend/db/verify_0007.sql
```
(The same rehearsal as one script, with old-and-new backend checks: `backend/scripts/rehearse_migration.sh`, usage in its header.)
Put `time` in front of each `psql ... -f ...migrations/...` to get your own numbers. Then repeat the Step 1 count query on the copy and `diff` it against the "before" file.
*(Measured on 250k audit + 250k event rows: 0004 = 0.22 s, 0006 = 0.03 s, 0007 = 0.02 s; verify scripts: 39 of 39, 16 of 16, 7 of 7 PASS; counts identical; every environment, key, flag, flag config, experiment and role id and content identical.)*
**Good:** every verify row says `PASS` and ends `SUMMARY: N of N checks passed`; the counts are identical. **Wrong:** `ERROR`, `RAISE`, any `FAIL` row, or a count difference -> stop and ask. Known error: 0004 says `no user is admin in every environment` -> nobody holds the admin role in all environments; do not edit data yourself. Do **not** run 0005 or `verify_0005` (it is expected to fail).
**What the migrations do to people** *(measured)*: one workspace is created, owned by the person who is admin in every environment, and holds all existing environments, flags, experiments, keys and audit rows (none left without a workspace). Everyone who held any role becomes a member of it, with their **highest** old role: admin or approver -> `admin`, editor -> `editor`, otherwise `viewer`. People who had no role are in no workspace; each gets their own private one at first sign-in. No invites exist afterwards (the old system had none).
**Optional but recommended:** run the app against the copy and the click-through below as automated tests: `tools/release1-dryrun/README.md`.

## Step 7. Announce, then the window: migrate production (G4)
**Do:** post the "before" message (below), then apply the three files **one at a time, in this order**, each in its own run (never `psql -1`).
```
cd ~/helios-release1
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0004_workspaces.sql   && psql "service=helios_prod" -f backend/db/verify_0004.sql
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0006_workspace_roles_invites.sql && psql "service=helios_prod" -f backend/db/verify_0006.sql
psql "service=helios_prod" -v ON_ERROR_STOP=1 -f backend/db/migrations/0007_workspace_keeps_an_owner.sql && psql "service=helios_prod" -f backend/db/verify_0007.sql
```
Then the Step 1 count query again into `~/helios-release1-counts-after.txt` and `diff` it with the "before" file.
**What it locks** *(measured, held until each file commits)*: 0004 takes an exclusive lock on `audit_logs`, `environments`, `experiments`, `flag_configs`, `flags` and `user_environment_roles` for the whole file (**reads and writes of those tables wait**; 0.22 s here, and the old backend kept answering: 3,000 requests during the three files, 0 failures, the slowest 181 ms). 0006 locks `api_keys`, `workspaces`, `workspace_members`, `workspace_invites`. 0007 only `workspace_members`. The `audit_logs` index build in 0004 grows with that table (a few seconds for millions of rows is plausible, not measured).
**Good:** each ends `COMMIT`, every verify row `PASS`, counts identical (only `audit_logs` may be **higher**, if someone edited a flag meanwhile; anything lower, or any other difference, is wrong). The old site keeps working, with one exception from 0006 on: **adding a member answers 500** until Step 8. **Wrong:** any `ERROR`/`FAIL`/count difference -> **stop; do not run the next file**; ask. A failing file changes nothing (one transaction each).

## Step 8. Deploy backend and console together (G5)
**Do:** merge the release into `main` on GitHub, then deploy Render by hand at once.
**First:** run the Step 9.4 `curl` once against the old backend and keep the answer to compare afterwards.
**Where:** GitHub: the Step 4 pull request. Mark it ready; its commit count must equal what `git rev-list --count 6c90844..release/1` prints; merge **in GitHub** (never `git push` to main: your local hook blocks it on purpose). Vercel starts its build on the merge. Render -> Manual Deploy -> **Deploy latest commit**.
**Good:** both "Live"; `curl -i https://<API URL>/healthz` returns `200` and `ok`; the console loads. **Wrong:** failed build, or no 200 after 5 minutes -> Rollback, "Step 8".
**The window is the gap between the two going live** *(measured by running every pairing)*: **old console + new backend:** existing users sign in and see *"This account has no environment access yet. Ask an admin to grant you a role"*; the old console has no sign-up at all. **New console + old backend:** existing and new users see *"This workspace has no environments you can see yet."* and new accounts get no workspace. Either way nothing is lost and SDK `/evaluate` keeps working throughout. So: click Render's deploy the moment you merge, and expect a few minutes of that message; do not keep the two apart. Open `/sdk/stream` connections do **not** receive flag changes made through the other side while old and new instances overlap (the channel name changed): SDKs pick the change up when they reconnect or poll.

## Step 9. Click through the live site (smoke list; automated as `tools/release1-dryrun/e2e/runbook-clickthrough.spec.ts`)
**Do checks 1-6 first, in order. Check 7 creates a second workspace, and from then on redeploying the old backend is only a degraded rollback** (see Rollback; a real user signing up does the same).
1. In a private window sign in as an **existing user** (this is `/me`): the old flags, the environment tabs and the audit log are there; Settings -> Members lists everyone who held a role.
2. The workspace menu shows one workspace and you are Owner.
3. Create a flag `smoke-test-1` and switch it on and off. **If you had 50 or more flags, creation is refused with "limit reached: at most 50 flags per workspace"** (expected, see Step 1); switch an existing flag on and off instead.
4. With an **existing SDK key**: `curl -s -X POST https://<API URL>/evaluate -H "Content-Type: application/json" -H "X-Helios-SDK-Key: <EXISTING SDK KEY>" -d '{"context":{"subjectKey":"smoke"},"flagKeys":["<EXISTING FLAG KEY>"]}'` -> JSON with an `evaluations` list and the same values as before; a **revoked** key still gives 401.
5. **Live updates**, same old key: `curl -N -i --max-time 60 -H "X-Helios-SDK-Key: <EXISTING SDK KEY>" https://<API URL>/sdk/stream`, and while it waits, switch a flag of that key's environment in the console (then back). Good: status `200`, `text/event-stream`, then `event: flag_update`; curl stopping after 60 s is normal. `503 STREAM_UNAVAILABLE` means the backend has no Redis (check `REDIS_URL`, and the log line `connected to Redis`).
6. Open Settings -> Members and check what a former dev-only viewer or admin can now do (see "Decide before").
7. **Sign up a brand-new account** (a fresh email, confirm it): it lands in an empty private workspace (dev, staging, production) with the onboarding card.
8. As the new user confirm none of the old flags show, and that the old workspace's data is not reachable.
9. **Unconfirmed email is refused:** sign up another fresh address, do **not** click the confirmation link, and try to get in. Good: Supabase refuses the sign-in, or the console shows "Please confirm your email first"; no workspace appears. **If you land in a workspace**, Supabase's "Confirm email" is off and Helios cannot detect that: stop and ask (and keep `INVITES_BY_ID=false`).
10. Clean up: delete `smoke-test-1` in the console and the test users in Supabase -> Authentication -> Users. **Release 1 cannot delete a workspace**, so the test account's empty workspace stays; leave it (do not delete rows by hand).
**Wrong anywhere** -> Rollback, the "Step 8" row that fits.

## Step 10. The first hour
Stay about 30-60 minutes. Every 10 minutes: Render -> Logs: look for `500`, `panic`, `error`; `curl -i https://<API URL>/healthz` is 200; one SDK `evaluate` (Step 9.6); open the console. Trouble signs: a flood of 500s, "cannot log in" or "see no data" reports, SDK 401s -> Rollback. Calm for an hour -> post the "after" message. **Do not apply 0005 now.**

## Rollback: pick the row for how far you got *(each "old backend" result was measured by running the old backend on a migrated copy)*
Which Step 8 row applies: `psql "service=helios_prod" -Atc "select count(*) from workspaces"`. A second workspace appears with a new sign-up, with the first sign-in of a user who held no role, or when anyone creates a workspace.

| You are at | Undo with | Data restore needed? |
|---|---|---|
| Steps 0-6 | nothing live changed; drop the local copy (and close the draft pull request) | no |
| Step 7, a file failed | that file made no change (one transaction each). Stop, ask | no |
| Step 7 done, new code **not** deployed | leave it. The old backend works on the migrated database: SDK evaluation, logins, reads, flag create/toggle/kill/delete, experiments, audit log. **One thing breaks: adding a member (500)**, because members must now be workspace members first | no |
| Step 8 deployed, **still 1 workspace** | redeploy the previous Render deploy and Vercel deployment (revert the new variables if you like). Same result as the row above: adding a member still fails | no |
| Step 8 deployed, **2 or more workspaces** | **not a clean rollback.** SDK evaluation keeps working and, at best, so do the old users' reads, edits of existing flags and experiments. **Creating a flag fails (500), deleting one is refused (403)**, adding a member fails (500), and accounts created since see nothing (no roles, and the old console has no sign-up). **Worse, the old console can lose an environment altogether (403 on everything in it, for everyone):** the old backend looks an environment up by its key (`dev`) without saying which workspace, and which row it gets is not guaranteed (reproduced by rewriting one row). No other workspace's flags were shown or changed in the checks made. **Do not count on this rollback;** fixing forward may be better. Deleting the extra users does not remove their workspaces, and undoing the migrations is destructive once another workspace exists. The only way back to a surely working old system is the Step 2 dump | **yes, to be fully back: restore from the dump; it loses everything written since** (accounts, workspaces, flag edits) |
| After 0005 (not this release) | **every old-backend write fails (500)**, even toggling a flag; its sign-in, reads and SDK evaluation still work. Do not redeploy the old backend like this; stop and ask. The commented ROLLBACK block at the bottom of `0005` puts the defaults back, which returns you to the row above that fits (tested on a local copy) | no for that block |

Whenever you roll back: first write down what was created since the dump. Never restore over production without asking (QUESTIONS 1).

## Timing, what users see, messages
- **Expected total: about 2 to 3 hours** end to end. Measured pieces on this laptop: dump 1.2 s, restore 1.2 s, the three migrations 0.3 s, verify under a second each. The rest is you: Steps 1-3 about 20 min, Step 5 about 15, Step 6 about 15, Step 7 about 10, Step 8 about 10-20 (Render's build), Step 9 about 15, Step 10 about 60. Production data may be larger or smaller than the rehearsal's.
- **Users see:** during Step 7 nothing, or a writes pause under a second; during Step 8 the "no environment access" / "no environments" message until both sides are live (a few minutes); SDKs see nothing.
- **Before** (24 h ahead): "Helios maintenance on <DATE> at <TIME> <TIMEZONE>, about 30 minutes. The console may show a short error and flag edits may pause. SDKs keep serving their last flag values. No action needed."
- **After:** "Maintenance is complete. New: every account now has its own private workspace; your existing flags, SDK keys and experiments are unchanged."

## Decide before (found by the rehearsal; not in ROLLOUT.md)
**Roles are flattened.** The old system gave roles per environment; the new one gives each person ONE role for the whole workspace, from their highest old role. Measured: a person who was viewer in dev only can now read production flags (HTTP 200); one who was admin in dev only can now switch a production flag (200) and is an `admin` everywhere; an old production-only viewer is now a viewer in dev too (the same rule; not separately tested). Run the Step 1 "gains access" query and decide whether to change roles (or accept) **before** the window; afterwards, in Settings -> Members.

**Unconfirmed addresses.** With `SUPABASE_ANON_KEY` set, an **existing** user whose address Supabase does not report as confirmed gets the "confirm your email" screen instead of the console. Look at Supabase -> Authentication -> Users before the window. (Read from the code; the rehearsal had no such existing user.)

## Do not do this
- Do not `git push` to `main` or force-push; merge in GitHub. Do not run commands from another branch's folder (use `~/helios-release1`).
- **Do not apply 0005 now.** The comments at the top of the `0004` and `0005` files say to apply it "IMMEDIATELY"; that is superseded by this runbook (the files are left byte-for-byte as rehearsed). There is no migration after 0007 in this release; later ones belong to later releases.
- Do not use `psql -1`, a URL with a password, or run Step 6 commands against `helios_prod`.
- Do not deploy the console and backend on different days; do not skip Steps 2-3.
- Do not edit production rows by hand to make a migration pass; do not delete the dump after the release.

## QUESTIONS (need your decision; the rest were answered by running the code)
1. **Restoring over production** is documented nowhere (`backup_db.sh` says restore into an empty local database only). I gave no command on purpose. Do you want a procedure written and rehearsed?
2. **Flattened roles** (above): change them first, or accept as is?
3. **The 50-flag limit** (and 20 members): is production under them? If not, accept, or have the limits in `quota.go` raised (a code change: it needs a new rehearsal)?
4. **Maintenance banner:** **none exists** in the console and this release does not add one; the messages below are the only announcement. Is that enough?
5. **`HELIOS_ENV=production`** does nothing in release 1; keep it in the variable list?
6. **Dashboard menu names** and the Render rollback button are from memory; check them on the day. Can Vercel hold its production build until Render is ready? Unknown.
7. **Server version:** `pg_dump` must be at least production's major version; Step 2 shows both.
8. **CI** runs for the first time when you push `release/1` (Step 4). Do you want its actions pinned to commit SHAs first (a TODO in `.github/workflows/ci.yml`)?

---
## What the rehearsal used
**A synthetic "old production", not your dump** (no dump has been available): the schema of `main` (6c90844), 10 users (7 with roles of different breadth, 3 with none), 5 environments, 60 flags, 300 flag configs, 6 experiments, 3 SDK keys minted by the old code (one revoked), 250,000 audit rows, 250,000 events. First run 2026-10-21; repeated on 2026-11-25 following this file from branch `release/1`: dump 0.7 s, restore 1.2 to 2.6 s, migrations 0.14 to 0.19 s (0004) and 0.02 s each (0006, 0007), verify 39/39, 16/16, 7/7, counts identical, 3,585 requests to the old backend during the migrations with none failing (slowest 147 ms). Steps 4, 5 and 8 (GitHub, dashboards, deploys) were not and cannot be rehearsed here. Scripts, the automated click-through and the evidence: `tools/release1-dryrun/`.
