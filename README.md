# Helios

A control plane for feature flags and A/B experiments — decouples deploying code from releasing it.

## Tech stack

- Go — backend (control plane + evaluation data plane)
- React + TypeScript (Vite) — admin console
- PostgreSQL — persistent store
- Redis — ruleset fan-out

## Prerequisites

- Go 1.24+
- Node 22+
- Docker + Docker Compose

## Setup

```bash
git clone https://github.com/bhavyamsharmaa/A-B-Testing-Feature-Flag-Management-Console.git helios
cd helios

# Backend dependencies (backend/ is its own Go module — run go commands there)
cd backend && go mod download && cd ..

# Frontend dependencies
cd frontend && npm install && cd ..

# Start Postgres + Redis
docker compose up -d
```

The backend reads configuration from the environment (see `.env.example`); defaults
target the docker-compose services:

| Variable | Default |
| --- | --- |
| `PORT` | `8080` |
| `DATABASE_URL` | `postgres://helios:helios@localhost:5432/helios?sslmode=disable` |
| `REDIS_ADDR` | `localhost:6379` |

## Run

```bash
# Backend  → http://localhost:8080
cd backend && go run ./cmd/api

# Frontend → http://localhost:5173  (proxies /api to the backend)
cd frontend && npm run dev
```

SQL files in `backend/db/migrations/` are applied automatically the first time the
Postgres container initializes its volume (`docker compose down -v` to reset).

## Workspaces (multi-tenancy)

Every user gets a private workspace (environments, flags, experiments, audit log)
the first time they call `GET /me`. No workspace can read or change another's data:
control-plane routes take the environment **UUID** (`/environments/{envId}/...`,
UUIDs come from `/me`) and answer 404 for any environment the caller holds no role
in. For now a key such as `dev` is also accepted and resolves only inside the
caller's own workspace; that shim goes away once the console uses UUIDs.

- **One workspace per user, for now.** A user cannot belong to two workspaces, so
  members can only be added if they are not in another workspace (otherwise
  `404 USER_NOT_FOUND`, the same as an unknown user). Lifting this means changing
  the primary key of `workspace_members`.
- Limits per workspace: 3 environments, 50 flags, 10 active SDK keys
  (`409 QUOTA_EXCEEDED`). SDK keys are minted with
  `go run ./cmd/mkkey -workspace <id> -env <key>`.
- **Deploying it:** apply `0004_workspaces.sql` → `db/verify_0004.sql` → deploy the
  backend → apply `0005_drop_workspace_defaults.sql` **immediately** →
  `db/verify_0005.sql`. Rehearse on a second project first (`backend/db/rehearsal.md`)
  and take a backup (`backend/scripts/backup_db.sh`).

## Where the code lives

- [`backend/`](backend/) — Go module. `cmd/api` entrypoint; `internal/controlplane` (flags, segments, experiments, RBAC, audit) and `internal/dataplane` (evaluation engine).
- [`frontend/`](frontend/) — React + TypeScript app: `src/{components,pages,api,types}`.
- [`api/openapi.yaml`](api/openapi.yaml) — shared API contract, the source of truth both sides reference.
