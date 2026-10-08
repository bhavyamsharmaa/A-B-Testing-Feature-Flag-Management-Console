#!/usr/bin/env bash
# Cross-tenant smoke test: user B must not be able to see or change anything
# that belongs to user A's workspace. Run against a live backend that has
# migrations 0004 (and 0005) applied.
#
#   API_BASE=http://localhost:8080 A_TOKEN=... B_TOKEN=... backend/scripts/smoke_tenancy.sh
#
# A_TOKEN and B_TOKEN are Supabase access tokens of two DIFFERENT, throwaway
# users who each have their own workspace (their first GET /me creates it).
# Tokens are read from the environment only, are handed to curl through a
# config file descriptor (so they never appear in `ps`), and are never
# printed. Needs bash and curl.
#
# A creates a flag, enables it and starts an experiment on it. B then tries,
# by A's environment UUID, to read, patch, kill and delete the flag, to read,
# start and stop the experiment, to read the audit log and the environment,
# and to grant itself a role: every attempt must answer 404. There are no
# API-key HTTP routes today, so "key by ID" is covered by the members routes,
# the only other ID-addressed resource. Finally B checks, from its own
# environment, that A's data is invisible, and that it can create a flag with
# the same key (flag keys are per workspace).
#
# It only removes what it created.

set -u
set +x

: "${API_BASE:?set API_BASE, e.g. http://localhost:8080}"
: "${A_TOKEN:?set A_TOKEN (user A's access token)}"
: "${B_TOKEN:?set B_TOKEN (user B's access token)}"
API_BASE="${API_BASE%/}"

FLAG="tenancy-smoke-flag"
EXP="tenancy-smoke-exp"
BODY_FILE="$(mktemp)"
FAILURES=0
A_FLAG=0; A_EXP_STARTED=0; B_FLAG=0

api() { # TOKEN_VAR METHOD PATH [JSON]  -> STATUS, BODY
  local token="${!1}" method="$2" path="$3" data="${4:-}"
  local args=(-sS -o "$BODY_FILE" -w '%{http_code}' -X "$method" --config <(printf 'header = "Authorization: Bearer %s"\n' "$token"))
  if [ -n "$data" ]; then args+=(-H 'Content-Type: application/json' --data "$data"); fi
  STATUS="$(curl "${args[@]}" "$API_BASE$path" 2>/dev/null)" || STATUS="000"
  BODY="$(cat "$BODY_FILE")"
}
code_of() { printf '%s' "$BODY" | grep -o '"code":"[A-Z_]*"' | head -1 | sed 's/"code":"\(.*\)"/\1/'; }

pass() { echo "PASS  $1"; }
fail() { FAILURES=$((FAILURES + 1)); echo "FAIL  $1 ($2)"; }

# expect NAME STATUS [CODE]
expect() {
  local name="$1" want="$2" code="${3:-}"
  if [ "$STATUS" = "$want" ] && { [ -z "$code" ] || [ "$(code_of)" = "$code" ]; }; then pass "$name"
  else fail "$name" "expected HTTP $want${code:+ $code}; got HTTP $STATUS $(code_of)"; fi
}
# expect_absent NAME NEEDLE: the last response body must not contain NEEDLE
expect_absent() {
  if printf '%s' "$BODY" | grep -qF -- "$2"; then fail "$1" "response contains $2"; else pass "$1"; fi
}

cleanup() {
  if [ "$A_EXP_STARTED" = 1 ]; then api A_TOKEN POST "/environments/$A_ENV/experiments/$EXP/stop"; fi
  if [ "$A_FLAG" = 1 ]; then api A_TOKEN DELETE "/environments/$A_ENV/flags/$FLAG"; [ "$STATUS" = 204 ] || echo "cleanup: could not delete A's $FLAG (HTTP $STATUS)" >&2; fi
  if [ "$B_FLAG" = 1 ]; then api B_TOKEN DELETE "/environments/$B_ENV/flags/$FLAG"; [ "$STATUS" = 204 ] || echo "cleanup: could not delete B's $FLAG (HTTP $STATUS)" >&2; fi
  rm -f "$BODY_FILE"
}
trap cleanup EXIT

