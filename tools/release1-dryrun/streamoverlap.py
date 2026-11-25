#!/usr/bin/env python3
"""Old and new API instances at the same time on one database and one Redis (as during a rolling deploy): is a flag change made
through one side pushed to /sdk/stream clients connected to the other? Local only.
   streamoverlap.py --old http://127.0.0.1:8191 --new http://127.0.0.1:8080 --auth http://127.0.0.1:54321 --keys FILE"""
import argparse, json, os, threading, time, urllib.request
ap = argparse.ArgumentParser(); [ap.add_argument("--" + n, required=True) for n in ("old", "new", "auth", "keys")]; a = ap.parse_args()
keys = dict(l.strip().split("=", 1) for l in open(os.path.expanduser(a.keys)) if "=" in l)
def call(m, u, t=None, b=None):
    h = {"Content-Type": "application/json"}; h.update({"Authorization": "Bearer " + t} if t else {})
    r = urllib.request.Request(u, json.dumps(b).encode() if b is not None else None, h, method=m)
    x = urllib.request.urlopen(r, timeout=10); return json.load(x)
t = call("POST", a.auth + "/auth/v1/token?grant_type=password", None, {"email": "alice@synthetic.invalid", "password": "rehearsal-pw-1"})["access_token"]
env = [e["id"] for e in call("GET", a.new + "/me", t)["workspaces"][0]["environments"] if e["key"] == "dev"][0]
def stream(base, got, ready):
    req = urllib.request.Request(base + "/sdk/stream", headers={"X-Helios-SDK-Key": keys["KEY_DEV"]})
    try:
        with urllib.request.urlopen(req, timeout=8) as r:
            ready.set()
            for line in r:
                if line.startswith(b"data:"): got.append(line.decode().strip()[:80]); return
    except Exception as e: got.append("ERR " + str(e)[:60]); ready.set()
edits = {"new": lambda n: call("PATCH", f"{a.new}/environments/{env}/flags/flag-004", t, {"enabled": n % 2 == 0}),
         "old": lambda n: call("PATCH", f"{a.old}/environments/dev/flags/flag-004", t, {"enabled": n % 2 == 1})}
for reader, writer in [("old", "old"), ("new", "new"), ("old", "new"), ("new", "old")]:   # the first two are controls: same side
    got, ready = [], threading.Event(); th = threading.Thread(target=stream, args=(getattr(a, reader), got, ready)); th.start(); ready.wait(5); time.sleep(0.5)
    edits[writer](int(time.time() * 1000)); th.join(9)
    ok = bool(got) and not got[0].startswith("ERR")
    print(f"stream on the {reader.upper()} backend, flag edited through the {writer.upper()} backend -> " + ("update received" if ok else "NOTHING received in 8 s: the update is lost until the SDK reconnects or polls") + ("  (control)" if reader == writer else ""))
