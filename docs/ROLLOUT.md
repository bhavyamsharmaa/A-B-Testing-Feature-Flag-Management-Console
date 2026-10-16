# Production rollout: two releases, and a third after them (DRAFT)

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

**A fifth release comes after the fourth: scoped API tokens for CI and scripts** (`feature/api-tokens`,
section "Release 5" below). It adds one migration, 0012, and needs only release 1's schema.

**A fourth release comes after the third: promotion between environments and scheduled changes**
(`feature/scheduling`, section "Release 4" below). It adds one migration, 0011, and needs only
release 1's schema.

**A third release comes after both: segments and the broader targeting operators**
(`feature/segments`, section "Release 3" below). It is not part of either release above,
needs only release 1's schema, and must not be started until release 2 (if it is shipping)
is finished or deliberately skipped. It adds one migration, 0010.

## Stack status

As of this writing (before the commit that added this section). Nothing was pushed
by the work that produced the experiments stack; only `feature/workspaces-ui` has
ever been pushed (at your request, early). Remote-tracking refs seen locally:
`origin/main` = `6c90844`, `origin/feature/workspaces-ui` = `1e2f5e6`. Nothing was
fetched to produce this table.

| Branch | Head | Pushed? | Based on |
|---|---|---|---|
| `main` (local) | `6c90844` | equals `origin/main` (`6c90844`). Until 2026-10-11 local `main` had been 2 commits ahead (`f134373`, `7381cc6`, both already on origin inside `feature/workspaces-ui`); it was moved back locally, and a local pre-push hook refuses pushes to `main` | `origin/main` |
| `feature/workspaces-ui` (release 1) | `1e2f5e6` | yes, equals `origin/feature/workspaces-ui` | `main` |
| `feature/experiments-tracking` | `865eb82` | no | `feature/workspaces-ui` |
| `feature/experiments-results` | `b5aea5c` | no | `feature/experiments-tracking` |
| `feature/experiments-results-ui` | `96438f5` | no | `feature/experiments-results` |
| `feature/experiments-hardening` | `36c4620` (plus the commit adding this section) | no | `feature/experiments-results-ui` |

**This table is a snapshot and goes stale.** Before each release gate, recheck every row from
local refs: `git rev-parse --short main origin/main feature/workspaces-ui ...` and
`git branch -vv`; fetch first only when you intend to look at the remote.

The history is strictly linear (each branch contains the one before it; checked with
`git merge-base --is-ancestor`). A bundle of all local branches is kept in
`~/helios-backups/` (outside the repository).

**A sixth release comes after the fifth: webhooks and invite email on one shared outbox**
(`feature/webhooks-email`, section "Release 6" below). It adds one migration, 0013, and needs release 5's
`api_tokens` (one column on `scheduled_changes` references it) and release 4's `scheduled_changes`.

## Migrations 0004-0013

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

| 0010 segments | **3** | after releases 1 and 2 are finished | two NEW tables, `segments` and `segment_members` (`workspace_id` on both, composite FK, RLS on, indexes lead with `workspace_id`); no existing table changes | none on existing data: new empty tables. The foreign keys take `SHARE ROW EXCLUSIVE` on `workspaces` and `auth.users` for milliseconds while created (blocks writes to those two tables, not reads) | commented block: `DROP TABLE` both (deploy a backend without segments first; discards segments) | `verify_0010.sql` |

| 0011 scheduled changes | **4** | after release 3 (or on its own after release 1: it needs only `workspaces`, `flags`, `environments`) | one NEW table, `scheduled_changes` (`workspace_id`, composite FKs, RLS on, indexes lead with `workspace_id` except the two partial claim indexes); no existing table changes | none on existing data: a new empty table. The foreign keys take `SHARE ROW EXCLUSIVE` on `workspaces`, `flags`, `environments` and `auth.users` for milliseconds while created (blocks writes to those tables, not reads) | commented block: `DROP TABLE` (deploy a backend without scheduling first; discards scheduled changes) | `verify_0011.sql` |

| 0012 api tokens | **5** | after release 4 (or on its own after release 1: it needs `workspaces`, `environments`, `audit_logs`) | two NEW tables (`api_tokens`, `api_token_environments`; `workspace_id`, composite FKs, RLS on) and two columns on `audit_logs` (`actor_type` default `user`, `actor_detail`) | new tables: none on existing data (FKs take `SHARE ROW EXCLUSIVE` on `workspaces`, `environments`, `auth.users` for milliseconds). `audit_logs`: `ADD COLUMN ... DEFAULT <constant>` is catalog-only on PostgreSQL 11+ (no rewrite, no row updated, the append-only trigger does not fire) with a brief `ACCESS EXCLUSIVE` that queues behind running inserts; the CHECK is added NOT VALID then validated (`SHARE UPDATE EXCLUSIVE`, inserts continue) | commented block: drop the tables and the two audit columns (deploy a backend without tokens first; discards tokens) | `verify_0012.sql` |

