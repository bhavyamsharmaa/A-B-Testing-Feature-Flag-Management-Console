# Production rollout: two releases (DRAFT)

> **Status: draft written during gate G0.** The rollout plan this was meant to
> mirror did not exist in the repo, so this is reconstructed from the gate list
> given for the rollout. Review it before G1. Nothing here has run against production.

Goal: existing users and SDK integrations see no breakage, and every step can be undone.

**Two releases, in this order, never together:**

1. **Release 1 - multi-tenancy only** (`feature/workspaces-ui`). Everything from "What
   ships" down in this document (gates G0-G7) is release 1.
2. **Release 2 - the experiments stack** (`feature/experiments-tracking`,
   `-results`, `-results-ui`, `-hardening`), only after release 1 is finished,
   including 0005 at G6b and a clean observation period. Section "Release 2" below.

Nothing in release 1 reads or writes anything release 2 adds, and release 2 needs
release 1's schema (it uses `workspace_id`). That is what makes the split safe.

## Stack status

As of this writing (before the commit that added this section). Nothing was pushed
by the work that produced the experiments stack; only `feature/workspaces-ui` has
ever been pushed (at your request, early). Remote-tracking refs seen locally:
`origin/main` = `6c90844`, `origin/feature/workspaces-ui` = `1e2f5e6`. Nothing was
fetched to produce this table.

| Branch | Head | Pushed? | Based on |
|---|---|---|---|
| `main` (local) | `7381cc6` | no: 2 commits ahead of `origin/main` (`6c90844`) - but both are already on origin inside `feature/workspaces-ui` | `origin/main` |
| `feature/workspaces-ui` (release 1) | `1e2f5e6` | yes, equals `origin/feature/workspaces-ui` | `main` |
| `feature/experiments-tracking` | `865eb82` | no | `feature/workspaces-ui` |
| `feature/experiments-results` | `b5aea5c` | no | `feature/experiments-tracking` |
| `feature/experiments-results-ui` | `96438f5` | no | `feature/experiments-results` |
| `feature/experiments-hardening` | `36c4620` (plus the commit adding this section) | no | `feature/experiments-results-ui` |

The history is strictly linear (each branch contains the one before it; checked with
`git merge-base --is-ancestor`). A bundle of all local branches is kept in
`~/helios-backups/` (outside the repository).

## Migrations 0004-0009

All files are in `backend/db/migrations/`; verify scripts in `backend/db/`. Every
migration runs in its own transaction (no `psql -1`). Lock notes come from each file's
header; none has been timed on the production data size - the G2 rehearsal on the
restored copy gives the real numbers.

| # | Release | Apply order | What it does | Lock impact | Rollback | Verify |
|---|---|---|---|---|---|---|
| 0004 workspaces | 1 | 1st, in the window | workspaces, members, `workspace_id` on every tenant table, composite FKs, legacy workspace | `ADD COLUMN ... NOT NULL DEFAULT <constant>` is catalog-only (no rewrite, no `audit_logs` UPDATE); constraint/FK additions take short exclusive locks and scan the (small) tables | commented block in the file; **destructive once any other workspace exists** - restore from the G1 backup instead | `verify_0004.sql` |
| 0006 roles, invites | 1 | 2nd, in the window | workspace roles, many workspaces per user, invites, slugs, tenant-scoped API keys | primary-key change on `workspace_members` and new columns/FK on `api_keys`: short exclusive locks on small tables | commented block in the file (undo before new roles are relied on) | `verify_0006.sql` |
| 0007 keep an owner | 1 | 3rd, in the window | deferred constraint trigger: a workspace with members keeps an owner | creating a trigger takes a brief lock on `workspace_members` | commented block (drop the trigger) | `verify_0007.sql` |
| 0005 drop defaults | 1 | **later, own gate G6b**, after the new backend has been live and observed | removes the transitional defaults | `DROP DEFAULT` is catalog-only; brief exclusive locks | commented block re-adds the defaults (needed to put old code back) | `verify_0005.sql` |
| 0008 event ingestion | 2 | after release 1 is complete (needs 0004/0005 columns) | `events` gets `workspace_id`, `event_id`, `properties`, `occurred_at`; composite FKs; exposure and event-id uniqueness; indexes | `events` has never been written before `/track`: empty table, lock held for milliseconds; **the file refuses to run if `events` has rows** | commented block: drops `events` (discards ingested data) | `verify_0008.sql` |
| 0009 exposure verification | 2 | after 0008 | nullable `events.exposure_verified`, a CHECK (added NOT VALID, then validated), a partial index | `ADD COLUMN` nullable without default is catalog-only; validation takes `SHARE UPDATE EXCLUSIVE` (writes continue); on a large `events` table build the index first with `CREATE INDEX CONCURRENTLY` | commented block: drop index, constraint, column (deploy a backend without exposure verification first) | `verify_0009.sql` |

