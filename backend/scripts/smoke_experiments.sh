#!/usr/bin/env bash
# Smoke test for the experiments API, run against a live backend after
# migration 0003 is applied. It creates a throwaway flag and experiment,
# exercises start/stop and the delete guard, then removes the flag.
#
#   API_BASE=http://localhost:8080 ENV_NAME=dev TOKEN=... backend/scripts/smoke_experiments.sh
#
# Needs: bash, curl. ENV_NAME is an environment key ("dev"); it is looked up in
# the caller's active workspace via GET /me. The caller must be an owner or
# admin of that workspace (flag deletion and experiment stop need it).
# The token is read from the environment only, is passed to curl through a
# config file descriptor (so it never appears in the process list), and is
# never printed.

set -u
set +x

: "${API_BASE:?set API_BASE, e.g. http://localhost:8080}"
: "${ENV_NAME:?set ENV_NAME, e.g. dev}"
: "${TOKEN:?set TOKEN to a Supabase access token}"
API_BASE="${API_BASE%/}"

FLAG_KEY="smoke-exp-flag"
EXP_KEY="smoke-exp"
EXP2_KEY="smoke-exp-2"

if [ "$ENV_NAME" = "production" ] && [ "${ALLOW_PRODUCTION:-}" != "1" ]; then
  echo "Refusing to run against production. Set ALLOW_PRODUCTION=1 to override." >&2
  exit 2
fi

BODY_FILE="$(mktemp)"
FAILURES=0
CREATED_FLAG=0
EXP_STARTED=0

# Runs curl with the bearer token supplied via process substitution.
api() { # METHOD PATH [JSON_BODY]  -> sets STATUS and BODY
  local method="$1" path="$2" data="${3:-}"
  local args=(-sS -o "$BODY_FILE" -w '%{http_code}' -X "$method" --config <(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN"))
  if [ -n "$data" ]; then args+=(-H 'Content-Type: application/json' --data "$data"); fi
  STATUS="$(curl "${args[@]}" "$API_BASE$path" 2>/dev/null)" || STATUS="000"
  BODY="$(cat "$BODY_FILE")"
}

code_of() { printf '%s' "$BODY" | grep -o '"code":"[A-Z_]*"' | head -1 | sed 's/"code":"\(.*\)"/\1/'; }

# check NAME EXPECTED_STATUS [EXPECTED_CODE_OR_BODY_SUBSTRING]
check() {
  local name="$1" want_status="$2" want_code="${3:-}" ok=1 detail=""
  [ "$STATUS" = "$want_status" ] || ok=0
  if [ -n "$want_code" ]; then
    case "$want_code" in
      [A-Z_]*[A-Z]) [ "$(code_of)" = "$want_code" ] || ok=0 ;;                 # error code
      *)            printf '%s' "$BODY" | grep -q "$want_code" || ok=0 ;;      # body substring
    esac
  fi
  if [ "$ok" = 1 ]; then
    echo "PASS  $name"
  else
    FAILURES=$((FAILURES + 1))
    detail="got HTTP $STATUS"
    [ -n "$(code_of)" ] && detail="$detail, code $(code_of)"
    echo "FAIL  $name (expected HTTP $want_status${want_code:+ / $want_code}; $detail)"
  fi
}

# Only removes what this run created, so a refused run never touches
# someone else's flag.
cleanup() {
  if [ "$CREATED_FLAG" = 1 ]; then
    [ "$EXP_STARTED" = 1 ] && api POST "/environments/$ENV_ID/experiments/$EXP_KEY/stop"
    api DELETE "/environments/$ENV_ID/flags/$FLAG_KEY"
    if [ "$STATUS" = 204 ]; then echo "cleanup: removed $FLAG_KEY"; else
      echo "cleanup: could not remove $FLAG_KEY (HTTP $STATUS); delete it by hand" >&2; fi
  fi
  rm -f "$BODY_FILE"
}
trap cleanup EXIT

# Environments are addressed by id: find ENV_NAME's id in the active workspace.
api GET /me
[ "$STATUS" = 200 ] || { echo "ABORT GET /me failed (HTTP $STATUS); check API_BASE and TOKEN." >&2; exit 2; }
ENV_ID="$(printf '%s' "$BODY" | grep -o '"id":"[^"]*","key":"'"$ENV_NAME"'"' | head -1 | sed 's/"id":"\([^"]*\)".*/\1/')"
[ -n "$ENV_ID" ] || { echo "ABORT no environment \"$ENV_NAME\" in your active workspace." >&2; exit 2; }

