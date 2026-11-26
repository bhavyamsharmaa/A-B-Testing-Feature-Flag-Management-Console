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
- order 0004,0006,0007 (0005 deferred): old-backend SDK, reads and writes ok at every step; new backend ok. *(Corrected below, 2026-11-25: only flag writes with a single workspace were tried.)*
- order 0004,0006,0007,0005: same, writes break only after 0005.

Findings: ROLLOUT.md did not exist (draft written); 0005 breaks old-backend writes; `backend/backups/` was not git-ignored (now is); `DATABASE_URL` is not visible to the tool shell.

Stopped. Waiting for "go 1".

## Decisions - 2026-10-09T13:26:17Z
- Approved by the owner: split order. Window applies 0004, 0006, 0007 only; 0005 is a separate later gate (G6b) on an explicit "go". ROLLOUT.md updated (gates, rollback, smoke list). DRAFT label stays until the owner has reviewed it.
- Three extra G2 rehearsal checks added to ROLLOUT.md (old backend full console flows on 0004+0006+0007; new backend without 0005 incl. write-path list; /sdk/stream on real Redis with overlapping old and new instances).

## G1 Backup - 2026-10-09T13:26:17Z
"go 1" received. BLOCKED before any command touched the database: `test -n "$DATABASE_URL"` in the tool shell reports NOT set. No dump attempted, nothing run against production. Need the CLI launched from a shell where DATABASE_URL is exported (or a pgpass/PGSERVICE setup).

## G1 Backup - second attempt - 2026-10-09T13:34:11Z
Owner: connection is now PGSERVICE=helios_prod (~/.pg_service.conf + ~/.pgpass); DATABASE_URL no longer needed; Render auto-deploy off, Vercel previews skipped for non-main, nothing is pushed before G3.
- backend/scripts/backup_db.sh now takes no URL at all: `pg_dump --dbname "service=$PGSERVICE"`; it unsets DATABASE_URL and PG* connection variables. No credential can appear in `ps`.
- BLOCKED again before touching the database: from the tool shell neither ~/.pg_service.conf nor ~/.pgpass exists (HOME=/Users/bhavyamsharmaa), so the service cannot resolve. No sanity check, no dump, nothing run against production.

## Correction: what the old backend can still do (recorded 2026-11-25)
The G0 result above covered the old backend's flag writes with **one** workspace. The later dry run on synthetic data (`tools/release1-dryrun/`, local only) found two
writes that fail on the migrated database: **adding a member** (500 from 0006 on) and, **once a second workspace exists, creating a flag** (500). So redeploying the
old backend is a full rollback of the flag features only while there is one workspace; after the first new sign-up it is a degraded one. With a second workspace, deleting a flag is also refused (403) and
the old backend can pick the wrong workspace's environment by key, answering 403 for everything in it (measured 2026-11-25). After 0005 the old backend
cannot write at all (reads, sign-in and SDK evaluation work). The tables in `docs/ROLLOUT.md` and `docs/RELEASE1_RUNBOOK.md` say this. Nothing ran against production.
