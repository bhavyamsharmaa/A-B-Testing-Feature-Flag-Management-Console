#!/usr/bin/env python3
"""Keeps the OLD (or NEW) backend busy while migrations run, and records every request's time, so a lock that blocks
users shows up as a slow or failed request. Local only.

    probe.py --api URL --auth URL --keys FILE --out FILE [--flag flag-002 --seconds 600]
Each loop: SDK evaluate (read), console flag list (read), console toggle of one flag (write). Stops on SIGTERM or after --seconds.
Output lines: epoch_ms,kind,status,elapsed_ms
"""
import argparse, json, os, signal, threading, time, urllib.request, urllib.error
ap = argparse.ArgumentParser(); ap.add_argument("--api", required=True); ap.add_argument("--auth", required=True); ap.add_argument("--keys", required=True)
ap.add_argument("--out", required=True); ap.add_argument("--seconds", type=int, default=3600); ap.add_argument("--env", default="dev"); ap.add_argument("--flag", default="flag-002")
ap.add_argument("--new", action="store_true", help="new backend paths (environment ids from /me)")
a = ap.parse_args()
keys = dict(l.strip().split("=", 1) for l in open(os.path.expanduser(a.keys)) if "=" in l)
def req(method, url, token=None, body=None, key=None):
    h = {"Content-Type": "application/json"}
    if token: h["Authorization"] = "Bearer " + token
    if key: h["X-Helios-SDK-Key"] = key
    r = urllib.request.Request(url, json.dumps(body).encode() if body is not None else None, h, method=method)
    t = time.time()
    try:
        with urllib.request.urlopen(r, timeout=30) as resp: resp.read(); s = resp.status
    except urllib.error.HTTPError as e: s = e.code
    except Exception: s = 0
    return s, int((time.time() - t) * 1000)
def login(email):
    r = urllib.request.Request(a.auth + "/auth/v1/token?grant_type=password", json.dumps({"email": email, "password": "rehearsal-pw-1"}).encode(), {"Content-Type": "application/json"}, method="POST")
    try: return json.load(urllib.request.urlopen(r, timeout=10))["access_token"]
    except Exception: return None
tok = login("alice@synthetic.invalid")
if not tok:
    r = urllib.request.Request(a.auth + "/auth/v1/signup", json.dumps({"email": "alice@synthetic.invalid", "password": "rehearsal-pw-1"}).encode(), {"Content-Type": "application/json"}, method="POST")
    tok = json.load(urllib.request.urlopen(r, timeout=10))["access_token"]
stop = threading.Event(); lock = threading.Lock(); out = open(a.out, "w", buffering=1)
signal.signal(signal.SIGTERM, lambda *_: stop.set()); signal.signal(signal.SIGINT, lambda *_: stop.set())
envpath = a.env
if a.new:
    r = urllib.request.Request(a.api + "/me", None, {"Authorization": "Bearer " + tok}); me = json.load(urllib.request.urlopen(r, timeout=10))
    envpath = [e["id"] for e in me["workspaces"][0]["environments"] if e["key"] == a.env][0]
ev = {"context": {"subjectKey": "u1"}, "flagKeys": ["flag-001", "flag-003", "flag-005"]}
def rec(kind, s, ms):
    with lock: out.write(f"{int(time.time()*1000)},{kind},{s},{ms}\n")
def reader():
    while not stop.is_set():
        rec("sdk-evaluate", *req("POST", a.api + "/evaluate", body=ev, key=keys["KEY_PRODUCTION"]))
        rec("console-flags", *req("GET", f"{a.api}/environments/{envpath}/flags", tok)); time.sleep(0.02)
def writer():
    n = 0
    while not stop.is_set():
        n += 1
        rec("console-toggle", *req("PATCH", f"{a.api}/environments/{envpath}/flags/{a.flag}", tok, {"enabled": n % 2 == 0})); time.sleep(0.05)
ts = [threading.Thread(target=f, daemon=True) for f in (reader, reader, writer)]
[t.start() for t in ts]; stop.wait(a.seconds); stop.set(); [t.join(5) for t in ts]