FLAGS="/environments/$ENV_ID/flags"
EXPS="/environments/$ENV_ID/experiments"

# 0. refuse to run if the flag already exists
api GET "$FLAGS/$FLAG_KEY"
case "$STATUS" in
  404) echo "PASS  preflight: $FLAG_KEY does not exist" ;;
  200) echo "ABORT $FLAG_KEY already exists in $ENV_NAME; refusing to touch it." >&2; exit 2 ;;
  *)   echo "ABORT preflight failed (HTTP $STATUS, code $(code_of)); check API_BASE, ENV_NAME and TOKEN." >&2; exit 2 ;;
esac

# 1-2. flag
api POST "$FLAGS" '{"key":"'"$FLAG_KEY"'","name":"Smoke test flag","variationType":"boolean","variations":[{"id":"on","value":true},{"id":"off","value":false}]}'
check "create flag" 201
[ "$STATUS" = 201 ] && CREATED_FLAG=1
[ "$CREATED_FLAG" = 1 ] || { echo "cannot continue without the flag" >&2; exit 1; }

api PATCH "$FLAGS/$FLAG_KEY" '{"enabled":true}'
check "enable flag" 200 '"enabled":true'

# 3. experiments
api POST "$EXPS" '{"flagKey":"'"$FLAG_KEY"'","key":"'"$EXP_KEY"'","name":"Smoke experiment","metrics":[{"name":"Conversions","eventName":"smoke_converted","type":"conversion","isPrimary":true}]}'
check "create experiment (draft)" 201 '"status":"draft"'

api POST "$EXPS" '{"flagKey":"'"$FLAG_KEY"'","key":"'"$EXP2_KEY"'","name":"Smoke experiment 2","metrics":[{"name":"Conversions","eventName":"smoke_converted","type":"conversion","isPrimary":true}]}'
check "create second draft experiment" 201

api POST "$EXPS" '{"flagKey":"'"$FLAG_KEY"'","key":"smoke-exp-bad","name":"Reserved name","metrics":[{"name":"M","eventName":"$exposure","type":"conversion","isPrimary":true}]}'
check "reserved \$exposure event name is rejected" 400 INVALID_REQUEST

# 4. start
api POST "$EXPS/$EXP_KEY/start"
check "start experiment" 200 '"status":"running"'
[ "$STATUS" = 200 ] && EXP_STARTED=1

# 5. start again, then start a different experiment on the same flag
api POST "$EXPS/$EXP_KEY/start"
check "start the same experiment again -> INVALID_TRANSITION" 409 INVALID_TRANSITION

api POST "$EXPS/$EXP2_KEY/start"
check "start a second experiment on the flag -> EXPERIMENT_ALREADY_RUNNING" 409 EXPERIMENT_ALREADY_RUNNING

# 6. delete guard
api DELETE "$FLAGS/$FLAG_KEY"
check "delete flag while running -> HAS_RUNNING_EXPERIMENT" 409 HAS_RUNNING_EXPERIMENT

api DELETE "$FLAGS/$FLAG_KEY?force=true"
check "delete flag with force=true while running -> HAS_RUNNING_EXPERIMENT" 409 HAS_RUNNING_EXPERIMENT

# 7. stop
api POST "$EXPS/$EXP_KEY/stop"
check "stop experiment" 200 '"status":"stopped"'
[ "$STATUS" = 200 ] && EXP_STARTED=0

api POST "$EXPS/$EXP_KEY/stop"
check "stop again -> INVALID_TRANSITION" 409 INVALID_TRANSITION

# 8. delete succeeds now (the draft experiment cascades away with the flag)
api DELETE "$FLAGS/$FLAG_KEY"
check "delete flag after stop" 204
[ "$STATUS" = 204 ] && CREATED_FLAG=0

api GET "$EXPS/$EXP_KEY"
check "experiment is gone after flag delete" 404 EXPERIMENT_NOT_FOUND

echo
if [ "$FAILURES" -eq 0 ]; then echo "ALL PASSED"; else echo "$FAILURES FAILED"; fi
[ "$FAILURES" -eq 0 ]
