#!/usr/bin/env bash
# The throwaway local Postgres every local test and rehearsal uses: Postgres 16 on
# 127.0.0.1:55432, user "helios", trust authentication, reachable only from this machine.
#
#   backend/scripts/local_pg.sh ensure   # create it if missing, start it if stopped (idempotent)
#   backend/scripts/local_pg.sh status
#   backend/scripts/local_pg.sh stop
#   backend/scripts/local_pg.sh path     # print the data directory
#
# The data lives OUTSIDE the repository and outside /tmp (which is cleared on restart):
#   ${HELIOS_LOCAL_DIR:-$HOME/helios-local}/pg      log: .../pg.log
# Set HELIOS_LOCAL_DIR to put it elsewhere. It refuses a location inside this repository, so
# a database can never be committed. Deleting the directory is always safe: `ensure` recreates
# it (empty); the tests make their own databases.
set -euo pipefail

PORT="${HELIOS_PG_PORT:-55432}"
BASE="${HELIOS_LOCAL_DIR:-$HOME/helios-local}"
DATA="$BASE/pg"
LOG="$BASE/pg.log"
REPO="$(cd "$(dirname "$0")/../.." && pwd -P)"

# Postgres 16 binaries: PATH first, then Homebrew's keg.
find_bin() {
  if command -v pg_ctl >/dev/null 2>&1; then dirname "$(command -v pg_ctl)"; return; fi
  for d in /opt/homebrew/opt/postgresql@16/bin /usr/local/opt/postgresql@16/bin /usr/lib/postgresql/16/bin; do
    [ -x "$d/pg_ctl" ] && { echo "$d"; return; }
  done
  echo "local_pg: Postgres 16 not found (brew install postgresql@16)" >&2; exit 2
}
BIN="$(find_bin)"

mkdir -p "$BASE"
case "$(cd "$BASE" && pwd -P)/" in
  "$REPO"/*) echo "local_pg: $BASE is inside the repository ($REPO); refusing" >&2; exit 2 ;;
esac

running() { "$BIN/pg_ctl" -D "$DATA" status >/dev/null 2>&1; }

cmd="${1:-ensure}"
case "$cmd" in
  path) echo "$DATA" ;;
  status)
    if running; then echo "running: $DATA on 127.0.0.1:$PORT"; else echo "not running ($DATA)"; exit 1; fi ;;
  stop)
    running && "$BIN/pg_ctl" -D "$DATA" -m fast stop >/dev/null && echo "stopped" || echo "not running" ;;
  ensure)
    if [ ! -f "$DATA/PG_VERSION" ]; then
      # a half-made or damaged directory is removed first: this database holds nothing worth keeping
      rm -rf "$DATA"
      "$BIN/initdb" -D "$DATA" -U helios --auth=trust -E UTF8 >/dev/null
      chmod 700 "$DATA"
      echo "created $DATA"
    fi
    if ! running; then
      rm -f "$DATA/postmaster.pid"   # left behind by a crash or a restart; pg_ctl said it is not running
      "$BIN/pg_ctl" -D "$DATA" -o "-p $PORT -c unix_socket_directories= -c listen_addresses=127.0.0.1" -l "$LOG" -w start >/dev/null
      echo "started on 127.0.0.1:$PORT"
    fi
    for _ in $(seq 1 30); do
      "$BIN/pg_isready" -h 127.0.0.1 -p "$PORT" -q && { echo "ready: $DATA on 127.0.0.1:$PORT"; exit 0; }
      sleep 0.5
    done
    echo "local_pg: not accepting connections; see $LOG" >&2; exit 1 ;;
  *) echo "usage: $0 {ensure|status|stop|path}" >&2; exit 2 ;;
esac
