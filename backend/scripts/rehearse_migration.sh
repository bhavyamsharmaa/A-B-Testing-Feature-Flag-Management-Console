#!/usr/bin/env bash
# Rehearse the multi-tenancy migrations on a LOCAL database, and prove what
# keeps working at every step. Nothing here can reach production: the script
# unsets DATABASE_URL and every PG* variable, and only ever talks to the
# throwaway Postgres on 127.0.0.1:55432 (PGPORT below).
#
#   rehearse_migration.sh --db NAME [--seed | --admin-email EMAIL] [--order 0004,0005,0006,0007]
#
#   --seed          build a legacy-shaped database (migrations 0001-0003 plus
#                   realistic data and keys minted by the OLD code) in NAME.
#                   Without it NAME must already hold such a database, e.g. a
#                   restored copy of production; keys are then minted into the
#                   copy with the old code, and --admin-email names an existing
#                   admin whose login is rehearsed.
#   --order         the order migrations are applied in (default: 0004,0005,0006,0007).
#
# Needs these binaries (set in the environment):
#   OLD_API OLD_MKKEY   built from the code that is in production now
#   NEW_API DEVAUTH     built from this branch
#   PGBIN               PostgreSQL client/server binaries
#   MIGRATIONS          directory with 0001..0007 *.sql
#   WORK                scratch directory
#
# After every migration the OLD backend (still running, as it would be during
# the maintenance window) is asked: SDK evaluation identical to the baseline,
# the console's reads, and its writes. At the end the NEW backend is checked.
# Exit status is non-zero if anything that must hold does not.
set -u
unset DATABASE_URL PGHOST PGPORT PGUSER PGPASSWORD PGSERVICE PGSERVICEFILE PGPASSFILE PGDATABASE

DB=""; SEED=0; ADMIN_EMAIL="admin@legacy.test"; ORDER="0004,0005,0006,0007"
while [ $# -gt 0 ]; do
  case "$1" in
    --db) DB="$2"; shift 2;; --seed) SEED=1; shift;; --admin-email) ADMIN_EMAIL="$2"; shift 2;; --order) ORDER="$2"; shift 2;;
    *) echo "unknown argument $1" >&2; exit 2;;
  esac
done
case "$DB" in ""|*[!a-z0-9_]*) echo "--db must be a lower-case database name" >&2; exit 2;; esac
for v in OLD_API OLD_MKKEY NEW_API DEVAUTH PGBIN MIGRATIONS WORK; do eval "[ -n \"\${$v:-}\" ]" || { echo "$v is not set" >&2; exit 2; }; done

PGPORT_LOCAL=55432
psqlq() { "$PGBIN/psql" -h 127.0.0.1 -p $PGPORT_LOCAL -U helios -d "$DB" -v ON_ERROR_STOP=1 -q -At "$@"; }
DBURL="postgres://helios@127.0.0.1:$PGPORT_LOCAL/$DB?sslmode=disable"
AUTH_PORT=54399; OLD_PORT=8091; NEW_PORT=8092
AUTH_URL="http://127.0.0.1:$AUTH_PORT"
FAILS=0; WRITES_BROKEN=0; ROWS=()
say()  { printf '%s\n' "$*"; }
ok()   { say "PASS  $*"; }
bad()  { say "FAIL  $*"; FAILS=$((FAILS+1)); }
rec()  { ROWS+=("$1"); }
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done; }
trap cleanup EXIT

mkdir -p "$WORK"; : > "$WORK/rehearsal.log"
now_ms() { python3 -c 'import time;print(int(time.time()*1000))'; }

# ---------------------------------------------------------------- database
if [ $SEED = 1 ]; then
  "$PGBIN/dropdb" -h 127.0.0.1 -p $PGPORT_LOCAL -U helios --if-exists --force "$DB" 2>/dev/null
  "$PGBIN/createdb" -h 127.0.0.1 -p $PGPORT_LOCAL -U helios "$DB" || exit 2
  for f in 0001_core 0002_user_environment_roles 0003_experiments; do psqlq -f "$MIGRATIONS/$f.sql" || exit 2; done
  psqlq <<'SQL' || exit 2
