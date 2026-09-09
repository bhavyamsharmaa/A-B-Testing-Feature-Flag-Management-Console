# Helios

A control plane for feature flags and A/B experiments — decouples deploying code from releasing it.

## Tech stack

- Go — backend / evaluation API
- React + TypeScript — admin console
- PostgreSQL — persistent store
- Redis — ruleset fan-out

## Prerequisites

- Go 1.22+
- Node 22+
- Docker + Docker Compose

## Setup

```bash
# Clone
git clone https://github.com/bhavyamsharmaa/A-B-Testing-Feature-Flag-Management-Console.git helios
cd helios

# Install dependencies
go mod download
(cd console && npm install)

# Environment variables (backend reads these; PORT is optional, defaults to 8080)
export DATABASE_URL="postgres://helios:helios@localhost:5432/helios?sslmode=disable"
export REDIS_ADDR="localhost:6379"
export PORT="8080"

# Start Postgres + Redis
docker compose up -d postgres redis
```

## Run

```bash
# Backend (from repo root)
go run ./cmd/api

# Frontend
cd console && npm run dev
```

Migrations in `db/migrations/` are applied automatically the first time the Postgres
container initializes its volume. To re-run them against a running database:

```bash
docker compose exec -T postgres psql -U helios -d helios -f /docker-entrypoint-initdb.d/0001_init.sql
```

## Where the code lives

Backend in [`cmd/`](cmd/) and [`internal/`](internal/); frontend in [`console/`](console/).
