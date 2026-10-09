# Rollout log

Times are UTC. Secrets are never recorded; the production URL is only ever referred to as `$DATABASE_URL`.

## G0 Preflight (read-only) - 2026-10-09T13:22:52Z

All against local throwaway services (Postgres 127.0.0.1:55432, no production access; `DATABASE_URL` is unset in the tool shell).

| Check | Command | Result |
|---|---|---|
| vet | `go vet ./...` | clean |
| backend tests | `go test ./... -count=1` with `HELIOS_TEST_DATABASE_URL` (local) | all packages ok |
| real Redis | `HELIOS_TEST_REDIS_URL=redis://127.0.0.1:56379 go test -run 'Redis\|Revoc'` | 4 passed |
| frontend | `tsc -b --noEmit`, `npm run build` | clean |
| Playwright | `npm run e2e` on fresh binaries and fresh DB with 0001-0007 | 2 passed |
| secret scan | `git grep` + history of origin/main..HEAD | only placeholders and test fixtures |
| deploy triggers | no CI workflows, no render.yaml in repo | auto-deploy is configured in the Render/Vercel dashboards: unverifiable from here |
| SDK-key continuity | `backend/scripts/rehearse_migration.sh` (keys minted by the code on origin/main) | see below |

SDK-key continuity (keys minted by the OLD code, one revoked, 20 subjects x 5 flags, byte-for-byte baseline):
- order 0004,0005,0006,0007: SDK evaluation identical after every migration (old backend) and on the new backend; revoked key still 401; ids and row counts identical; verify_0005/6/7 pass. Old-backend writes: 500 after 0005.
- order 0004,0006,0007 (0005 deferred): old-backend SDK, reads and writes ok at every step; new backend ok.
- order 0004,0006,0007,0005: same, writes break only after 0005.

Findings: ROLLOUT.md did not exist (draft written); 0005 breaks old-backend writes; `backend/backups/` was not git-ignored (now is); `DATABASE_URL` is not visible to the tool shell.

Stopped. Waiting for "go 1".