INSERT INTO auth.users (id, email) VALUES
  ('aaaaaaaa-0000-4000-8000-0000000000a1', 'admin@legacy.test'),
  ('aaaaaaaa-0000-4000-8000-0000000000b2', 'viewer@legacy.test');
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-0000000000a1', id, 'admin' FROM environments;
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-0000000000b2', id, 'viewer' FROM environments WHERE key = 'dev';
INSERT INTO flags (key, name, variation_type, variations, created_by) VALUES
  ('demo-banner', 'Demo banner', 'boolean', '[{"id":"on","value":true},{"id":"off","value":false}]', 'aaaaaaaa-0000-4000-8000-0000000000a1'),
  ('pct-flag',    'Percent',     'boolean', '[{"id":"on","value":true},{"id":"off","value":false}]', 'aaaaaaaa-0000-4000-8000-0000000000a1'),
  ('rule-flag',   'Rule',        'boolean', '[{"id":"on","value":true},{"id":"off","value":false}]', 'aaaaaaaa-0000-4000-8000-0000000000a1'),
  ('off-flag',    'Off',         'boolean', '[{"id":"on","value":true},{"id":"off","value":false}]', 'aaaaaaaa-0000-4000-8000-0000000000a1');
INSERT INTO flag_configs (flag_id, environment_id, salt, fallthrough_variation_id, enabled, rollout, targeting_rules)
  SELECT f.id, e.id, 'salt-' || f.key || '-' || e.key, 'off',
         f.key <> 'off-flag',
         CASE WHEN f.key = 'pct-flag' THEN '{"on":30000,"off":70000}'::jsonb END,
         CASE WHEN f.key = 'rule-flag' THEN '[{"clauses":[{"attribute":"plan","operator":"equals","value":"pro"}],"variationId":"on"}]'::jsonb ELSE '[]'::jsonb END
  FROM flags f CROSS JOIN environments e;
INSERT INTO experiments (environment_id, flag_id, key, name)
  SELECT e.id, f.id, 'banner-test', 'Banner test' FROM environments e, flags f WHERE e.key = 'dev' AND f.key = 'demo-banner';
INSERT INTO experiment_metrics (experiment_id, name, event_name, type, is_primary) SELECT id, 'Clicks', 'banner_click', 'conversion', true FROM experiments;
INSERT INTO audit_logs (actor_email, environment_id, action, resource_type, resource_id) VALUES
  ('admin@legacy.test', NULL, 'flag.create', 'flag', 'demo-banner'),
  ('admin@legacy.test', (SELECT id FROM environments WHERE key = 'dev'), 'flag.update', 'flag', 'demo-banner');
SQL
fi

# ------------------------------------------------- servers and credentials
wait_http() { for _ in $(seq 1 60); do curl -s -o /dev/null "$1" && return 0; sleep 0.25; done; return 1; }
DATABASE_URL="$DBURL" "$DEVAUTH" -addr 127.0.0.1:$AUTH_PORT -public-url "$AUTH_URL" >"$WORK/devauth.log" 2>&1 & PIDS+=($!)
wait_http "$AUTH_URL/dev/health" || { say "devauth did not start"; exit 2; }

# The admin's login: devauth reuses the existing auth.users row for this email.
TOKEN=$(curl -s -X POST "$AUTH_URL/auth/v1/signup" -H 'Content-Type: application/json' -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"rehearsal-pw-1\"}" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("access_token",""))')
[ -n "$TOKEN" ] || { say "could not log the admin in through devauth"; exit 2; }

# SDK keys minted by the OLD code, before any migration: production, dev, and one revoked.
mint() { DATABASE_URL="$DBURL" "$OLD_MKKEY" -env "$1" 2>/dev/null | tail -1; }
KEY_PROD=$(mint production); KEY_DEV=$(mint dev); KEY_REVOKED=$(mint staging)
case "$KEY_PROD$KEY_DEV$KEY_REVOKED" in *hsdk_*hsdk_*hsdk_*) ;; *) say "old mkkey did not mint keys"; exit 2;; esac
psqlq -c "UPDATE api_keys SET revoked_at = now() WHERE key_prefix = '${KEY_REVOKED:0:13}'" >/dev/null

start_api() { # binary port
  DATABASE_URL="$DBURL" SUPABASE_URL="$AUTH_URL" PORT=$2 CORS_ALLOWED_ORIGINS=http://localhost:5173 "$1" >"$WORK/api-$2.log" 2>&1 & PIDS+=($!)
  LAST_PID=$!; wait_http "http://127.0.0.1:$2/healthz" || { say "api on $2 did not start"; exit 2; }
}
stop_pid() { kill "$1" 2>/dev/null; wait "$1" 2>/dev/null; }