| 0013 webhooks, email, outbox | **6** | after release 5 (it references `api_tokens` and `scheduled_changes`) | two NEW tables (`webhook_endpoints`, `outbox`; `workspace_id`, composite FKs, RLS on, indexes lead with `workspace_id` except the claim, lease, purge and recipient indexes that look across workspaces by time or recipient); `scheduled_changes.created_by_token_id` (nullable, FK added NOT VALID then validated); `UNIQUE (id, workspace_id)` on `workspace_invites` | new tables: none on existing data (FKs take `SHARE ROW EXCLUSIVE` on `workspaces`, `environments`, `auth.users`, `api_tokens` for milliseconds). `scheduled_changes`: one nullable column, catalog-only, brief `ACCESS EXCLUSIVE`. `workspace_invites`: the unique index build takes a `SHARE` lock (invite writes wait, reads do not; milliseconds on a table this small) | commented block: drop both tables, the constraint, the index and the column (deploy a backend without webhooks and invite email first; discards endpoints, secrets and queued deliveries) | `verify_0013.sql` |

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

**Release 6 (webhooks and invite email):** all optional; with none set, webhooks are unavailable (endpoints cannot be
created: fail closed) and invite email goes to a sink that sends nothing. See `docs/WEBHOOKS.md`, `docs/OUTBOX.md`.

