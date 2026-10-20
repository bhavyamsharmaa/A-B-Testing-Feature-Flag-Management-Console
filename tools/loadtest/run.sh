#!/usr/bin/env bash
# Starts a SEPARATE API instance for load tests (own database, own auth stand-in, rate limits off, pprof on) and runs
# the load generator against it. Everything is loopback; state lives in ${HELIOS_LOCAL_DIR:-$HOME/helios-local}
# (outside the repository, which this script enforces).
#
#   tools/loadtest/run.sh up            # database helios_load (rebuilt empty), devauth :54322, API :8081, pprof :6061
#   tools/loadtest/run.sh seed [flags]  # create the workspace, flags, segments, experiments and an SDK key
#   tools/loadtest/run.sh all  [flags]  # evaluate, track, stream, mixed, one table (flags: -duration 20s -c 32 ...)
#   tools/loadtest/run.sh run -scenario evaluate [flags]
#   tools/loadtest/run.sh down
#
# `up` builds the API from the current tree (go build into $HELIOS_LOCAL_DIR/bin/load-api), so the numbers are for
# the code you have checked out. Needs the local Postgres and Redis (backend/scripts/local_stack.sh start).
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd -P)"
BASE="${HELIOS_LOCAL_DIR:-$HOME/helios-local}"
parent="$(cd "$(dirname "$BASE")" && pwd -P)"; BASE="$parent/$(basename "$BASE")"
case "$BASE/" in "$REPO"/*) echo "loadtest: $BASE is inside the repository; refusing" >&2; exit 2 ;; esac
RUN="$BASE/run"; LOG="$BASE/log"; BIN="$BASE/bin"; mkdir -p "$RUN" "$LOG" "$BIN"
DB=helios_load; PG="postgres://helios@127.0.0.1:55432"; API=127.0.0.1:8081; AUTH=127.0.0.1:54322; PPROF=127.0.0.1:6061
psqlb() { if command -v psql >/dev/null; then command -v psql; else echo /opt/homebrew/opt/postgresql@16/bin/psql; fi; }
PSQL="$(psqlb)"

alive() { [ -f "$RUN/$1.pid" ] && kill -0 "$(cat "$RUN/$1.pid")" 2>/dev/null; }
stop_one() {
  local n="${1:?}"
  if alive "$n"; then kill "$(cat "$RUN/$n.pid")" 2>/dev/null || true; for _ in $(seq 1 40); do alive "$n" || break; sleep 0.25; done; fi
  rm -f "${RUN:?}/${n:?}.pid"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  up)
    stop_one load-api; stop_one load-devauth
    "$PSQL" -h 127.0.0.1 -p 55432 -U helios -d postgres -qAt -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
    PGHOST=127.0.0.1 PGPORT=55432 PGUSER=helios PGDATABASE=$DB "$REPO/backend/scripts/apply_migrations.sh" >/dev/null
    (cd "$REPO/backend" && go build -o "$BIN/load-api" ./cmd/api && go build -o "$BIN/load-devauth" ./cmd/devauth)
    export DATABASE_URL="$PG/$DB?sslmode=disable"
    nohup "$BIN/load-devauth" -addr "$AUTH" -public-url "http://$AUTH" >> "$LOG/load-devauth.log" 2>&1 & echo $! > "$RUN/load-devauth.pid"
    sleep 1
    # LOADTEST_API_ENV adds settings to the API under test, e.g. LOADTEST_API_ENV="CONFIG_CACHE_TTL=off"
    # shellcheck disable=SC2086
    SUPABASE_URL="http://$AUTH" REDIS_URL="redis://127.0.0.1:56379/1" RATE_LIMIT_DISABLED=true STREAMS_PER_KEY=100000 \
      LISTEN_ADDR="$API" PPROF_ADDR="$PPROF" HELIOS_ENV=development \
      nohup env ${LOADTEST_API_ENV:-} "$BIN/load-api" >> "$LOG/load-api.log" 2>&1 & echo $! > "$RUN/load-api.pid"
    for _ in $(seq 1 60); do curl -fsS -o /dev/null "http://$API/healthz" 2>/dev/null && break; sleep 0.5; done
    curl -fsS -o /dev/null "http://$API/healthz" || { echo "load API did not start; see $LOG/load-api.log" >&2; exit 1; }
    echo "load API up on http://$API (pid $(cat "$RUN/load-api.pid")), pprof http://$PPROF, database $DB"
    ;;
  down) stop_one load-api; stop_one load-devauth; echo "stopped" ;;
  seed|all|run)
    cd "$REPO/tools/loadtest"
    export LOADTEST_DATABASE_URL="$PG/$DB?sslmode=disable"
    exec go run . "$cmd" -api "http://$API" -auth "http://$AUTH" -pprof "http://$PPROF" "$@"
    ;;
  *) echo "usage: $0 {up|seed|all|run|down} [flags]" >&2; exit 2 ;;
esac