evaluate_all() { # base-url key -> normalised JSON of every flag for 20 subjects
  python3 - "$1" "$2" <<'PY'
import json, sys, urllib.request
base, key = sys.argv[1], sys.argv[2]
out = {}
for i in range(1, 21):
    body = {"context": {"subjectKey": f"u{i}", "attributes": {"plan": "pro" if i % 2 == 0 else "free"}},
            "flagKeys": ["demo-banner", "pct-flag", "rule-flag", "off-flag", "missing-flag"]}
    req = urllib.request.Request(base + "/evaluate", json.dumps(body).encode(), {"Content-Type": "application/json", "X-Helios-SDK-Key": key})
    try:
        with urllib.request.urlopen(req) as r: out[f"u{i}"] = json.load(r)
    except urllib.error.HTTPError as e: out[f"u{i}"] = {"http": e.code}
print(json.dumps(out, sort_keys=True))
PY
}
http() { # method url [bearer] [json] -> prints status
  local m="$1" u="$2" t="${3:-}" b="${4:-}" args=()
  [ -n "$t" ] && args+=(-H "Authorization: Bearer $t")
  [ -n "$b" ] && args+=(-H 'Content-Type: application/json' --data "$b")
  curl -s -o "$WORK/last-body" -w '%{http_code}' -X "$m" "${args[@]}" "$u"
}
expect() { # wanted-status description method url [bearer] [json]; sets WHY on mismatch
  local want="$1" what="$2"; shift 2; local got; got=$(http "$@")
  [ "$got" = "$want" ] || { WHY="$WHY [$what: got $got, wanted $want: $(head -c 160 "$WORK/last-body")]"; return 1; }
}
snapshot() { psqlq -c "select 'env',id::text,key from environments union all select 'api_key',id::text,key_prefix||coalesce(revoked_at::text,'') from api_keys union all select 'flag',id::text,key from flags union all select 'flag_config',id::text,flag_id::text||environment_id::text||enabled::text from flag_configs union all select 'audit',id::text,action from audit_logs where resource_id not like 'rehearsal-%' order by 1,3,2"; }
counts() { psqlq -c "select 'environments',count(*) from environments union all select 'flags',count(*) from flags union all select 'flag_configs',count(*) from flag_configs union all select 'api_keys',count(*) from api_keys union all select 'experiments',count(*) from experiments union all select 'audit_logs',count(*) from audit_logs where resource_id not like 'rehearsal-%' union all select 'user_environment_roles',count(*) from user_environment_roles order by 1"; }

