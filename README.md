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

## Where the code lives

- [`backend/`](backend/) — Go module. `cmd/api` entrypoint; `internal/controlplane` (flags, segments, experiments, RBAC, audit) and `internal/dataplane` (evaluation engine).
- [`frontend/`](frontend/) — React + TypeScript app: `src/{components,pages,api,types}`.
- [`api/openapi.yaml`](api/openapi.yaml) — shared API contract, the source of truth both sides reference.
