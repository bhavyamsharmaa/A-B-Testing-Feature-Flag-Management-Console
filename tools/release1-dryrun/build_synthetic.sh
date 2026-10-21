#!/usr/bin/env bash
# Builds the SYNTHETIC "old production" (see synth_old_prod.sql) in the local throwaway Postgres, from the migrations of the
# commit given (default 6c90844, the state of production before release 1). Local only; nothing leaves this machine.
#   tools/release1-dryrun/build_synthetic.sh <MIGRATIONS_DIR_OF_MAIN> <DATABASE_NAME>
set -euo pipefail
MIG="${1:?directory with 0001..0003 migrations of main}"; DB="${2:?database name}"
case "$DB" in ""|*[!a-z0-9_]*) echo "bad database name" >&2; exit 2;; esac
PB=/opt/homebrew/opt/postgresql@16/bin; P=(-h 127.0.0.1 -p 55432 -U helios)
"$PB/dropdb" "${P[@]}" --if-exists --force "$DB"; "$PB/createdb" "${P[@]}" "$DB"
for f in 0001_core 0002_user_environment_roles 0003_experiments; do
  "$PB/psql" "${P[@]}" -d "$DB" -v ON_ERROR_STOP=1 -q -f "$MIG/$f.sql"
done
"$PB/psql" "${P[@]}" -d "$DB" -v ON_ERROR_STOP=1 -q -f "$(dirname "$0")/synth_old_prod.sql"
"$PB/psql" "${P[@]}" -d "$DB" -At -c "select 'rows', (select count(*) from audit_logs)||' audit, '||(select count(*) from events)||' events, '||(select count(*) from flag_configs)||' flag_configs'"