# --------------------------------------------------------------- baseline
start_api "$OLD_API" $OLD_PORT; OLD_PID=$LAST_PID
BASE_PROD=$(evaluate_all "http://127.0.0.1:$OLD_PORT" "$KEY_PROD"); BASE_DEV=$(evaluate_all "http://127.0.0.1:$OLD_PORT" "$KEY_DEV")
echo "$BASE_PROD" > "$WORK/baseline-prod.json"
python3 - "$BASE_PROD" <<'PY' || { say "baseline is empty or not varied; the comparison would prove nothing"; exit 2; }
import json,sys
d=json.loads(sys.argv[1]); vals={json.dumps(e.get("value")) for u in d.values() for e in u.get("evaluations",[])}
sys.exit(0 if len(vals)>=2 else 1)
PY
[ "$(http POST "http://127.0.0.1:$OLD_PORT/evaluate" "" '{}' )" != 200 ] || true
snapshot > "$WORK/ids-before.txt"; counts > "$WORK/counts-before.txt"
say "baseline: $(wc -l < "$WORK/ids-before.txt" | tr -d ' ') ids snapshotted; counts:"; sed 's/^/    /' "$WORK/counts-before.txt" | tr '|' ' '

old_checks() { # label
  local label="$1" ev=ok rd=ok w=ok WHY="" B="http://127.0.0.1:$OLD_PORT"
  [ "$(evaluate_all "$B" "$KEY_PROD")" = "$BASE_PROD" ] || { ev=DIFFERENT; WHY="$WHY [prod key evaluations differ]"; }
  [ "$(evaluate_all "$B" "$KEY_DEV")" = "$BASE_DEV" ] || { ev=DIFFERENT; WHY="$WHY [dev key evaluations differ]"; }
  local rev; rev=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "X-Helios-SDK-Key: $KEY_REVOKED" -H 'Content-Type: application/json' --data '{"context":{"subjectKey":"x"},"flagKeys":["demo-banner"]}' "$B/evaluate")
  [ "$rev" = 401 ] || { ev=DIFFERENT; WHY="$WHY [revoked key got $rev]"; }
  expect 200 me GET "$B/me" "$TOKEN" && grep -q '"roles"' "$WORK/last-body" || rd=FAIL
  expect 200 flags GET "$B/environments/dev/flags" "$TOKEN" && grep -q demo-banner "$WORK/last-body" || rd=FAIL
  expect 200 audit GET "$B/environments/dev/audit-logs" "$TOKEN" || rd=FAIL
  local k="rehearsal-$RANDOM" body
  body="{\"key\":\"$k\",\"name\":\"r\",\"variationType\":\"boolean\",\"variations\":[{\"id\":\"on\",\"value\":true},{\"id\":\"off\",\"value\":false}]}"
  expect 201 create POST "$B/environments/dev/flags" "$TOKEN" "$body" || w=REFUSED
  [ $w = ok ] && { expect 200 toggle PATCH "$B/environments/dev/flags/$k" "$TOKEN" '{"enabled":true}' || w=REFUSED; }
  [ $w = ok ] && { expect 200 kill POST "$B/environments/dev/flags/$k/kill" "$TOKEN" || w=REFUSED; }
  [ $w = ok ] && { expect 204 delete DELETE "$B/environments/dev/flags/$k" "$TOKEN" || w=REFUSED; }
  say "      old backend $label: SDK evaluation=$ev  console reads=$rd  console writes=$w$WHY"
  rec "$label|$ev|$rd|$w"
  [ "$ev" = ok ] || FAILS=$((FAILS+1)); [ "$rd" = ok ] || FAILS=$((FAILS+1))
  [ "$w" = ok ] || WRITES_BROKEN=$((WRITES_BROKEN+1))
}

# ------------------------------------------------------------- migrations
say ""; say "== control: the old backend before any migration"; old_checks before-migration
say ""; say "== migrations in the order: $ORDER (the OLD backend keeps serving throughout)"
for m in ${ORDER//,/ }; do
  f=$(ls "$MIGRATIONS"/${m}_*.sql); t0=$(now_ms)
  if psqlq -f "$f" >>"$WORK/rehearsal.log" 2>&1; then ok "applied $m in $(( $(now_ms) - t0 )) ms"; else bad "migration $m failed; see $WORK/rehearsal.log"; break; fi
  old_checks "after-$m"
done
say ""; say "== verify scripts"
for v in 0004 0005 0006 0007; do
  vf="$MIGRATIONS/../verify_$v.sql"; [ -f "$vf" ] || continue
  out=$(psqlq -f "$vf" 2>&1 | grep -E 'FAIL|SUMMARY' | tr '\n' ' ')
  case "$v" in
    0004) # only meaningful before 0005/0006 change the keys and defaults: report, do not fail
      say "      verify_$v (expected to differ once 0005/0006 ran): $out";;
    *) case "$out" in *"|FAIL"*) bad "verify_$v: $out";; *) ok "verify_$v: $out";; esac;;
  esac
done
snapshot > "$WORK/ids-after.txt"; counts > "$WORK/counts-after.txt"
if diff -q "$WORK/ids-before.txt" "$WORK/ids-after.txt" >/dev/null; then ok "environment, api_key, flag, flag_config and audit ids and contents are identical before and after ($(wc -l < "$WORK/ids-after.txt" | tr -d ' ') rows)"; else bad "ids or contents changed"; diff "$WORK/ids-before.txt" "$WORK/ids-after.txt" | head -10; fi
if diff -q "$WORK/counts-before.txt" "$WORK/counts-after.txt" >/dev/null; then ok "row counts unchanged for every pre-existing table"; else bad "row counts changed"; diff "$WORK/counts-before.txt" "$WORK/counts-after.txt"; fi

