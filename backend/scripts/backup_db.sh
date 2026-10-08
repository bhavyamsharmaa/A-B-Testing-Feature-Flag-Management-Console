#!/usr/bin/env bash
# Back up the public schema (data included) in pg_dump's custom format.
#
#   DATABASE_URL='postgres://...' backend/scripts/backup_db.sh
#
# DATABASE_URL is read from the environment only: this script never prints it,
# never writes it to a file and never takes it as an argument. One caveat:
# pg_dump has no environment variable for a connection URI, so the URL is
# passed to pg_dump as its --dbname argument and is visible to other
# processes of the same OS user (`ps`) for the few seconds the dump runs. Run
# it on a machine you trust.
#
# The dump goes to backend/backups/helios-<UTC timestamp>.dump, a directory
# that is git-ignored. Treat the file as a secret: it contains your data.
#
# RESTORE (into an EMPTY database, e.g. a second Supabase project; --clean
# would drop objects first, so avoid it on a database you care about):
#
#   pg_restore --no-owner --no-privileges --exit-on-error \
#     --dbname "$TARGET_DATABASE_URL" backend/backups/helios-<timestamp>.dump
#
# List what a dump contains without restoring:
#   pg_restore --list backend/backups/helios-<timestamp>.dump
#
# Only the `public` schema is dumped. Supabase's auth.users is NOT included,
# so a restore needs matching users (the rehearsal creates them; see
# backend/db/rehearsal.md). Use pg_dump from a version >= your server's.

set -euo pipefail
set +x

: "${DATABASE_URL:?set DATABASE_URL to the Postgres connection string}"
command -v pg_dump >/dev/null || { echo "pg_dump not found; install the PostgreSQL client tools" >&2; exit 2; }

dir="$(cd "$(dirname "$0")/.." && pwd)/backups"
mkdir -p "$dir"
chmod 700 "$dir"
out="$dir/helios-$(date -u +%Y%m%dT%H%M%SZ).dump"

PGCONNECT_TIMEOUT=15 pg_dump \
  --dbname "$DATABASE_URL" \
  --schema=public \
  --format=custom \
  --no-owner \
  --no-privileges \
  --file "$out"

chmod 600 "$out"
echo "wrote $out ($(wc -c <"$out" | tr -d ' ') bytes)"
