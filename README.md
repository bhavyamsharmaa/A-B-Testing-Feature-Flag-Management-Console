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

Every user gets a private workspace the first time they sign in (the first
`GET /me` creates it). A workspace owns its environments, flags, experiments, SDK
keys and audit log; **no workspace can read or change another's data**. A user can
belong to several workspaces with a role in each (owner, admin, editor, viewer),
create more (up to 5 owned), switch between them in the console, and invite people
by email (a one-time link: Helios does not send email).

Control-plane routes take ids (`/environments/{envId}/...`, `/workspaces/{wsId}/...`)
that are only honoured for members; anything else is a 404, never a 403. SDK keys
(`/environments/{envId}/sdk-keys`) only ever see their own workspace's flags.
Per workspace: 3 environments, 50 flags, 10 active SDK keys, 20 members plus open
invites. Roles and limits are in [docs/LOCAL_MULTITENANCY.md](docs/LOCAL_MULTITENANCY.md),
which also shows how to run the whole stack locally without Supabase
(`backend/cmd/devauth`) and how to run the end-to-end test that produced
[docs/screenshots/](docs/screenshots/).

**Database:** migrations `0004`-`0007` add workspaces (roles, invites, the last-owner guarantee) and are **not applied
to any shared database yet**. Apply them in order, after a backup and a rehearsal on a
copy (`backend/db/rehearsal.md`, `backend/scripts/backup_db.sh`), with the matching
`db/verify_000N.sql` after each. `0005` must follow the backend deploy of the 0004
version immediately; if you go straight to this version, apply 0004-0007 together and
then deploy. The console and backend in this branch must be deployed together: the old
console calls routes that no longer exist.

## Where the code lives

- [`backend/`](backend/) — Go module. `cmd/api` entrypoint; `internal/controlplane` (flags, segments, experiments, RBAC, audit) and `internal/dataplane` (evaluation engine).
- [`frontend/`](frontend/) — React + TypeScript app: `src/{components,pages,api,types}`.
- [`api/openapi.yaml`](api/openapi.yaml) — shared API contract, the source of truth both sides reference.
