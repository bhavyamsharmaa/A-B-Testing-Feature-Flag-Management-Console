# Running the multi-tenant stack locally

Everything here runs on your machine. Nothing touches Supabase, Render or any
shared database.

You need Go 1.24+, Node 22+ and a local Postgres 16 (Docker's `postgres:16` works
too: `docker compose up -d postgres` applies every migration in
`backend/db/migrations/` on first start).

## 1. A throwaway Postgres (no Docker)

```bash
brew install postgresql@16
PGBIN=/opt/homebrew/opt/postgresql@16/bin
$PGBIN/initdb -D /tmp/helios-pg -U helios --auth=trust -E UTF8
$PGBIN/pg_ctl -D /tmp/helios-pg -o "-p 55432 -c unix_socket_directories= -c listen_addresses=127.0.0.1" -l /tmp/helios-pg.log start
$PGBIN/createdb -h 127.0.0.1 -p 55432 -U helios helios_dev
for f in backend/db/migrations/000[1-6]_*.sql; do
  $PGBIN/psql -h 127.0.0.1 -p 55432 -U helios -d helios_dev -v ON_ERROR_STOP=1 -q -f "$f"
done
```

## 2. Auth stand-in, backend, console

```bash
export DATABASE_URL='postgres://helios@127.0.0.1:55432/helios_dev?sslmode=disable'

# Supabase Auth stand-in on :54321 (refuses any non-local database)
(cd backend && go run ./cmd/devauth) &

# Backend on :8080, trusting devauth's signing keys
(cd backend && SUPABASE_URL=http://127.0.0.1:54321 CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173 go run ./cmd/api) &

# Console on :5173 (frontend/.env.local, which is git-ignored)
cat > frontend/.env.local <<'ENV'
VITE_API_BASE_URL=http://localhost:8080
VITE_SUPABASE_URL=http://127.0.0.1:54321
VITE_SUPABASE_PUBLISHABLE_KEY=local-dev-not-a-secret
ENV
(cd frontend && npm install && npm run dev -- --host 127.0.0.1)
```

Open <http://127.0.0.1:5173>, choose **Create an account**, and you land in your own
empty workspace. `devauth` auto-confirms accounts; start it with
`-require-confirmation` to see the "check your email" state (confirm with
`curl 'http://127.0.0.1:54321/dev/confirm?email=you@example.com'`).

Two people at once: use a second browser profile (or a private window).

## 3. Tests

```bash
cd backend && go vet ./... && go test ./...          # database-backed tests skip without the next variable
HELIOS_TEST_DATABASE_URL='postgres://helios@127.0.0.1:55432/postgres?sslmode=disable' go test ./internal/integration
cd frontend && npx tsc --noEmit && npm run build
cd frontend && npx playwright install chromium && npm run e2e   # needs the stack above; writes docs/screenshots/
```

`internal/integration` mounts the real routes on a fresh database per test (migrations
0001-0006), refuses a non-local server, and checks that user B gets 404 on every one
of user A's flags, experiments, SDK keys, audit log, members and invites, with none of
A's data in any response B receives.

## 4. What the roles can do

| Role | Flags and experiments | Members, invites, SDK keys, rename |
| --- | --- | --- |
| viewer | read | nothing (members list is visible) |
| editor | create and edit outside production, kill anywhere | nothing |
| admin | everything, including production | manage everyone except owners |
| owner | everything | everything, incl. making owners; the last owner can't leave |

A user can belong to several workspaces and owns at most 5. A workspace has at most 3
environments, 50 flags, 10 active SDK keys, and 20 members plus open invites.

## 5. Invites

Helios does not send email. An admin creates an invite and gets a one-time link
(`/invite/<token>`) to share; it works once, only for the invited address, for 7 days.
The invitee can also accept from the banner shown in the console when they sign in with
that address.