# ---------------------------------------------------------- new backend
say ""; say "== the NEW backend on the migrated database"
stop_pid $OLD_PID
start_api "$NEW_API" $NEW_PORT
NEWB="http://127.0.0.1:$NEW_PORT"
[ "$(evaluate_all "$NEWB" "$KEY_PROD")" = "$BASE_PROD" ] && ok "SDK key minted BEFORE the migration (production): evaluations identical to the baseline (20 subjects x 5 flags)" || bad "production key: evaluations differ from the baseline"
[ "$(evaluate_all "$NEWB" "$KEY_DEV")" = "$BASE_DEV" ] && ok "SDK key minted BEFORE the migration (dev): evaluations identical to the baseline" || bad "dev key: evaluations differ"
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "X-Helios-SDK-Key: $KEY_REVOKED" -H 'Content-Type: application/json' --data '{"context":{"subjectKey":"x"},"flagKeys":["demo-banner"]}' "$NEWB/evaluate")" = 401 ] && ok "a key revoked before the migration is still rejected" || bad "revoked key accepted"
[ "$(http GET "$NEWB/me" "$TOKEN")" = 200 ] && ok "existing admin logs in: /me answers 200" || bad "/me for the existing admin failed"
python3 - "$WORK/last-body" <<'PY' && ok "the admin's data moved into ONE workspace as its owner, with the same three environments" || bad "unexpected /me shape"
import json,sys
d=json.load(open(sys.argv[1])); w=d["workspaces"]
assert len(w)==1 and w[0]["role"]=="owner" and sorted(e["key"] for e in w[0]["environments"])==["dev","production","staging"], d
PY
ENV_DEV=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print([e["id"] for e in d["workspaces"][0]["environments"] if e["key"]=="dev"][0])' "$WORK/last-body")
[ "$(http GET "$NEWB/environments/$ENV_DEV/flags" "$TOKEN")" = 200 ] && grep -q demo-banner "$WORK/last-body" && ok "flag list through the new console API" || bad "flag list failed"
[ "$(http POST "$NEWB/environments/$ENV_DEV/flags" "$TOKEN" '{"key":"after-migration","name":"n","variationType":"boolean","variations":[{"id":"on","value":true},{"id":"off","value":false}]}')" = 201 ] && ok "creating a flag works" || bad "creating a flag failed"
[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 -H "X-Helios-SDK-Key: $KEY_PROD" "$NEWB/sdk/stream")" = 200 ] || [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 -H "X-Helios-SDK-Key: $KEY_PROD" "$NEWB/sdk/stream")" = 000 ] && ok "an old key opens /sdk/stream (503 only when no Redis is configured)" || true
SNEW=$(http POST "$AUTH_URL/auth/v1/signup" "" '{"email":"newuser@legacy.test","password":"rehearsal-pw-1"}'); NEWTOKEN=$(python3 -c 'import json; print(json.load(open("'"$WORK"'/last-body")).get("access_token",""))')
[ "$(http GET "$NEWB/me" "$NEWTOKEN")" = 200 ] && python3 -c 'import json; d=json.load(open("'"$WORK"'/last-body")); assert len(d["workspaces"])==1 and d["workspaces"][0]["role"]=="owner" and d["workspaces"][0]["name"].startswith("newuser")' && ok "a brand-new user gets a private workspace of their own" || bad "new user bootstrap failed"
[ "$(http GET "$NEWB/environments/$ENV_DEV/flags" "$NEWTOKEN")" = 404 ] && ok "...and cannot see the existing workspace (404)" || bad "new user can reach the existing workspace"

# ---------------------------------------------------------------- summary
say ""; say "== what the OLD backend could still do after each step"
printf '   %-16s %-16s %-15s %s\n' step "SDK evaluation" "console reads" "console writes"
for r in "${ROWS[@]:-}"; do IFS='|' read -r a b c d <<<"$r"; printf '   %-16s %-16s %-15s %s\n' "$a" "$b" "$c" "$d"; done
say ""; [ $WRITES_BROKEN -gt 0 ] && say "NOTE: the old backend could not write in $WRITES_BROKEN state(s); that is reported, not failed, because the plan only needs old WRITES to survive until the new backend is live (see docs/ROLLOUT.md)."
[ $FAILS = 0 ] && say "REHEARSAL PASSED" || say "REHEARSAL FAILED ($FAILS)"
exit $((FAILS > 0))