`verify_0005.sql` is expected to FAIL until 0005 is applied.

## Environment variables per release

Names only for secrets. "Recommended" is for production on day one of that release.

**Release 1 (Render backend)** - unchanged and required: `DATABASE_URL` (secret), `SUPABASE_URL`, `REDIS_URL` (secret), `CORS_ALLOWED_ORIGINS`, `PORT`.

| Name | Release | New or changed | Recommended production value |
|---|---|---|---|
| `SUPABASE_ANON_KEY` | 1 | new; the project's public key | set it (email mode then defaults to `strict`) |
| `EMAIL_CONFIRMATION` | 1 | new | unset (or `strict`) |
| `INVITES_BY_ID` | 1 | new | `false` |
| `TRUSTED_PROXY_HOPS` | 1 | new | `1` (also defaults to 1 when `RENDER=true`) |
| `RATE_LIMITS`, `RATE_LIMIT_DISABLED`, `STREAMS_PER_KEY` | 1 | new, optional | unset |

**Release 2 (Render backend)**

| Name | New or changed | Recommended production value |
|---|---|---|
| `EXPOSURE_VERIFICATION` | new | **`off` on day one** (default); `report` once SDKs send the token; `required` after that. See "Day one" below |
| `EXPOSURE_TOKEN_KEY` | new, **secret**, `<id>:<secret>` with a secret of at least 32 bytes | set a generated value from day one, the same on every instance, so tokens are consistent when verification is turned on; secret store only |
| `EXPOSURE_TOKEN_KEY_PREVIOUS` | new, **secret**, rotation only | unset |
| `EXPOSURE_TOKEN_MAX_AGE` | new | unset (24h) |
| `RATE_LIMITS` | existing; new classes `sdk_track`, `sdk_track_events`, `results` | unset (defaults: 50/s burst 200 batches per key; 100/s burst 1,000 events per key; 1/s burst 20 results reads per user) |
| `HELIOS_EVENT_RETENTION_DAYS` | new, read only by the `purgeevents` command, not the API | `90` |

Vercel (console), release 2: no new variables. Supabase auth settings: not changed by either release.

## Release 1 - detail

(Gates G0-G7, smoke list and rollback below.)

## What ships

| Piece | Production today | After |
|---|---|---|
| Database | migrations 0001-0003 | + 0004, 0005, 0006, 0007 |
| Backend (Render) | `origin/main` = `6c90844` | `feature/workspaces-ui` |
| Console (Vercel) | same commit | same branch (deploy together with the backend) |

## Migration order (approved): split

0005 removes the column defaults the old backend's writes rely on: after 0005
and before the new backend is live, the old backend's writes fail with 500
(reads and SDK `/evaluate` keep working). So 0005 is **not** part of the window:

1. Window: apply **0004, 0006, 0007** only (each has its own transaction; no `psql -1`).
2. Deploy the new backend and console together. Verify. Observe.
3. Later, as its own gate (**G6b**), only on an explicit "go": apply **0005**.

Until 0005 is applied, rolling back is redeploying the old backend and console
(no restore). Rehearsed on a legacy-shaped database (keys minted by the old code):

| Database state | Old backend SDK evaluate / reads / writes |
|---|---|
| before | ok / ok / ok |
| 0004 | ok / ok / ok |
| 0004+0006 | ok / ok / ok |
| 0004+0006+0007 | ok / ok / ok |
| + 0005 | ok / ok / **500** |

The new backend was rehearsed on 0004+0006+0007 (no 0005): all checks pass.
Caveat: the old `mkkey` CLI fails after 0006 (`api_keys.workspace_id` NOT NULL); CLI only.

## Release 1 environment variables (detail)

Render (backend) - existing, unchanged: `DATABASE_URL`, `SUPABASE_URL`, `REDIS_URL`, `CORS_ALLOWED_ORIGINS`, `PORT`.

| Name | Value | Notes |
|---|---|---|
| `SUPABASE_ANON_KEY` | the project's **public** anon/publishable key | you set it. With it present, email mode defaults to `strict` |
| `EMAIL_CONFIRMATION` | leave unset (or `strict`) | `off` disables the check |
| `INVITES_BY_ID` | `false` | accept-by-id off; only the one-time link works. Use `true` only if Supabase "Confirm email" is guaranteed on |
| `TRUSTED_PROXY_HOPS` | `1` | defaults to 1 when `RENDER=true`; set explicitly anyway |
| `RATE_LIMITS`, `RATE_LIMIT_DISABLED`, `STREAMS_PER_KEY` | unset | optional; defaults are 50/s burst 500 per IP, 100 streams per key |

