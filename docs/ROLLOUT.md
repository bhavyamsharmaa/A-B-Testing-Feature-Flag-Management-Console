# Production rollout: multi-tenancy (DRAFT)

> **Status: draft written during gate G0.** The rollout plan this was meant to
> mirror did not exist in the repo, so this is reconstructed from the gate list
> given for the rollout. Review it before G1. Nothing here has run against production.

Goal: existing users and SDK integrations see no breakage, and every step can be undone.

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