| Name | New | Recommended production value |
|---|---|---|
| `WEBHOOK_SECRET_KEY` | **secret**, 32 random bytes, base64 (`openssl rand -base64 32`), the same on every instance and on the outbox job | set it before announcing webhooks; secret store only; back it up with the database backups (without it, stored webhook secrets cannot be read) |
| `WEBHOOK_SECRET_KEY_PREVIOUS` | **secret**, rotation only | unset |
| `WEBHOOK_ALLOW_INSECURE_LOCAL` | development switch (http and loopback endpoints) | **unset** |
| `OUTBOX_SECRET` | **secret**, at least 32 characters; enables `POST /internal/outbox/run` | optional: set it if a Cloud Scheduler HTTP job will run the worker, otherwise use `cmd/outbox` |
| `MAIL_MODE` | `sink` (default) or `smtp` | `smtp` when real invite email is wanted |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD` (**secret**), `SMTP_FROM`, `SMTP_TLS` | SMTP provider settings (`starttls` default, or `implicit`) | provider's submission settings |
| `MAIL_ALLOW_INSECURE_SMTP`, `MAIL_SINK_DIR` | development only | **unset** |
| `PUBLIC_CONSOLE_URL` | the console's origin, for the link in invite emails; required with `MAIL_MODE=smtp` | the Vercel URL |
| `RATE_LIMITS` | new classes `webhook_write`, `webhook_test`, `outbox_run` | unset |

**Release 5 (API tokens):** no new variables in the backend or the console. Three rate-limit classes,
overridable through `RATE_LIMITS`: `api_token` (per token, default burst 200 then 20/s), `token_invalid` (failed
token authentications per IP, burst 30 then one per 2 s) and `api_token_write` (creating and revoking, per
user). The verification cache TTL is a constant (15 s, `apitoken.DefaultCacheTTL`).

**Release 4 (scheduling):** two optional variables on the API. `SCHEDULER_SECRET` (a **secret**, at
least 32 characters) enables `POST /internal/scheduler/run`; unset, the endpoint does not exist (404) and
changes are run by `cmd/scheduler` instead. `SCHEDULER_GRACE` (a duration, 1m to 24h, default 15m) is how
late a scheduled change may still run; an invalid value stops the API at startup. Two rate-limit classes:
`schedule_write` (per user, default burst 30 then 1/s) and `scheduler_run` (per IP, default burst 10 then
one per 5 s), overridable through `RATE_LIMITS`. The console has no new variables.

**Release 3 (segments):** no new variables in the backend or the console; one new rate-limit
class, `segment_write` (per user, POST/PATCH/DELETE on segments; default 1/s with a burst of 30,
override with `RATE_LIMITS="segment_write=perSecond/burst"`; reads keep the general limit). Limits are
constants in `quota` (20 segments per workspace, 10,000 explicit members per segment,
20 rule clauses / 8,192 bytes per segment); changing them is a code change.

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

## Release 3 - segments and more targeting operators

Do not start until release 2 is complete (or has been consciously skipped, in which case
re-check the dependency below) and observed.

**What it adds:** reusable audience segments (explicit members and/or attribute rules)
that a flag's targeting rule can reference with `inSegment` / `notInSegment`; the
operators `startsWith`, `endsWith`, `lessThan`, `lessThanOrEqual`, `greaterThanOrEqual`,
semver and date comparisons, `matchesRegex` (Go RE2, length-capped), and a `negate`
flag on any clause; six workspace routes (`/workspaces/{wsId}/segments...`); the
Segments page and a segment picker in the targeting editor. See `docs/SEGMENTS.md`.

**Schema:** one migration, 0010 - two new tables, nothing else touched. Because nothing
existing changes, the previous backend keeps running against the migrated database
(it never reads the tables). The reverse is not true: do not save flag rules with the new
operators or segment clauses and then put an older backend back — it treats an operator it
does not know as "no match", so those rules would quietly stop matching.

**Evaluation is unchanged for flags that use none of it:** `internal/dataplane/evaluation`
has a golden file (`testdata/legacy_golden.txt`, ~7,700 results recorded from the code
before this release) that must stay byte-identical. `/evaluate` runs one extra query only
when a requested flag's rules name a segment.

**Sequence (each step on "go", with a report; never chained):**

1. **S0 Preflight** (read-only): the checks under "After each merge" on the merged branch.
2. **S1 Backup** (`backend/scripts/backup_db.sh`, via `PGSERVICE` only) and restore into a throwaway database.
3. **S2 Rehearsal** on the restored copy: 0010, `verify_0010.sql`, the segments backend and
   console against it, the Playwright suite. Steps in `backend/db/rehearsal.md` ("Rehearsing 0010").
4. **S3 Migrate:** 0010, `verify_0010.sql`. The running backend is unaffected.
5. **S4 Deploy** backend and console together. Smoke: create a segment with two members,
   reference it from a rule of a test flag in dev, `/evaluate` as a member and as a stranger
   with a dev SDK key, edit the members and see the answer change, delete the segment (the usage
   warning must name the flag), confirm the audit log shows `segment.create`, `segment.update`,
   `segment.force_delete` without a member list.
6. **S5 Observe**, including the p95 of `/evaluate` for flags that use a segment (one extra indexed query).

**Rollback:** redeploy the previous backend and console. Segments already created stay in the
database, unused. **Before redeploying a backend older than segments, find and revert the flags
that use the new features** — an older evaluator treats segment clauses, the added operators and
`negate` as "no match", so those rules silently stop matching (no error, no log). List them with
`DATABASE_URL=... go run ./cmd/segmentusage` (read-only, keys and counts only) or the SQL in
`docs/SEGMENTS.md` ("Rolling back to a backend older than segments"); revert each in the console,
re-run until it prints nothing, then redeploy. Undoing 0010 itself is the commented `DROP TABLE`
block in the file, after a backup.

**Dependency on the earlier releases:** `segments.workspace_id` references `workspaces` (0004);
the every-route test, the tenant checks and the workspace guard of release 1 are assumed. It
does not need 0008 or 0009, so it could ship without release 2, but this plan keeps the order.

## Release 6 - webhooks and invite email

Do not start until release 5 is finished and observed.

**What it adds:** one outbox and one delivery worker for everything sent outside the system: signed webhooks
(nine event types, endpoint management in the console, delivery log, test and resend, SSRF protection,
encrypted signing secrets) and invite emails (SMTP over TLS or a development sink, status and resend in the
console, daily limits). Also two fixes from release 5: a scheduled change made through an API token is
cancelled when the token is revoked or expires, and the executor refuses it if the token is gone; and a test
that fails when `openapi.yaml`'s token-accessible marks differ from the router's allowlist.

**Schema:** one migration, 0013 (lock impact in the table above). The previous backend keeps running against
the migrated database: it does not read the new tables or column. Nothing is sent until something runs the
worker (`cmd/outbox` or `POST /internal/outbox/run`) **and** an endpoint exists or an invite is created, so
deploying the backend alone sends nothing.

**Safety to confirm before enabling:** `WEBHOOK_SECRET_KEY` is set identically everywhere and backed up;
`WEBHOOK_ALLOW_INSECURE_LOCAL` and `MAIL_ALLOW_INSECURE_SMTP` are unset; real mail needs `MAIL_MODE=smtp`
(until then the console says plainly that emails are not actually sent); the SSRF guard has its own tests
(`internal/platform/netguard`) and the delivery path re-validates the destination on every attempt.

**Sequence (each step on "go", with a report; never chained):**

1. **W0 Preflight** (read-only): the checks under "After each merge" on the merged branch.
2. **W1 Backup** (`backend/scripts/backup_db.sh`, via `PGSERVICE` only) and restore into a throwaway database.
3. **W2 Rehearsal** on the restored copy: 0013, `verify_0013.sql`, the backend with a throwaway
   `WEBHOOK_SECRET_KEY` and `WEBHOOK_ALLOW_INSECURE_LOCAL=true` and a local receiver, the Playwright suite, and
   the steps in `backend/db/rehearsal.md` ("Migration 0013").
4. **W3 Migrate:** 0013, `verify_0013.sql`. The running backend is unaffected.
5. **W4 Deploy** backend and console together **without** `MAIL_MODE=smtp` and without any outbox job. Smoke: add
   a webhook to a public test receiver (a request-bin you control), send a test; create an invite and see the
   console say emails are not sent.
6. **W5 Run the worker by hand once:** `DATABASE_URL=… WEBHOOK_SECRET_KEY=… go run ./cmd/outbox` after a flag edit:
   the receiver gets one signed POST; the audit log has no secret.
7. **W6 Turn on the schedule** (on "go"): a Cloud Scheduler job or Render cron running `cmd/outbox` every minute,
   as in `docs/OUTBOX.md`; then, separately, real email (`MAIL_MODE=smtp` with the provider's settings and a test
   invite to an address you own).

**Rollback:** stop the outbox job first (nothing is sent; rows wait), then redeploy the previous backend and
console. Queued webhook deliveries and invite emails are not undone by a rollback: delete the endpoints in the
console first if nothing should be sent when the job returns. Undoing 0013 itself is the commented block at
the end of the file, after a backup; it discards endpoints, secrets and the delivery log.

## Release 5 - API tokens

Do not start until the release before it in your plan is finished and observed.

**What it adds:** `hlt_` bearer tokens for CI and scripts (create, list, revoke, admins and owners only, in the
console or with a session), an allowlist of the routes a token may reach, `GET /workspaces/{wsId}/environments`,
and an actor type on audit entries. See `docs/API_TOKENS.md`.

**Schema:** one migration, 0012: two new tables and two columns on `audit_logs`. The previous backend keeps
running against the migrated database (it neither reads nor writes the new columns, which have defaults). The
reverse is also safe: a token minted and then a rollback to an older backend leaves the token **unused**, not
more powerful: the older backend has no token authentication and refuses `hlt_` bearers as invalid JWTs.

**Authority to confirm before announcing it:** a token is never above its creator (checked on every request,
from the live membership), never above its own role, restricted tokens cannot see other environments, the
production approval gate applies, and tokens cannot manage tokens.

**Sequence (each step on "go", with a report; never chained):**

1. **K0 Preflight** (read-only): the checks under "After each merge" on the merged branch.
2. **K1 Backup** (`backend/scripts/backup_db.sh`, via `PGSERVICE` only) and restore into a throwaway database.
3. **K2 Rehearsal** on the restored copy: 0012, `verify_0012.sql`, the backend, the Playwright suite, and the
   steps in `backend/db/rehearsal.md` ("Rehearsing 0012"). Confirm the pre-0012 audit entries still read as `user`.
4. **K3 Migrate:** 0012, `verify_0012.sql`. The running backend is unaffected.
5. **K4 Deploy** backend and console together. Smoke: create a viewer token limited to dev, read dev flags with
   it, confirm staging is 404 and `/api-tokens` is 403 `TOKEN_NOT_ALLOWED`; create an editor token, confirm a
   production edit is 403; edit a dev flag with it and find the audit entry with the token as actor; revoke it
   and confirm 401 at once on every instance (look at the second Render instance, if there is one).
6. **K5 Observe**, including the rate of `429` on `/environments/...` for token traffic and any
   `api_token.rejected` audit entries (a revoked token still in use is worth chasing).

**Rollback:** redeploy the previous backend and console. Tokens already created stay in the database,
unusable. Undoing 0012 itself is the commented block in the file, after a backup; it discards the tokens.

## Release 4 - promotion and scheduled changes

Do not start until the release before it in your plan is finished (release 1 is the real dependency;
release 3 is the order this plan uses) and observed.

**What it adds:** promotion of a flag's configuration between environments (preview, then apply, through
the ordinary edit path); scheduled changes (enable, disable, kill, patch) with a worker that is a
function, not a process (`cmd/scheduler`, `POST /internal/scheduler/run`); the flag drawer's Schedule and
Promote dialogs and a scheduled-changes list. See `docs/SCHEDULING.md`.

**Schema:** one migration, 0011, a new empty table; nothing existing changes. The previous backend keeps
running against the migrated database. Nothing runs scheduled changes until something calls the
scheduler, so deploying the backend alone changes no flag at any time.

**Authority and safety to confirm before enabling the scheduler in production:** a production change needs
an approver both when scheduled and when it runs (creator's role re-read at execution); a stale change is
marked `missed` after the grace window, never run late; a cancel racing an execution never half applies.

**Sequence (each step on "go", with a report; never chained):**

1. **T0 Preflight** (read-only): the checks under "After each merge" on the merged branch.
2. **T1 Backup** (`backend/scripts/backup_db.sh`, via `PGSERVICE` only) and restore into a throwaway database.
3. **T2 Rehearsal** on the restored copy: 0011, `verify_0011.sql`, the backend with `SCHEDULER_SECRET` set to
   a throwaway value, the Playwright suite, and the steps in `backend/db/rehearsal.md` ("Rehearsing 0011").
4. **T3 Migrate:** 0011, `verify_0011.sql`. The running backend is unaffected.
5. **T4 Deploy** backend and console together **without** `SCHEDULER_SECRET` and without any scheduler job.
   Smoke: promote a test flag dev to staging (preview shows the diff; apply; audit shows `promotedFrom`);
   schedule an `enable` for a test flag in dev for two minutes ahead and look at the list.
6. **T5 Run it by hand once:** `DATABASE_URL=… go run ./cmd/scheduler` after the time: the flag is enabled,
   the audit log shows "… via schedule" and `scheduled_change.succeeded`.
7. **T6 Turn on the schedule** (on "go"): set `SCHEDULER_SECRET` from the secret store and create the Cloud
   Scheduler job as in `docs/SCHEDULING.md`. Watch the counts and the audit log.

**Rollback:** disable the Cloud Scheduler job first (nothing runs any more; pending changes stay pending,
and any that pass the grace window are marked `missed` when it is turned back on, not run late), then
redeploy the previous backend and console. Pending scheduled changes are not undone by a rollback: cancel
them in the console first if they should not happen once scheduling returns. Undoing 0011 itself is the
commented `DROP TABLE` block in the file, after a backup.

## Merge order and keeping the stack consistent

The branches form one line: `main` <- `feature/workspaces-ui` <- `-tracking` <- `-results`
<- `-results-ui` <- `-hardening` <- `feature/ci` <- `feature/segments` (release 3) <- `feature/scheduling` (release 4) <- `feature/api-tokens` (release 5). Merge in that order, one at a time.

1. **Release 1:** merge `feature/workspaces-ui` into `main` with a **merge commit or fast-forward, not a squash**. (Local `main`'s two extra commits are already inside that branch.)
2. Deploy and finish release 1 (G0-G7). Any fixes made to release 1 during the gates go on `feature/workspaces-ui`/`main` first.
3. **Release 2:** bring the stack onto the new `main`. If release 1 was merged without squashing and nothing changed since the stack was cut, the stack already sits on it: merge `feature/experiments-hardening` into `main` (it contains the three branches below it), or merge each branch in order for smaller reviews. If release 1 changed after the stack was cut (fixes, a squash merge), move each branch in order, bottom first:
   `git rebase --onto main <old base> feature/experiments-tracking`, then rebase `-results` onto the new `-tracking`, `-results-ui` onto `-results`, `-hardening` onto `-results-ui` (or merge the lower branch into each, if the branches were shared). Never rebase a branch that has been pushed and reviewed without telling the reviewer.
4. Expected conflict hot spots: `api/openapi.yaml`, `backend/internal/server/server.go`, `backend/internal/platform/ratelimit/ratelimit.go`, `backend/internal/integration/harness_test.go` (its migration-count check: 7 on release 1, 9 on release 2, 10 on release 3), `docs/ROLLOUT.md`, `docs/LOCAL_MULTITENANCY.md`. Resolve by keeping both sides' additions; never resolve a migration file by editing 0004-0007.
5. **Never commit re-rendered screenshots** (`docs/screenshots/*.png` change on every Playwright run): `git checkout -- docs/screenshots` before committing.

**After each merge, re-run:** `cd backend && go vet ./... && HELIOS_TEST_DATABASE_URL=... HELIOS_TEST_REDIS_URL=... go test -count=1 ./...` (local throwaway Postgres and Redis only); `cd frontend && npx tsc --noEmit && npm run build`; start the local stack and run `npx playwright test` (all specs); apply the migrations in order to a fresh local database and run each `verify_*.sql`; check `git status` is clean apart from screenshots.
