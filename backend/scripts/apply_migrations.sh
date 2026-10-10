#!/usr/bin/env bash
# Applies every migration in backend/db/migrations (sorted, each in its own
# transaction) and then every backend/db/verify_*.sql to ONE database, the way the
# local rehearsal does, and fails if any verification row says FAIL.
#
# The connection comes from the standard PG* environment variables, e.g. in CI:
#   PGHOST=127.0.0.1 PGPORT=5432 PGUSER=helios PGDATABASE=helios_ci backend/scripts/apply_migrations.sh
#
# It refuses any host that is not this machine, so it can never be pointed at a
# hosted database by mistake. The database must already exist and be empty.
set -euo pipefail

case "${PGHOST:-}" in
  127.0.0.1|localhost|::1) ;;
  *) echo "apply_migrations: PGHOST must be 127.0.0.1 or localhost (got '${PGHOST:-unset}'); refusing" >&2; exit 2 ;;
esac
: "${PGDATABASE:?set PGDATABASE}"
: "${PGUSER:?set PGUSER}"

cd "$(dirname "$0")/.."

# Each verify script describes the database right after ITS migration (verify_0004
# expects 0005's defaults still present, verify_0003 expects 0008 not applied yet),
# so it is run immediately after that migration, not at the end.
failed=0
for f in db/migrations/*.sql; do
  n=$(basename "$f" | cut -c1-4)
  echo "migrate  $(basename "$f")"
  psql -v ON_ERROR_STOP=1 -q -f "$f"
  v="db/verify_${n}.sql"
  if [ -f "$v" ]; then
    out=$(psql -v ON_ERROR_STOP=1 -At -F '|' -f "$v")
    fails=$(printf '%s\n' "$out" | grep -c '|FAIL$' || true)
    total=$(printf '%s\n' "$out" | grep -cE '\|(PASS|FAIL)$' || true)
    echo "verify   $(basename "$v"): $((total - fails)) of $total rows passed"
    if [ "$fails" -ne 0 ]; then
      printf '%s\n' "$out" | grep '|FAIL$' >&2
      failed=1
    fi
  fi
done
exit $failed