Vercel (console): no new variables. Existing: `VITE_API_BASE_URL`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, `VITE_HELIOS_SDK_KEY`. `frontend/vercel.json` now also sends `Referrer-Policy: no-referrer` and `X-Content-Type-Options: nosniff`.
Supabase auth settings are **not** changed by this rollout.

## Gates

Each gate starts only on "go <n>" and ends with a report. Never chain gates.

- **G0 Preflight** (read-only): tests, builds, e2e, secret scan, env list, SDK-key continuity. Done.
- **G1 Backup**: `pg_dump` to git-ignored `backend/backups/<timestamp>.dump`, per-table counts and max ids to `<timestamp>-counts.txt`, restore into a throwaway local DB and compare.
- **G2 Rehearsal on the restored copy**, with 0004, 0006, 0007 (not 0005); verify scripts; backend and console against it; existing-user login; two-user Playwright. Plus the three rollback-proof checks:
  1. *Old backend on 0004+0006+0007:* the OLD backend (`origin/main`) through its full console flows: login, `/me`, environment and member listing, role-gated actions (viewer/editor/admin attempts, allowed and refused), audit log, experiments (create/start/stop), SDK key create and revoke. Report any 500 or permission surprise, especially anything depending on the old per-environment roles after 0006 backfilled the new roles.
  2. *New backend without 0005:* the NEW backend plus the two-user Playwright scenario, and a list of every write path (each route that writes, with its result) showing it works without 0005.
  3. */sdk/stream with a pre-migration key:* on a real Redis, old and new API instances running at the same time (the channel name changed), flag updates published from each side; report whether the overlap drops any flag update and for how long.
- **G3 Prepare** (no production changes): merge plan (branch, commit range, draft PR to `main`), Render/Vercel changes prepared not applied, maintenance-banner plan, exact rollback commands, confirm previous deploys can be redeployed.
- **G4 Window + migrate**: banner on, new env vars set (your confirmation); apply **0004, 0006, 0007**; verify scripts (verify_0005 is expected to fail: 0005 not applied); compare counts with G1.
- **G5 Deploy** backend and console together (you merge); watch both deploys; smoke list below.
- **G6 Observe** about 30 minutes; banner off only after a clean report.
- **G6b Apply 0005** (own gate, only on "go"): after G6 is clean. Run verify_0005, repeat the smoke list, compare counts.
- **G7 Close out**: remove test accounts and data; summary (changes, final counts, downtime); keep the backup; update `docs/SECURITY_REVIEW.md` limitations with "applied to production on <date>".

Abort without fixing forward if: a row count differs from the snapshot, a migration errors, a smoke test fails, or error rates rise after deploy. Then propose rollback and wait for "go".

## Smoke list (dedicated test account)

login; `/me`; flag list and create; SDK-key evaluation with an OLD key; SSE `/sdk/stream` with an old key; a new signup gets a private workspace; an unconfirmed email is refused; a second user cannot see the first user's data.

## Rollback

Choose by how far the rollout got; always list the data written since the migration first, and wait for "go".

| Reached | Undo |
|---|---|
| G4 done (0004/0006/0007 applied), new code not deployed | nothing to redeploy; the old backend runs on the migrated schema (rehearsed). Restore from the G1 dump only if the schema itself must go back |
| G5 deployed, 0005 **not** applied | redeploy the previous backend and console (Render and Vercel keep prior deploys); revert env vars (the new ones are inert for old code). No restore needed |
| G6b done (0005 applied) | old code can no longer write. Either fix forward is NOT attempted; redeploy previous code **and** re-add the defaults via the commented rollback block at the bottom of `0005`, or restore from the G1 dump |

A restore from the G1 dump loses everything written after it was taken: new
signups and their workspaces, invites, flags/experiments created or changed,
audit rows, SDK keys created or revoked. The commented rollback blocks at the bottom of each migration undo them individually.

## Release 2 - the experiments stack

Do not start until release 1 is complete: 0005 applied (G6b), observation clean, G7 done.

**What it adds:** `POST /track` and experiment event ingestion; `experimentId` and
`exposureToken` in `/evaluate`; results API and the console's Experiments pages;
signed exposure tokens; the `purgeevents` command. It adds two migrations (0008, 0009)
and no change to anything release 1 uses.

**Sequence (each step on "go", with a report; never chained):**

1. **R0 Preflight** (read-only): run the checks under "After each merge" on the merged
   branch; confirm release 1 is stable in production.
