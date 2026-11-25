# Helios

A control plane for feature flags and A/B experiments — decouples deploying code from releasing it.

## Tech stack

- Go — backend (control plane + evaluation data plane)
- React + TypeScript (Vite) — admin console
- PostgreSQL — persistent store
- Redis — ruleset fan-out

## Prerequisites

- Go 1.25+
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
| `REDIS_URL` | unset (no push to `/sdk/stream`) |

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
to any shared database yet**. Release 1 is done by following [docs/RELEASE1_RUNBOOK.md](docs/RELEASE1_RUNBOOK.md): back up, rehearse, apply **0004, 0006,
0007** with the matching `db/verify_000N.sql` after each, then deploy the console and backend together (the old console calls routes that no longer exist).
**`0005` is applied later, as its own step**, once you no longer want to redeploy the old backend ([docs/ROLLOUT.md](docs/ROLLOUT.md), gate G6b).

## Where the code lives

- [`backend/`](backend/) — Go module. `cmd/api` entrypoint; `internal/controlplane` (flags, segments, experiments, RBAC, audit) and `internal/dataplane` (evaluation engine).
- [`frontend/`](frontend/) — React + TypeScript app: `src/{components,pages,api,types}`.
- [`api/openapi.yaml`](api/openapi.yaml) — shared API contract, the source of truth both sides reference.
