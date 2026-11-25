#!/usr/bin/env bash
# The runbook's Step 3, as a script: restore a dump of the public schema into a fresh LOCAL database, with a stand-in
# auth.users filled with the ids the data refers to. Local only (127.0.0.1:55432); it refuses anything else.
#   tools/release1-dryrun/restore_copy.sh <DUMP_FILE> <NEW_DATABASE_NAME>
set -euo pipefail
DUMP="${1:?dump file}"; DB="${2:?database name}"
case "$DB" in ""|*[!a-z0-9_]*) echo "bad database name" >&2; exit 2;; esac
PB=/opt/homebrew/opt/postgresql@16/bin
ADMIN="postgres://helios@127.0.0.1:55432/postgres"; LOCAL="postgres://helios@127.0.0.1:55432/$DB"
"$PB/psql" "$ADMIN" -qAt -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
"$PB/psql" "$LOCAL" -q -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA IF NOT EXISTS auth; CREATE TABLE IF NOT EXISTS auth.users (id uuid primary key default gen_random_uuid(), email text unique)"
"$PB/pg_restore" --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=pre-data "$DUMP"
"$PB/pg_restore" --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=data "$DUMP"
"$PB/psql" "$LOCAL" -q -c "INSERT INTO auth.users (id, email) SELECT u, u||'@rehearsal.invalid' FROM (SELECT created_by u FROM flags UNION SELECT created_by FROM api_keys UNION SELECT created_by FROM experiments UNION SELECT user_id FROM user_environment_roles) x WHERE u IS NOT NULL ON CONFLICT DO NOTHING"
"$PB/pg_restore" --dbname="$LOCAL" --no-owner --no-privileges --exit-on-error --section=post-data "$DUMP"
echo "restored into $DB"