2. **R1 Backup** (`backend/scripts/backup_db.sh`, via `PGSERVICE` only) and restore into a throwaway database.
3. **R2 Rehearsal** on the restored copy: 0008, `verify_0008.sql`, 0009,
   `verify_0009.sql`; the experiments backend and console against it; the Playwright
   suite (it seeds results through the real `/track`).
4. **R3 Set variables** (see the table) - `EXPOSURE_TOKEN_KEY` set, `EXPOSURE_VERIFICATION` unset (off).
5. **R4 Migrate:** 0008, verify, 0009, verify. Old backend keeps working (it never touches `events`).
6. **R5 Deploy** backend and console together; smoke: an SDK key `/evaluate` (unchanged for flags outside an experiment), a draft->running experiment, `/track` an exposure and an event, the Experiments page showing the counts, the Results tab.
7. **R6 Observe**, then schedule the purge (below) before real traffic.

**Rollback:** redeploy the previous backend and console (they never read the new
columns); the migrations' commented blocks undo 0009 then 0008 (0008's rollback
discards ingested events, so take a backup first).

**Dependencies on release 1:** 0008 needs `workspace_id` on `experiments`/`environments`
(0004, with defaults removed by 0005) and the tenant-scoped SDK keys (0006); `/track`
scoping, the every-route test and the tenant checks all assume release 1's guard.

### Day one: what keeps the experiments data path safe

* `EXPOSURE_VERIFICATION=off` (default). The Go SDK does not pass the exposure token
  yet. `required` would reject every exposure (no experiment would ever collect data);
  `report` would accept them but mark every one unverified, so every results page would
  carry a permanent "exposures could not be verified" warning. Off keeps today's
  behaviour; the risk it leaves is a client of the tenant's own SDK key reporting a
  variation it was not served (it cannot reach another tenant).
* `EXPOSURE_TOKEN_KEY` set anyway, so tokens `/evaluate` issues are signed with a real,
  shared key and verification can be switched on without a key rollout.
* Plan: when the Go SDK ships `exposureToken` passthrough (see `docs/SDK_TRACKING.md`)
  -> `report` -> watch the unverified count fall to zero in the results warnings ->
  `required`. Rotate keys as in `docs/SDK_TRACKING.md`.
* Schedule `cmd/purgeevents` (a Cloud Run Job or any scheduler, see `docs/EVENTS.md`)
  before real traffic; retention 90 days. Default rate limits stay.

## Merge order and keeping the stack consistent

The branches form one line: `main` <- `feature/workspaces-ui` <- `-tracking` <- `-results`
<- `-results-ui` <- `-hardening`. Merge in that order, one at a time.

1. **Release 1:** merge `feature/workspaces-ui` into `main` with a **merge commit or fast-forward, not a squash**. (Local `main`'s two extra commits are already inside that branch.)
2. Deploy and finish release 1 (G0-G7). Any fixes made to release 1 during the gates go on `feature/workspaces-ui`/`main` first.
3. **Release 2:** bring the stack onto the new `main`. If release 1 was merged without squashing and nothing changed since the stack was cut, the stack already sits on it: merge `feature/experiments-hardening` into `main` (it contains the three branches below it), or merge each branch in order for smaller reviews. If release 1 changed after the stack was cut (fixes, a squash merge), move each branch in order, bottom first:
   `git rebase --onto main <old base> feature/experiments-tracking`, then rebase `-results` onto the new `-tracking`, `-results-ui` onto `-results`, `-hardening` onto `-results-ui` (or merge the lower branch into each, if the branches were shared). Never rebase a branch that has been pushed and reviewed without telling the reviewer.
4. Expected conflict hot spots: `api/openapi.yaml`, `backend/internal/server/server.go`, `backend/internal/platform/ratelimit/ratelimit.go`, `backend/internal/integration/harness_test.go` (its migration-count check: 7 on release 1, 9 on release 2), `docs/ROLLOUT.md`, `docs/LOCAL_MULTITENANCY.md`. Resolve by keeping both sides' additions; never resolve a migration file by editing 0004-0007.
5. **Never commit re-rendered screenshots** (`docs/screenshots/*.png` change on every Playwright run): `git checkout -- docs/screenshots` before committing.

**After each merge, re-run:** `cd backend && go vet ./... && HELIOS_TEST_DATABASE_URL=... HELIOS_TEST_REDIS_URL=... go test -count=1 ./...` (local throwaway Postgres and Redis only); `cd frontend && npx tsc --noEmit && npm run build`; start the local stack and run `npx playwright test` (all specs); apply the migrations in order to a fresh local database and run each `verify_*.sql`; check `git status` is clean apart from screenshots.
