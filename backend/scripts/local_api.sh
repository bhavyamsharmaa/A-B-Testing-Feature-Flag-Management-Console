#!/bin/bash
# Run the API for local development, bound to loopback so the dev server is not reachable from the network.
#
#   backend/scripts/local_api.sh                    # 127.0.0.1:8080
#   LISTEN_ADDR=127.0.0.1:9000 backend/scripts/local_api.sh
#
# It sets LISTEN_ADDR (which wins over PORT; see docs/LOCAL_MULTITENANCY.md) and REFUSES an address that is not
# loopback. Everything else (DATABASE_URL, SUPABASE_URL, ...) comes from the environment as for `go run ./cmd/api`.
set -euo pipefail
addr="${LISTEN_ADDR:-127.0.0.1:8080}"
host="${addr%:*}"
case "$host" in
  127.*|localhost|"[::1]") ;;
  *) echo "local_api.sh: LISTEN_ADDR must be a loopback address (127.x.x.x, localhost or [::1]), got '$addr'. A development server must not be exposed on the network." >&2; exit 2 ;;
esac
cd "$(dirname "$0")/.."
export LISTEN_ADDR="$addr"
exec go run ./cmd/api