# --- identities --------------------------------------------------------
api A_TOKEN GET /me; [ "$STATUS" = 200 ] || { echo "ABORT A's /me failed (HTTP $STATUS)" >&2; exit 2; }
A_ENV="$(printf '%s' "$BODY" | grep -o '"id":"[^"]*","key":"dev"' | head -1 | sed 's/"id":"\([^"]*\)".*/\1/')"
A_USER="$(printf '%s' "$BODY" | sed 's/^{"id":"\([^"]*\)".*/\1/')"
A_WS="$(printf '%s' "$BODY" | sed 's/.*"workspace":{"id":"\([^"]*\)".*/\1/')"
api B_TOKEN GET /me; [ "$STATUS" = 200 ] || { echo "ABORT B's /me failed (HTTP $STATUS)" >&2; exit 2; }
B_ENV="$(printf '%s' "$BODY" | grep -o '"id":"[^"]*","key":"dev"' | head -1 | sed 's/"id":"\([^"]*\)".*/\1/')"
B_USER="$(printf '%s' "$BODY" | sed 's/^{"id":"\([^"]*\)".*/\1/')"
B_WS="$(printf '%s' "$BODY" | sed 's/.*"workspace":{"id":"\([^"]*\)".*/\1/')"
if [ -z "$A_ENV" ] || [ -z "$B_ENV" ] || [ -z "$A_WS" ] || [ -z "$B_WS" ]; then echo "ABORT could not read environments from /me" >&2; exit 2; fi
if [ "$A_USER" = "$B_USER" ] || [ "$A_WS" = "$B_WS" ] || [ "$A_ENV" = "$B_ENV" ]; then echo "ABORT A and B must be different users with different workspaces" >&2; exit 2; fi
pass "A and B have different workspaces and different dev environments"

for who in A B; do
  env_var="${who}_ENV"; tok="${who}_TOKEN"
  api "$tok" GET "/environments/${!env_var}/flags/$FLAG"
  [ "$STATUS" = 404 ] || { echo "ABORT $who already has a flag $FLAG (or the API is unreachable: HTTP $STATUS)" >&2; exit 2; }
done

# --- A sets up ---------------------------------------------------------
api A_TOKEN POST "/environments/$A_ENV/flags" '{"key":"'"$FLAG"'","name":"Tenancy smoke","variationType":"boolean","variations":[{"id":"on","value":true},{"id":"off","value":false}]}'
expect "A creates a flag" 201; [ "$STATUS" = 201 ] && A_FLAG=1
[ "$A_FLAG" = 1 ] || { echo "cannot continue" >&2; exit 1; }
api A_TOKEN PATCH "/environments/$A_ENV/flags/$FLAG" '{"enabled":true}'
expect "A enables the flag" 200
api A_TOKEN POST "/environments/$A_ENV/experiments" '{"flagKey":"'"$FLAG"'","key":"'"$EXP"'","name":"Tenancy smoke","metrics":[{"name":"Conversions","eventName":"smoke_converted","type":"conversion","isPrimary":true}]}'
expect "A creates an experiment" 201
api A_TOKEN POST "/environments/$A_ENV/experiments/$EXP/start"
expect "A starts the experiment" 200; [ "$STATUS" = 200 ] && A_EXP_STARTED=1

# --- B attacks A's environment by UUID: everything must be 404 ---------
E="/environments/$A_ENV"
attack() { # NAME METHOD PATH [JSON]
  api B_TOKEN "$2" "$3" "${4:-}"
  expect "B -> $1" 404 ENVIRONMENT_NOT_FOUND
}
attack "GET A's flags"                GET    "$E/flags"
attack "GET A's flag"                 GET    "$E/flags/$FLAG"
attack "PATCH A's flag"               PATCH  "$E/flags/$FLAG" '{"enabled":false}'
attack "kill A's flag"                POST   "$E/flags/$FLAG/kill"
attack "DELETE A's flag"              DELETE "$E/flags/$FLAG?force=true"
attack "create a flag in A's env"     POST   "$E/flags" '{"key":"b-intruder","name":"x","variationType":"boolean","variations":[{"id":"on","value":true},{"id":"off","value":false}]}'
attack "GET A's experiments"          GET    "$E/experiments"
attack "GET A's experiment"           GET    "$E/experiments/$EXP"
attack "start A's experiment"         POST   "$E/experiments/$EXP/start"
attack "stop A's experiment"          POST   "$E/experiments/$EXP/stop"
attack "create an experiment in A's env" POST "$E/experiments" '{"flagKey":"'"$FLAG"'","key":"b-intruder","name":"x","metrics":[{"name":"M","eventName":"e","type":"conversion","isPrimary":true}]}'
attack "GET A's audit log"            GET    "$E/audit-logs"
attack "grant itself a role in A's env" POST "$E/members" '{"userId":"'"$B_USER"'","role":"admin"}'
attack "remove A from A's env"        DELETE "$E/members/$A_USER"

# --- B from its own environment: A's data must not exist for B ---------
O="/environments/$B_ENV"
api B_TOKEN GET "$O/flags/$FLAG";                        expect "B's env: A's flag key is not found" 404 FLAG_NOT_FOUND
api B_TOKEN PATCH "$O/flags/$FLAG" '{"enabled":false}';  expect "B's env: PATCH A's flag key -> 404" 404 FLAG_NOT_FOUND
api B_TOKEN POST "$O/flags/$FLAG/kill";                  expect "B's env: kill A's flag key -> 404" 404 FLAG_NOT_FOUND
api B_TOKEN DELETE "$O/flags/$FLAG";                     expect "B's env: DELETE A's flag key -> 404" 404 FLAG_NOT_FOUND
api B_TOKEN POST "$O/experiments" '{"flagKey":"'"$FLAG"'","key":"b-intruder","name":"x","metrics":[{"name":"M","eventName":"e","type":"conversion","isPrimary":true}]}'
expect "B's env: experiment on A's flag key -> 404" 404 FLAG_NOT_FOUND
api B_TOKEN GET "$O/flags";      expect "B lists its flags" 200;      expect_absent "B's flag list does not contain A's flag" "$FLAG"
api B_TOKEN GET "$O/audit-logs"; expect "B reads its audit log" 200;  expect_absent "B's audit log does not contain A's flag" "$FLAG"
expect_absent "B's audit log does not contain A's workspace id" "$A_WS"
# The temporary environment-key shim must resolve inside B's own workspace.
api B_TOKEN GET "/environments/dev/flags"
expect "B reads /environments/dev/flags by key" 200; expect_absent "B's 'dev' by key does not contain A's flag" "$FLAG"
api B_TOKEN GET "/environments/dev/audit-logs"
expect "B reads /environments/dev/audit-logs by key" 200; expect_absent "B's 'dev' audit log by key does not contain A's flag" "$FLAG"

# --- A is untouched ----------------------------------------------------
api A_TOKEN GET "$E/flags/$FLAG"
expect "A's flag still exists" 200; printf '%s' "$BODY" | grep -q '"enabled":true' && pass "A's flag is still enabled" || fail "A's flag is still enabled" "B changed it"
api A_TOKEN GET "$E/experiments/$EXP"
printf '%s' "$BODY" | grep -q '"status":"running"' && pass "A's experiment is still running" || fail "A's experiment is still running" "B changed it"

# --- keys are unique per workspace -------------------------------------
api B_TOKEN POST "$O/flags" '{"key":"'"$FLAG"'","name":"B same key","variationType":"boolean","variations":[{"id":"on","value":true},{"id":"off","value":false}]}'
expect "B can create a flag with A's key (keys are per workspace)" 201; [ "$STATUS" = 201 ] && B_FLAG=1

echo
if [ "$FAILURES" -eq 0 ]; then echo "ALL PASSED"; else echo "$FAILURES FAILED"; fi
[ "$FAILURES" -eq 0 ]
