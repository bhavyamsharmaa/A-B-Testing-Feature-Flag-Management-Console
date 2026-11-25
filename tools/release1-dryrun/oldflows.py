#!/usr/bin/env python3
"""Flows of the OLD backend (main, 6c90844) as the old console uses them, to prove the baseline works and to see what a
migrated database does to it. Local only. Keys come from an env file (KEY_PRODUCTION, KEY_DEV, KEY_STAGING; staging is revoked).

    oldflows.py --api http://127.0.0.1:8081 --auth http://127.0.0.1:54331 --keys ~/helios-local/dryrun/work/keys.env
Prints one line per check: PASS/FAIL, what, status. Exit 1 if anything that must hold does not.
"""
import argparse, json, os, sys, urllib.request, urllib.error

NAMES = ["alice", "bob", "carol", "dan", "erin", "frank"]
def call(method, url, token=None, body=None, key=None):
    h = {"Content-Type": "application/json"}
    if token: h["Authorization"] = "Bearer " + token
    if key: h["X-Helios-SDK-Key"] = key
    req = urllib.request.Request(url, json.dumps(body).encode() if body is not None else None, h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            raw = r.read(); return r.status, (json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode())
    except urllib.error.HTTPError as e:
        raw = e.read()
        try: return e.code, json.loads(raw)
        except Exception: return e.code, raw.decode(errors="replace")[:200]
    except Exception as e:
        return 0, str(e)

def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--api", required=True); ap.add_argument("--auth", required=True); ap.add_argument("--keys", required=True)
    ap.add_argument("--allow-write-failure", action="store_true", help="report failing WRITES but do not fail the run (the known state after 0005)")
    a = ap.parse_args()
    keys = dict(l.strip().split("=", 1) for l in open(os.path.expanduser(a.keys)) if "=" in l)
    fails = 0; writes_failed = 0
    def check(ok, what, got="", write=False):
        nonlocal fails, writes_failed
        print(("PASS  " if ok else ("WRITE-FAIL  " if write and a.allow_write_failure else "FAIL  ")) + what + (f"  [{got}]" if not ok or got else ""))
        if not ok:
            if write and a.allow_write_failure: writes_failed += 1
            else: fails += 1
        return ok
    tok = {}
    for n in NAMES:
        em = {"email": f"{n}@synthetic.invalid", "password": "rehearsal-pw-1"}
        st, b = call("POST", a.auth + "/auth/v1/token?grant_type=password", body=em)   # already known to the stand-in?
        if not (isinstance(b, dict) and b.get("access_token")):
            st, b = call("POST", a.auth + "/auth/v1/signup", body=em)                  # first time: sign up (reuses the existing row)
        tok[n] = b.get("access_token") if isinstance(b, dict) else None
    check(all(tok.values()), "six existing users can log in (the auth stand-in reuses their rows)")
    A = a.api
    st, me = call("GET", A + "/me", tok["alice"])
    check(st == 200 and isinstance(me, dict) and "roles" in me, "alice: /me (old shape: per-environment roles)", st)
    st, me = call("GET", A + "/me", tok["erin"])
    check(st == 200 and isinstance(me, dict) and set(r.get("environment", r.get("key", "")) if isinstance(r, dict) else r for r in me.get("roles", [])) , "erin (viewer in dev only): /me lists her role", st)
    st, fl = call("GET", A + "/environments/dev/flags", tok["alice"])
    n = len(fl) if isinstance(fl, list) else len(fl.get("flags", [])) if isinstance(fl, dict) else -1
    check(st == 200 and n >= 60, "alice: dev flag list shows the 60 existing flags", f"{st} count={n}")
    st, _ = call("GET", A + "/environments/dev/flags", tok["erin"]); check(st == 200, "erin (viewer): can read dev flags", st)
    st, _ = call("GET", A + "/environments/production/flags", tok["erin"]); check(st in (403, 404), "erin: production is refused (no role there)", st)
    st, _ = call("GET", A + "/environments/dev/audit-logs", tok["alice"]); check(st == 200, "alice: audit log reads", st)
    st, ex = call("GET", A + "/environments/dev/experiments", tok["alice"])
    check(st == 200 and len(ex if isinstance(ex, list) else ex.get("experiments", [])) == 6, "alice: the 6 existing experiments are listed", st)
    # writes
    body = lambda k: {"key": k, "name": k, "variationType": "boolean", "variations": [{"id": "on", "value": True}, {"id": "off", "value": False}]}
    st, _ = call("POST", A + "/environments/dev/flags", tok["dan"], body("dry-flag-1")); check(st == 201, "dan (editor): creates a flag in dev", st, write=True)
    st, _ = call("POST", A + "/environments/dev/flags", tok["erin"], body("dry-flag-x")); check(st == 403, "erin (viewer): creating a flag is refused", st)
    st, _ = call("PATCH", A + "/environments/dev/flags/dry-flag-1", tok["dan"], {"enabled": True}); check(st == 200, "dan: toggles it on in dev", st, write=True)
    st, _ = call("PATCH", A + "/environments/production/flags/flag-001", tok["dan"], {"enabled": True}); check(st in (403, 404), "dan: production toggle refused (no role there)", st)
    st, _ = call("PATCH", A + "/environments/production/flags/flag-001", tok["carol"], {"enabled": True}); check(st == 200, "carol (approver): production toggle allowed", st, write=True)
    st, _ = call("POST", A + "/environments/dev/flags/dry-flag-1/kill", tok["frank"]); check(st == 200, "frank (editor): kills it", st, write=True)
    st, _ = call("DELETE", A + "/environments/dev/flags/dry-flag-1", tok["dan"]); check(st in (403, 404), "dan: delete refused (admin only)", st)
    st, _ = call("DELETE", A + "/environments/dev/flags/dry-flag-1", tok["alice"]); check(st == 204, "alice (admin): deletes it", st, write=True)
    st, _ = call("POST", A + "/environments/dev/experiments", tok["alice"], {"flagKey": "flag-020", "key": "dry-exp", "name": "Dry",
        "metrics": [{"name": "M", "eventName": "purchase", "type": "conversion", "isPrimary": True}]}); check(st == 201, "alice: creates an experiment", st, write=True)
    st, _ = call("POST", A + "/environments/dev/experiments/dry-exp/start", tok["alice"]); check(st == 200, "alice: starts it", st, write=True)
    st, _ = call("POST", A + "/environments/dev/experiments/dry-exp/stop", tok["alice"]); check(st == 200, "alice: stops it", st, write=True)
    st, _ = call("POST", A + "/environments/dev/members", tok["alice"], {"email": "gina@synthetic.invalid", "role": "viewer"}); check(st in (200, 201), "alice: adds (invites) gina as viewer in dev", st, write=True)
    st, _ = call("GET", A + "/environments/dev/flags", tok["alice"]); check(st == 200, "reads still work after the writes", st)
    # SDK
    ev = {"context": {"subjectKey": "u1", "attributes": {"plan": "pro"}}, "flagKeys": ["flag-001", "flag-003", "flag-005", "missing"]}
    st, r = call("POST", A + "/evaluate", body=ev, key=keys["KEY_PRODUCTION"]); check(st == 200 and len(r.get("evaluations", [])) == 4, "SDK key minted before the migration (production) evaluates", st)
    st, r = call("POST", A + "/evaluate", body=ev, key=keys["KEY_DEV"]); check(st == 200, "SDK key (dev) evaluates", st)
    st, _ = call("POST", A + "/evaluate", body=ev, key=keys["KEY_STAGING"]); check(st == 401, "the revoked SDK key is rejected", st)
    print(f"\n{'ALL PASSED' if fails == 0 else str(fails) + ' FAILED'}" + (f" ({writes_failed} write(s) failing, tolerated)" if writes_failed else ""))
    sys.exit(1 if fails else 0)
main()
