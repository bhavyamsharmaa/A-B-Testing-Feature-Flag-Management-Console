#!/usr/bin/env bash
# Builds the database release 1 ships with, in an EMPTY database, running each migration's verify script right after it:
# 0001-0004, 0006, 0007. NOT 0005: that is a later gate (docs/ROLLOUT.md, G6b).
#
#   PGHOST=127.0.0.1 PGPORT=55432 PGUSER=helios PGDATABASE=<empty db> backend/scripts/apply_release1.sh [--with-0005]
#
# For CI and local use only: it takes the connection from the PG* variables and refuses any host but this machine.
# 0001 expects Supabase's auth.users; it creates a stand-in itself on a plain Postgres.
set -euo pipefail
case "${PGHOST:-}" in 127.0.0.1|localhost) ;; *) echo "apply_release1: set PGHOST to this machine (127.0.0.1); refusing '${PGHOST:-unset}'" >&2; exit 2 ;; esac
: "${PGDATABASE:?set PGDATABASE to an empty local database}"
unset PGSERVICE PGSERVICEFILE PGHOSTADDR
DB_DIR="$(cd "$(dirname "$0")/../db" && pwd)"
MIGRATIONS=(0001_core 0002_user_environment_roles 0003_experiments 0004_workspaces 0006_workspace_roles_invites 0007_workspace_keeps_an_owner)
case "${1:-}" in
  "") ;;
  --with-0005) MIGRATIONS+=(0005_drop_workspace_defaults) ;;
  *) echo "usage: $0 [--with-0005]" >&2; exit 2 ;;
esac
for m in "${MIGRATIONS[@]}"; do
  psql -X -q -v ON_ERROR_STOP=1 -f "$DB_DIR/migrations/$m.sql" >/dev/null
  n="${m%%_*}"
  if [ -f "$DB_DIR/verify_$n.sql" ]; then
    out="$(psql -X -At -v ON_ERROR_STOP=1 -f "$DB_DIR/verify_$n.sql")"
    if grep -q '|FAIL' <<<"$out" || ! grep -q 'SUMMARY' <<<"$out"; then echo "$out"; echo "apply_release1: verify_$n failed" >&2; exit 1; fi
    echo "applied $m; $(grep -o 'SUMMARY: [^|]*' <<<"$out")"
  else
    echo "applied $m"
  fi
done
