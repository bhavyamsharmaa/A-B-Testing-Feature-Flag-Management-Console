#!/usr/bin/env bash
# Back up the public schema (data included) in pg_dump's custom format.
#
#   PGSERVICE=helios_prod backend/scripts/backup_db.sh
#
# The connection comes from a pg_service.conf entry (host, port, dbname, user,
# sslmode) and ~/.pgpass (the password), both set up by the operator. This
# script takes NO connection URL: not as an argument, not from DATABASE_URL, so
# no credential ever appears on a command line, in `ps`, in a file it writes or
# in its output. It unsets DATABASE_URL and the PG* connection variables so they cannot
# override the service.
#
# The dump goes to backend/backups/helios-<UTC timestamp>.dump, a directory
# that is git-ignored. Treat the file as a secret: it contains your data.
#
# RESTORE into a throwaway LOCAL database only (never over production): follow
# docs/RELEASE1_RUNBOOK.md Step 3, or run tools/release1-dryrun/restore_copy.sh.
# A single pg_restore into an empty database fails: the dump creates the public
# schema itself, and its foreign keys need auth.users rows first.
#
# List what a dump contains without restoring:
#   pg_restore --list backend/backups/helios-<timestamp>.dump
#
# Only the `public` schema is dumped. Supabase's auth.users is NOT included,
# so a restore needs matching users (the runbook's Step 3 makes them). Use a
# pg_dump whose major version is >= the server's.
# It writes only the dump: row counts are a manual query (runbook Step 1).

set -euo pipefail
set +x

: "${PGSERVICE:?set PGSERVICE to the name of the pg_service.conf entry}"
unset DATABASE_URL PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE
command -v pg_dump >/dev/null || { echo "pg_dump not found; install the PostgreSQL client tools" >&2; exit 2; }

dir="$(cd "$(dirname "$0")/.." && pwd)/backups"
mkdir -p "$dir"
chmod 700 "$dir"
out="$dir/helios-$(date -u +%Y%m%dT%H%M%SZ).dump"

PGCONNECT_TIMEOUT=15 pg_dump \
  --dbname "service=$PGSERVICE" \
  --schema=public \
  --format=custom \
  --no-owner \
  --no-privileges \
  --file "$out"

chmod 600 "$out"
echo "wrote $out ($(wc -c <"$out" | tr -d ' ') bytes)"
