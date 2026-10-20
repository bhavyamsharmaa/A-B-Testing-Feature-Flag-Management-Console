#!/usr/bin/env bash
# The whole local stack in one script: Postgres, Redis, the auth stand-in (devauth), the API and the console dev
# server. (The background jobs are NOT started: run them by hand, see docs/OPERATIONS.md.) Everything is bound to
# 127.0.0.1, every step is idempotent, and nothing here is a secret: the few local-only secrets are generated on
# first use into a file OUTSIDE the repository.
#
#   backend/scripts/local_stack.sh start  [db]   # postgres, redis, migrate (if empty), build, devauth, api, console
#   backend/scripts/local_stack.sh stop          # devauth, api, console (postgres and redis keep running)
#   backend/scripts/local_stack.sh stop --all    # ... and postgres and redis
#   backend/scripts/local_stack.sh restart [db]
#   backend/scripts/local_stack.sh reset  [db]   # drop and recreate the database, migrate, restart the processes
#   backend/scripts/local_stack.sh migrate [db]  # create the database if missing; apply every migration only to an EMPTY one
#   backend/scripts/local_stack.sh api           # run the API in the foreground (go run), loopback only
#   backend/scripts/local_stack.sh web           # run the console dev server in the foreground
#   backend/scripts/local_stack.sh status
#   backend/scripts/local_stack.sh env [db]      # print the exports the Go and Playwright tests expect (includes the local secrets)
#
# [db] defaults to helios_dev ($HELIOS_DB). State lives in ${HELIOS_LOCAL_DIR:-$HOME/helios-local}: pg/ (the
# database), redis/, bin/ (built binaries), run/ (pid files), log/ and secrets.env (mode 600). The script REFUSES a
# directory inside this repository, so data, logs and secrets can never be committed. Deleting the directory is
# always safe: the next `start` recreates everything.
#
# It never connects to anything but 127.0.0.1 and refuses any other host in HELIOS_PG_HOST / HELIOS_REDIS_HOST.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd -P)"
BASE="${HELIOS_LOCAL_DIR:-$HOME/helios-local}"
PG_HOST="${HELIOS_PG_HOST:-127.0.0.1}"
PG_PORT="${HELIOS_PG_PORT:-55432}"
REDIS_HOST="${HELIOS_REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${HELIOS_REDIS_PORT:-56379}"
AUTH_PORT="${HELIOS_AUTH_PORT:-54321}"
API_ADDR="${LISTEN_ADDR:-127.0.0.1:8080}"
WEB_PORT="${HELIOS_WEB_PORT:-5173}"

die() { echo "local_stack: $*" >&2; exit 2; }
for h in "$PG_HOST" "$REDIS_HOST"; do
  case "$h" in 127.0.0.1|localhost) ;; *) die "host '$h' is not this machine; refusing" ;; esac
done
case "${API_ADDR%:*}" in 127.*|localhost|"[::1]") ;; *) die "LISTEN_ADDR must be loopback, got '$API_ADDR'" ;; esac

