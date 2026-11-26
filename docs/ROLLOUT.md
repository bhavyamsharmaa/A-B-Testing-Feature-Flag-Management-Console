# Production rollout: multi-tenancy (DRAFT)

> **Status: DRAFT.** Reconstructed during gate G0 from the gate list given for the rollout (the plan it was meant to mirror was not in the repo).
> The label stays until the owner has reviewed it (decision of 2026-10-09, `docs/rollout-log.md`). Nothing here has run against production.
> Still open before it can be removed: (1) the owner's review; (2) the questions at the end of `docs/RELEASE1_RUNBOOK.md`; (3) the comments at the top
> of migrations `0004`, `0005`, `0006` disagree with the order below (see "Migration order"), and the migration files are not edited.

**To do the release, follow `docs/RELEASE1_RUNBOOK.md`.** This file is the plan behind it. It covers **release 1 (multi-tenancy) only**; the experiments
stack and everything after it are later releases, planned on their own branches.

Goal: existing users and SDK integrations see no breakage, and every step can be undone.

## What ships

| Piece | Production today | After |
|---|---|---|
| Database | migrations 0001-0003 | + 0004, 0005, 0006, 0007 |
| Backend (Render) | `origin/main` = `6c90844` | `release/1`: the code of `feature/workspaces-ui` (`1e2f5e6`) plus documents, scripts and CI |
| Console (Vercel) | same commit | same branch (deploy together with the backend) |

## Migration order (approved): split

0005 removes the column defaults the old backend's writes rely on: after 0005
and before the new backend is live, the old backend's writes fail with 500
(reads and SDK `/evaluate` keep working). So 0005 is **not** part of the window:

1. Window: apply **0004, 0006, 0007** only (each has its own transaction; no `psql -1`).
2. Deploy the new backend and console together. Verify. Observe.
3. Later, as its own gate (**G6b**), only on an explicit "go": apply **0005**.

The comments at the top of `0004_workspaces.sql` and `0005_drop_workspace_defaults.sql` say to apply 0005 "IMMEDIATELY" after the deploy, and those of
`0006` say it applies on top of 0005. **Both are superseded by this order**: 0004, 0006, 0007 apply and verify without 0005, and the new backend runs on
them (rehearsed). The files are left byte-for-byte as rehearsed.

What the **old** backend can do on each database state (`backend/scripts/rehearse_migration.sh` for the flag writes; the member and second-workspace
results from the dry run, `tools/release1-dryrun/`):

| Database state | SDK evaluate / reads | Flag create | Flag toggle, kill; experiments | Flag delete | Add a member |
|---|---|---|---|---|---|
| before any migration | ok / ok | ok | ok | ok | ok |
| 0004 | ok / ok | ok | ok | ok | ok |
| 0004+0006 and 0004+0006+0007, one workspace | ok / ok | ok | ok | ok | **500** |
| 0004+0006+0007, **a second workspace exists** | ok / ok (**but see below**) | **500** | ok (see below) | **403** | **500** |
| + 0005 | ok / ok | **500** | **500** | **500** | **500** |

**With a second workspace the old console cannot be relied on at all.** The old backend finds an environment by its key without saying which workspace,
and which row the database returns is not guaranteed. In one state every request worked as in the table; after one row was rewritten, every console
request for that environment answered 403. SDK evaluation was unaffected, and no other workspace's flags were shown or changed in the checks made.

Caveat: the old `mkkey` CLI fails after 0006 (`api_keys.workspace_id` NOT NULL); CLI only.
The new backend also changes who may do what: the per-environment roles are flattened into one workspace role per person (the highest old role), which
widens access for people who held a role in only some environments (`docs/RELEASE1_RUNBOOK.md`, "Decide before").

