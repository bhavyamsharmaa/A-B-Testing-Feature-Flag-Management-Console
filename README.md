# Helios — A/B Testing & Feature Flag Management Console

Group G221 · Bhavyam Sharma & Hashanasherastha Behera · Mentor: Harshit Batra
Track: Product Development — Developer Tools · Delivery window: 3 months (M1/M2/M3)

Control plane (writes) and data plane (reads) are split at the package level to
mirror the PRD's architecture: the evaluation hot path never makes a database call.

## Repo layout

```
cmd/
  api/            Control-plane HTTP server (Admin API + evaluate/events/kill endpoints for now)
  evalsvc/        (future) standalone, horizontally-scaled evaluation service
internal/
  flags/          Flag & flag_config domain logic
  segments/       Segment domain logic
  experiments/    Experiment lifecycle, sample-size calc
  audit/          Audit log writer (same-transaction inserts)
  rbac/           Role checks, server-side enforcement
  evaluation/      <-- start here. Bucketing + in-memory engine (US-03, US-09)
db/migrations/     SQL schema (0001_init.sql covers flags/segments/experiments/audit)
api/openapi.yaml   Frozen API contract (Week 1 deliverable per PRD delivery plan)
console/            React + TypeScript admin UI (not yet scaffolded)
```

## Local setup

```bash
docker compose up -d postgres redis   # schema in db/migrations auto-applies on first boot
go mod tidy
go run ./cmd/api
curl localhost:8080/healthz
```

## What's already here vs. what's next

**Done (this scaffold):**
- Full schema for flags/flag_configs/segments/experiments/exposures/metric_events/audit_logs,
  with the PRD's normative constraints encoded as DB constraints where possible
  (one running experiment per flag, 2–20 variations, append-only audit log).
- `internal/evaluation`: the bucketing algorithm (`hash(flagKey+salt+subjectKey) % 100000`,
  never `rand()`) and the lock-free `Engine` with the required-fallback `Evaluate()` signature.
- OpenAPI contract for all six PRD-listed endpoints plus flag/segment/experiment CRUD.
- docker-compose for local Postgres + Redis, Dockerfile for the API.

**Your Week 1 target, concretely, is:**
1. Wire `cmd/api/main.go`'s TODO route block to real handlers in `internal/flags`,
   `internal/segments` (basic CRUD against Postgres — the engine's `Ruleset` doesn't
   need to be populated from these yet, that's M2).
2. `POST /environments/{env}/flags` → insert into `flags` + `flag_configs`, return 409 on
   duplicate key (unique constraint is already there, just needs a mapped error).
3. Console shell: React + TS, flag list + create-flag form calling the above.
4. **Gate for M1**: a flag created in the console evaluates correctly via `POST /evaluate`
   — that means `internal/evaluation.Engine` needs *some* path (even a naive DB-backed
   ruleset load, not yet Redis pub/sub) wired into the `/evaluate` handler by end of Week 4.

**Explicitly NOT Week 1:** Redis pub/sub fan-out, SSE stream, RBAC middleware, audit
writes, statistics service. Those are M2/M3 per the delivery plan — don't let scope
creep from those into the Week 1 target.

## Suggested split (per PRD's engineer boundary)

- **Engineer A (evaluation path):** `internal/evaluation`, later `internal/experiments`
  statistics, `cmd/evalsvc`.
- **Engineer B (control path):** `internal/flags`, `internal/segments`, `internal/rbac`,
  `internal/audit`, `console/`.

The API contract in `api/openapi.yaml` is the integration boundary — freeze it before
diverging into these two tracks, per the PRD's stated risk mitigation.
