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

## Ordering constraint (found in the G0 rehearsal)

The new backend **requires** 0004-0007. Deploying it before them breaks it.
Migration **0005 removes the column defaults the old backend's writes rely on**:
after 0005 and before the new backend is live, the old backend's flag create,
toggle and audit writes fail with 500 (reads and SDK `/evaluate` keep working).

Rehearsed on a legacy-shaped database (old code minted the keys):

| State of the database | Old backend: SDK evaluate | reads | writes |
|---|---|---|---|
| before | ok | ok | ok |
| after 0004 | ok | ok | ok |
| after 0004+0005 | ok | ok | **500** |
| after 0004+0006 | ok | ok | ok |
| after 0004+0006+0007 | ok | ok | ok |
| after 0004+0006+0007+0005 | ok | ok | **500** |

**Recommended order (needs your approval at G3):** in the window apply
**0004, 0006, 0007**; deploy backend and console; verify; apply **0005 afterwards**.
Until 0005, rolling back the code alone is enough (old backend still writes).
The new backend was also rehearsed on the 0004+0006+0007 state: all checks pass.
The documented order (0004, 0005, 0006, 0007) also passes the rehearsal, but
leaves old-backend writes broken from 0005 until the deploy completes.
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

Each gate starts only on "go <n>" and ends with a report.

- **G0 Preflight** (read-only): tests, builds, e2e, secret scan, env list, SDK-key continuity rehearsal.
- **G1 Backup**: `pg_dump` to git-ignored `backend/backups/<timestamp>.dump`, per-table counts and max ids to `<timestamp>-counts.txt`, restore into a throwaway local DB and compare.
- **G2 Rehearsal on the restored copy**: `backend/scripts/rehearse_migration.sh --db <copy> --admin-email <real admin>`; verify scripts; backend and console against it; two-user Playwright.
- **G3 Prepare**: merge plan, draft PR to `main`, env changes prepared (not applied), banner plan, rollback commands, confirm previous Render and Vercel deploys can be redeployed.
- **G4 Window + migrate**: banner on, new env vars set; apply migrations (order above); verify scripts; compare counts with G1.
- **G5 Deploy** backend and console together (you merge); smoke list below.
- **G6 Observe** about 30 minutes; banner off only after a clean report. (Then 0005, if deferred.)
- **G7 Close out**: remove test accounts, final summary, keep the backup, update `docs/SECURITY_REVIEW.md`.

Abort without fixing forward if: a row count differs from the snapshot, a migration errors, a smoke test fails, or error rates rise after deploy. Then propose rollback and wait for "go".

## Smoke list (dedicated test account)

login; `/me`; flag list and create; SDK-key evaluation with an OLD key; SSE `/sdk/stream`; a new signup gets a private workspace; an unconfirmed email is refused; a second user cannot see the first user's data.

## Rollback

1. Redeploy the previous backend and console (Render and Vercel keep prior deploys).
2. Revert env vars (the new ones are inert for the old code).
3. If migrations ran and the schema must go back: restore from the G1 dump. Data written after the migration (new workspaces, invites, flags created, audit rows) **is lost** by a restore; list it first and wait for "go".
4. Code-only rollback is enough while 0005 is not applied. The commented rollback blocks at the bottom of each migration undo them individually.