## Environment variables

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
- **G1 Backup**: take the `public`-schema dump with `pg_dump --dbname "service=helios_prod"` to a private, mode-600 file under `~/helios-release1-backups/`, outside the checkout (see `docs/RELEASE1_RUNBOOK.md`, Step 2). Do not use `backend/scripts/backup_db.sh` for this release: it writes inside the repository checkout. Per-table counts are a separate read-only query (runbook Step 1); restore into a throwaway local database and compare them (runbook Step 3). *Blocked so far: see `docs/rollout-log.md`.*
- **G2 Rehearsal on the restored copy**, with 0004, 0006, 0007 (not 0005); verify scripts; backend and console against it; existing-user login; two-user Playwright. Plus the three rollback-proof checks:
  1. *Old backend on 0004+0006+0007:* the OLD backend (`origin/main`) through its full console flows: login, `/me`, environment and member listing, role-gated actions (viewer/editor/admin attempts, allowed and refused), audit log, experiments (create/start/stop), SDK key create and revoke. Report any 500 or permission surprise, especially anything depending on the old per-environment roles after 0006 backfilled the new roles.
  2. *New backend without 0005:* the NEW backend plus the two-user Playwright scenario, and a list of every write path (each route that writes, with its result) showing it works without 0005.
  3. */sdk/stream with a pre-migration key:* on a real Redis, old and new API instances running at the same time (the channel name changed), flag updates published from each side; report whether the overlap drops any flag update and for how long.
- **G3 Prepare** (no production changes): push `release/1`, open a draft pull request to `main`, CI green; Render/Vercel changes prepared not applied; announcement written (**the console has no maintenance banner**, and this release adds none); rollback steps read; confirm previous deploys can be redeployed.
- **G4 Window + migrate**: announce, new env vars set (your confirmation); apply **0004, 0006, 0007**; verify scripts (verify_0005 is expected to fail: 0005 not applied); compare counts with G1.
- **G5 Deploy** backend and console together (you merge); watch both deploys; smoke list below.
- **G6 Observe** about 30-60 minutes; send the "complete" message only after a clean report.
- **G6b Apply 0005** (own gate, only on "go"): after G6 is clean, and only when you no longer want to redeploy the old backend. Run verify_0005, repeat the smoke list, compare counts.
- **G7 Close out**: delete the test flag and test users (release 1 cannot delete a workspace: a test account's empty workspace stays); summary (changes, final counts, downtime); keep the backup; update `docs/SECURITY_REVIEW.md` limitations with "applied to production on <date>".

Abort without fixing forward if: a row count differs from the snapshot, a migration errors, a smoke test fails, or error rates rise after deploy. Then propose rollback and wait for "go".

## Smoke list (dedicated test accounts)

The steps are in `docs/RELEASE1_RUNBOOK.md`, Step 9: existing user's login (`/me`); flag list and create; SDK-key evaluation with an OLD key and a revoked
key; SSE `/sdk/stream` with an old key; former single-environment users' access; then (these make a second workspace) a new sign-up gets a private
workspace, it cannot see the first workspace's data, and an unconfirmed email is refused.

## Rollback

Choose by how far the rollout got; always list the data written since the migration first, and wait for "go". The runbook's rollback table has the same
rows with the detail; "workspaces" means `select count(*) from workspaces`.

| Reached | Undo | Data restore needed? |
|---|---|---|
| G4 done (0004/0006/0007 applied), new code not deployed | nothing to redeploy; the old backend runs on the migrated schema, except adding a member (500) | no |
| G5 deployed, 0005 not applied, **still 1 workspace** | redeploy the previous backend and console (Render and Vercel keep prior deploys); revert env vars (the new ones are inert for old code). Adding a member still fails | no |
| G5 deployed, 0005 not applied, **2 or more workspaces** (first new sign-up, a role-less user's first sign-in, or a created workspace) | **not a clean rollback**: redeploying the old backend gives a degraded system (no flag creation or deletion, no member adding, new accounts see nothing, and the old console may answer 403 for whole environments) | yes, to be fully back |
| G6b done (0005 applied) | the old backend **cannot write at all** (reads and SDK evaluation work). Running the commented ROLLBACK block at the bottom of `0005` re-adds the defaults and returns to the matching row above; do not redeploy the old code before that | no for that block |

A restore from the G1 dump loses everything written after it was taken: new
signups and their workspaces, invites, flags/experiments created or changed,
audit rows, SDK keys created or revoked. The commented rollback blocks at the bottom of each migration undo them individually.