# Resolve the (possibly not yet existing) directory WITHOUT creating anything, and refuse it before any mkdir.
parent="$(cd "$(dirname "$BASE")" 2>/dev/null && pwd -P)" || die "the parent of $BASE does not exist"
BASE="$parent/$(basename "$BASE")"
case "$BASE/" in
  "$REPO"/*) die "$BASE is inside the repository ($REPO); data, logs and secrets must live outside it" ;;
esac
mkdir -p "$BASE"
RUN="$BASE/run"; LOG="$BASE/log"; BIN="$BASE/bin"; SECRETS="$BASE/secrets.env"
mkdir -p "$RUN" "$LOG" "$BIN" "$BASE/redis"

pg_bin() {
  if command -v psql >/dev/null 2>&1; then dirname "$(command -v psql)"; return; fi
  for d in /opt/homebrew/opt/postgresql@16/bin /usr/local/opt/postgresql@16/bin /usr/lib/postgresql/16/bin; do
    [ -x "$d/psql" ] && { echo "$d"; return; }
  done
  die "Postgres client tools not found (brew install postgresql@16)"
}
PGB="$(pg_bin)"
psql_admin() { "$PGB/psql" -h "$PG_HOST" -p "$PG_PORT" -U helios -d postgres -v ON_ERROR_STOP=1 -qAt "$@"; }

# ---- local-only secrets, generated once, outside the repo --------------------------------------------------
ensure_secrets() {
  if [ ! -f "$SECRETS" ]; then
    ( umask 077
      {
        echo "WEBHOOK_SECRET_KEY=$(openssl rand -base64 32)"
        echo "OUTBOX_SECRET=$(openssl rand -hex 24)"
        echo "PURGE_SECRET=$(openssl rand -hex 24)"
        echo "SCHEDULER_SECRET=$(openssl rand -hex 24)"
        echo "EVENT_PURGE_SECRET=$(openssl rand -hex 24)"
      } > "$SECRETS" )
    echo "created $SECRETS (local-only secrets, mode 600)"
  fi
  chmod 600 "$SECRETS"
  set -a; . "$SECRETS"; set +a
}

dbname() { local d="${1:-${HELIOS_DB:-helios_dev}}"; [[ "$d" =~ ^[a-z][a-z0-9_]{0,40}$ ]] || die "bad database name '$d'"; echo "$d"; }
db_url() { echo "postgres://helios@$PG_HOST:$PG_PORT/$1?sslmode=disable"; }

# ---- postgres, redis ---------------------------------------------------------------------------------------
ensure_pg() { HELIOS_LOCAL_DIR="$BASE" HELIOS_PG_PORT="$PG_PORT" "$REPO/backend/scripts/local_pg.sh" ensure >/dev/null; }

redis_up() { (exec 3<>"/dev/tcp/$REDIS_HOST/$REDIS_PORT") 2>/dev/null; }

ensure_redis() {
  redis_up && return 0
  command -v redis-server >/dev/null || die "redis-server not found (brew install redis)"
  redis-server --bind "$REDIS_HOST" --port "$REDIS_PORT" --dir "$BASE/redis" --save "" --appendonly no \
    --daemonize yes --pidfile "$RUN/redis.pid" --logfile "$LOG/redis.log" >/dev/null
  for _ in $(seq 1 20); do
    redis_up && { echo "redis started on $REDIS_HOST:$REDIS_PORT"; return 0; }
    sleep 0.25
  done
  die "redis did not start; see $LOG/redis.log"
}

# ---- database ----------------------------------------------------------------------------------------------
db_exists() { [ "$(psql_admin -c "SELECT 1 FROM pg_database WHERE datname = '$1'")" = "1" ]; }

do_migrate() {
  local db="$1"
  db_exists "$db" || { psql_admin -c "CREATE DATABASE $db" >/dev/null; echo "created database $db"; }
  if [ "$("$PGB/psql" -h "$PG_HOST" -p "$PG_PORT" -U helios -d "$db" -qAt -c "SELECT to_regclass('public.workspaces') IS NOT NULL")" = "t" ]; then
    echo "database $db is already migrated (use 'reset' to rebuild it)"; return 0
  fi
  PGHOST="$PG_HOST" PGPORT="$PG_PORT" PGUSER=helios PGDATABASE="$db" "$REPO/backend/scripts/apply_migrations.sh" >/dev/null
  echo "applied every migration to $db"
}

do_reset() {
  local db="$1"
  stop_processes
  psql_admin -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
  do_migrate "$db"
}

# ---- processes ---------------------------------------------------------------------------------------------
alive() { [ -f "$RUN/$1.pid" ] && kill -0 "$(cat "$RUN/$1.pid")" 2>/dev/null; }

stop_one() {
  local n="${1:?}"
  if alive "$n"; then
    kill "$(cat "$RUN/$n.pid")" 2>/dev/null || true
    for _ in $(seq 1 40); do alive "$n" || break; sleep 0.25; done
    if alive "$n"; then kill -9 "$(cat "$RUN/$n.pid")" 2>/dev/null || true; fi
    echo "stopped $n"
  fi
  rm -f "${RUN:?}/${n:?}.pid"
}
stop_processes() { stop_one web; stop_one api; stop_one devauth; }

start_one() { # name, command...
  local n="${1:?}"; shift
  if alive "$n"; then echo "$n already running (pid $(cat "$RUN/$n.pid"))"; return 0; fi
  nohup "$@" >> "$LOG/$n.log" 2>&1 &
  echo $! > "$RUN/$n.pid"
}

wait_http() { # url, name
  for _ in $(seq 1 60); do curl -fsS -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.5; done
  die "$2 did not answer on $1; see $LOG/"
}

do_build() {
  ( cd "$REPO/backend" && for c in api devauth outbox scheduler purgeworkspaces purgeevents; do
      if [ -d "cmd/$c" ]; then go build -o "$BIN/$c" "./cmd/$c"; fi
    done )
  echo "built binaries in $BIN"
}

ensure_web_env() {
  local f="$REPO/frontend/.env.local" # git-ignored, holds no secret
  if [ ! -f "$f" ]; then
    printf '%s\n' "VITE_API_BASE_URL=http://localhost:${API_ADDR##*:}" "VITE_SUPABASE_URL=http://127.0.0.1:$AUTH_PORT" \
      "VITE_SUPABASE_PUBLISHABLE_KEY=local-dev-not-a-secret" > "$f"
    echo "created frontend/.env.local"
  fi
}

api_env() { # the environment the API runs with; the sourced secrets are local-only
  export DATABASE_URL SUPABASE_URL REDIS_URL CORS_ALLOWED_ORIGINS PUBLIC_CONSOLE_URL LISTEN_ADDR WEBHOOK_ALLOW_INSECURE_LOCAL HELIOS_ENV
  DATABASE_URL="$(db_url "$1")"; SUPABASE_URL="http://127.0.0.1:$AUTH_PORT"; REDIS_URL="redis://$REDIS_HOST:$REDIS_PORT/0"
  CORS_ALLOWED_ORIGINS="http://127.0.0.1:$WEB_PORT"; PUBLIC_CONSOLE_URL="http://127.0.0.1:$WEB_PORT"; LISTEN_ADDR="$API_ADDR"
  WEBHOOK_ALLOW_INSECURE_LOCAL=true; HELIOS_ENV=development
}

do_start() {
  local db="$1"
  ensure_secrets; ensure_pg; ensure_redis; do_migrate "$db"; do_build
  api_env "$db"
  start_one devauth "$BIN/devauth"
  wait_http "http://127.0.0.1:$AUTH_PORT/dev/health" devauth
  start_one api "$BIN/api"
  wait_http "http://$API_ADDR/healthz" api
  if [ -d "$REPO/frontend/node_modules" ]; then
    ensure_web_env
    if (exec 3<>"/dev/tcp/127.0.0.1/$WEB_PORT") 2>/dev/null; then echo "console already answering on :$WEB_PORT"
    else
      ( cd "$REPO/frontend" && start_one web npx vite --host 127.0.0.1 --port "$WEB_PORT" --strictPort )
      wait_http "http://127.0.0.1:$WEB_PORT/" console
    fi
  else
    echo "frontend/node_modules missing: run 'npm install' in frontend/ to get the console"
  fi
  echo "up: api http://$API_ADDR  auth :$AUTH_PORT  console http://127.0.0.1:$WEB_PORT  (database $db, logs $LOG)"
}

cmd="${1:-status}"; shift || true
case "$cmd" in
  start)    do_start "$(dbname "${1:-}")" ;;
  stop)     stop_processes
            if [ "${1:-}" = "--all" ]; then
              HELIOS_LOCAL_DIR="$BASE" "$REPO/backend/scripts/local_pg.sh" stop
              if [ -f "$RUN/redis.pid" ]; then kill "$(cat "$RUN/redis.pid")" 2>/dev/null || true; rm -f "${RUN:?}/redis.pid"; echo "stopped redis"; fi
            fi ;;
  restart)  db="$(dbname "${1:-}")"; stop_processes; do_start "$db" ;;
  migrate)  ensure_pg; do_migrate "$(dbname "${1:-}")" ;;
  reset)    ensure_secrets; ensure_pg; ensure_redis; db="$(dbname "${1:-}")"; do_reset "$db"; do_start "$db" ;;
  api)      ensure_secrets; ensure_pg; ensure_redis; db="$(dbname "${1:-}")"; do_migrate "$db"; api_env "$db"
            cd "$REPO/backend" && exec go run ./cmd/api ;;
  web)      ensure_web_env; cd "$REPO/frontend" && exec npx vite --host 127.0.0.1 --port "$WEB_PORT" --strictPort ;;
  env)      db="$(dbname "${1:-}")"
            echo "export HELIOS_TEST_DATABASE_URL='postgres://helios@$PG_HOST:$PG_PORT/postgres?sslmode=disable'"
            echo "export HELIOS_TEST_REDIS_URL='redis://$REDIS_HOST:$REDIS_PORT/0'"
            echo "export DATABASE_URL='$(db_url "$db")'"
            ensure_secrets # the Playwright specs that call the guarded job endpoints need the local secrets
            echo "export HELIOS_E2E_OUTBOX_SECRET='$OUTBOX_SECRET' HELIOS_E2E_PURGE_SECRET='$PURGE_SECRET'" ;;
  status)   for n in devauth api web; do if alive "$n"; then echo "$n: running (pid $(cat "$RUN/$n.pid"))"; else echo "$n: not running"; fi; done
            "$REPO/backend/scripts/local_pg.sh" status || true
            if redis_up; then echo "redis: answering on $REDIS_HOST:$REDIS_PORT"; else echo "redis: not running"; fi ;;
  *) die "usage: $0 {start|stop [--all]|restart|reset|migrate|api|web|status|env} [db]" ;;
esac
