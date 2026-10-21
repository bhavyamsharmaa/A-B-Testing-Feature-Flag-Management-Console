#!/usr/bin/env bash
# restore the dump, set up the synthetic logins, apply 0004, 0006, 0007 (the runbook's steps 3 and 6) -> a migrated local copy.
#   prep_migrated.sh <DUMP> <DB> <RELEASE1_CHECKOUT>
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; DUMP="$1"; DB="$2"; REL="$3"; PSQL=/opt/homebrew/opt/postgresql@16/bin/psql; URL="postgres://helios@127.0.0.1:55432/$DB"
"$HERE/restore_copy.sh" "$DUMP" "$DB" >/dev/null
"$PSQL" "$URL" -q -f "$HERE/synthetic_logins.sql"
for m in 0004_workspaces 0006_workspace_roles_invites 0007_workspace_keeps_an_owner; do "$PSQL" "$URL" -q -v ON_ERROR_STOP=1 -f "$REL/backend/db/migrations/$m.sql" >/dev/null; done
echo "migrated copy ready: $DB"
