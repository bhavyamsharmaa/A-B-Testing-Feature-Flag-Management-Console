#!/usr/bin/env python3
"""Smoke test of the shipped artifact: the entrypoint, the API and every job command, against the LOCAL stack only.

    tools/image/stage.sh                     # build the artifact (the image's contents, and a copy for this machine)
    tools/image/smoke.py                     # run it from that staged copy (no container runtime needed)
    tools/image/smoke.py --image NAME        # run it from a container image instead (needs docker; see below)

What it does, in order, each step a PASS or FAIL line:
  1. the entrypoint lists its commands and refuses an unknown one;
  2. in production mode (HELIOS_ENV=production) the API REFUSES to start with development settings, before connecting;
  3. the API starts (development mode, because the local auth stand-in is http), /healthz and /readyz answer;
  4. a user signs up, makes a flag, an SDK key, evaluates, tracks an event, and the console-facing /me answers;
  5. every job command runs once and exits 0: scheduler, outbox, purge-workspaces, purge-events (dry run), segment-usage;
  6. SIGTERM: the API goes not-ready, an open stream is told to reconnect, and the process exits 0 inside its budget.

It needs the local Postgres and Redis (backend/scripts/local_stack.sh start), refuses any non-local database, uses its own
database (helios_smoke, rebuilt each run) and its own auth stand-in on 127.0.0.1:54323, and prints no secret.

--image mode runs the same steps with `docker run --network host`. It exists because the brief asks for the smoke test
against the container; it could NOT be exercised where it was written (no container runtime was installed), so treat it as
untested until it has run once. On macOS `--network host` reaches the VM, not the Mac: run it on Linux, or point
HELIOS_PG_HOST/HELIOS_REDIS_HOST at addresses the container can reach.
"""
import argparse
import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

HOME = os.path.expanduser("~")
BASE = os.environ.get("HELIOS_LOCAL_DIR", os.path.join(HOME, "helios-local"))
REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
STAGE = os.path.join(BASE, "image-stage", "host")
DB = "helios_smoke"
PG_HOST, PG_PORT = os.environ.get("HELIOS_PG_HOST", "127.0.0.1"), os.environ.get("HELIOS_PG_PORT", "55432")
REDIS_HOST, REDIS_PORT = os.environ.get("HELIOS_REDIS_HOST", "127.0.0.1"), os.environ.get("HELIOS_REDIS_PORT", "56379")
AUTH_PORT = 54323

fails = 0


def check(ok, what, detail=""):
    global fails
    print(("PASS  " if ok else "FAIL  ") + what + (("  -- " + detail) if (detail and not ok) else ""))
    if not ok:
        fails += 1
    return ok


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def http(method, url, body=None, headers=None, timeout=10):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode())
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode(errors="replace")
    except Exception as e:  # connection refused and the like
        return 0, str(e)


class Runner:
    """Starts the artifact's commands either from the staged directory or from a container image."""

    def __init__(self, image):
        self.image = image

    def argv(self, command, env, name=None):
        if self.image:
            args = ["docker", "run", "--rm", "--network", "host"] + (["--name", name] if name else [])
            for k, v in env.items():
                args += ["-e", f"{k}={v}"]
            return args + [self.image] + command
        return [os.path.join(STAGE, "helios")] + command

    def env(self, extra):
        if self.image:
            return os.environ.copy(), extra
        e = {"PATH": os.environ.get("PATH", ""), "HELIOS_BIN_DIR": os.path.join(STAGE, "bin")}
        e.update(extra)
        return e, extra

    def run(self, command, extra, timeout=60):
        env, passed = self.env(extra)
        return subprocess.run(self.argv(command, passed), env=env, capture_output=True, text=True, timeout=timeout)

    def popen(self, command, extra, name=None):
        env, passed = self.env(extra)
        return subprocess.Popen(self.argv(command, passed, name), env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)


def psql(db, *sql):
    bindir = "/opt/homebrew/opt/postgresql@16/bin"
    exe = os.path.join(bindir, "psql") if os.path.exists(os.path.join(bindir, "psql")) else "psql"
    return subprocess.run([exe, "-h", PG_HOST, "-p", PG_PORT, "-U", "helios", "-d", db, "-qAt", "-v", "ON_ERROR_STOP=1"] + [a for s in sql for a in ("-c", s)],
                          capture_output=True, text=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", help="a container image to run instead of the staged binaries")
    args = ap.parse_args()
    if PG_HOST not in ("127.0.0.1", "localhost") and not args.image:
        sys.exit("refusing a database host that is not this machine")
    if not args.image and not os.path.exists(os.path.join(STAGE, "helios")):
        sys.exit("nothing staged: run tools/image/stage.sh first")
    r = Runner(args.image)
    db_url = f"postgres://helios@{PG_HOST}:{PG_PORT}/{DB}?sslmode=disable"

    # a fresh database
    psql("postgres", f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)", f"CREATE DATABASE {DB}")
    mig = subprocess.run([os.path.join(REPO, "backend", "scripts", "apply_migrations.sh")], capture_output=True, text=True,
                         env={**os.environ, "PGHOST": PG_HOST, "PGPORT": PG_PORT, "PGUSER": "helios", "PGDATABASE": DB})
    if not check(mig.returncode == 0, "database migrated", mig.stderr[-300:]):
        sys.exit(1)

    # 1. the entrypoint
    out = r.run(["commands"], {})
    check(out.returncode == 0 and all(c in out.stdout for c in ("api", "scheduler", "outbox", "purge-workspaces", "purge-events", "segment-usage")),
          "entrypoint lists the commands", out.stdout + out.stderr)
    out = r.run(["bash"], {})
    check(out.returncode == 2 and "unknown command" in out.stderr, "entrypoint refuses an unknown command (no shell to escape into)", out.stderr)

    # 2. production mode refuses development settings, and does so before touching the database
    prod_env = {"HELIOS_ENV": "production", "DATABASE_URL": db_url, "SUPABASE_URL": "http://127.0.0.1:1", "RATE_LIMIT_DISABLED": "true",
                "WEBHOOK_ALLOW_INSECURE_LOCAL": "true", "LISTEN_ADDR": "127.0.0.1:" + str(free_port())}
    out = r.run(["api"], prod_env, timeout=30)
    text = out.stdout + out.stderr
    check(out.returncode == 1 and "refusing to start in production mode" in text and "RATE_LIMIT_DISABLED" in text and "WEBHOOK_ALLOW_INSECURE_LOCAL" in text
          and "sslmode=disable" in text and "connected to Redis" not in text,
          "production mode refuses development settings before connecting", text[-400:])
    out = r.run(["scheduler"], {"HELIOS_ENV": "production", "DATABASE_URL": db_url}, timeout=30)
    check(out.returncode == 1 and "sslmode=disable" in out.stdout + out.stderr, "a job in production mode refuses a database without TLS", (out.stdout + out.stderr)[-300:])

    # an auth stand-in for this database
    auth = subprocess.Popen([os.path.join(BASE, "bin", "devauth"), "-addr", f"127.0.0.1:{AUTH_PORT}", "-public-url", f"http://127.0.0.1:{AUTH_PORT}"],
                            env={"PATH": os.environ.get("PATH", ""), "DATABASE_URL": db_url}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    api = None
    try:
        for _ in range(50):
            if http("GET", f"http://127.0.0.1:{AUTH_PORT}/dev/health")[0] == 200:
                break
            time.sleep(0.1)
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        api_env = {"HELIOS_ENV": "development", "DATABASE_URL": db_url, "SUPABASE_URL": f"http://127.0.0.1:{AUTH_PORT}",
                   "REDIS_URL": f"redis://{REDIS_HOST}:{REDIS_PORT}/2", "LISTEN_ADDR": f"127.0.0.1:{port}",
                   "SHUTDOWN_TIMEOUT": "5s", "SHUTDOWN_READY_DELAY": "300ms", "RATE_LIMIT_DISABLED": "true"}
        api = r.popen(["api"], api_env, name="helios-smoke-api")
        ready = False
        for _ in range(100):
            code, body = http("GET", base + "/readyz")
            if code == 200:
                ready = True
                break
            time.sleep(0.2)
        check(ready, "the API starts and /readyz answers 200", str(body))
        code, body = http("GET", base + "/healthz")
        check(code == 200 and body == "ok", "/healthz answers ok")
        code, body = http("GET", base + "/readyz")
        check(code == 200 and isinstance(body, dict) and body.get("checks", {}).get("database") == "ok" and body.get("checks", {}).get("redis") == "ok",
              "/readyz reports the database and Redis", str(body))

        # 4. a user, a flag, an SDK key, evaluate, track
        email = f"smoke-{int(time.time() * 1000)}@test.dev"
        http("POST", f"http://127.0.0.1:{AUTH_PORT}/auth/v1/signup", {"email": email, "password": "password123"})
        code, tok = http("POST", f"http://127.0.0.1:{AUTH_PORT}/auth/v1/token?grant_type=password", {"email": email, "password": "password123"})
        bearer = {"Authorization": "Bearer " + tok["access_token"]}
        code, me = http("GET", base + "/me", headers=bearer)
        env_id = next(e["id"] for e in me["workspaces"][0]["environments"] if e["key"] == "dev")
        check(code == 200, "/me answers for a signed-in user")
        flag = {"key": "smoke", "name": "Smoke", "variationType": "boolean", "variations": [{"id": "on", "value": True}, {"id": "off", "value": False}]}
        c1, _ = http("POST", f"{base}/environments/{env_id}/flags", flag, bearer)
        c2, _ = http("PATCH", f"{base}/environments/{env_id}/flags/smoke", {"enabled": True, "fallthroughVariationId": "on"}, bearer)
        c3, key = http("POST", f"{base}/environments/{env_id}/sdk-keys", {"name": "smoke"}, bearer)
        check((c1, c2, c3) == (201, 200, 201), "a flag and an SDK key are created", f"{c1} {c2} {c3}")
        sdk = {"X-Helios-SDK-Key": key["plaintext"]}
        code, ev = http("POST", base + "/evaluate", {"context": {"subjectKey": "u1"}, "flagKeys": ["smoke"]}, sdk)
        check(code == 200 and ev["evaluations"][0]["variationId"] == "on", "/evaluate serves the flag", str(ev))
        code, tr = http("POST", base + "/track", {"events": [{"id": "smoke-1", "name": "purchase", "subjectKey": "u1"}]}, sdk)
        check(code == 200 and tr.get("accepted") == 1, "/track stores an event", str(tr))
        # an edit is seen by the next evaluation (the configuration cache is dropped by the change)
        http("PATCH", f"{base}/environments/{env_id}/flags/smoke", {"fallthroughVariationId": "off"}, bearer)
        code, ev = http("POST", base + "/evaluate", {"context": {"subjectKey": "u1"}, "flagKeys": ["smoke"]}, sdk)
        check(code == 200 and ev["evaluations"][0]["variationId"] == "off", "an edit is visible to the very next /evaluate", str(ev))

        # 5. the jobs
        job_env = {"HELIOS_ENV": "development", "DATABASE_URL": db_url, "REDIS_URL": f"redis://{REDIS_HOST}:{REDIS_PORT}/2"}
        for name, cmd in [("scheduler", ["scheduler"]), ("outbox", ["outbox"]), ("purge-workspaces", ["purge-workspaces"]),
                          ("purge-events (dry run)", ["purge-events", "-dry-run"]), ("segment-usage", ["segment-usage"])]:
            out = r.run(cmd, job_env, timeout=120)
            check(out.returncode == 0, f"job {name} runs once and exits 0", (out.stdout + out.stderr)[-300:])

        # 6. SIGTERM: not ready at once, a stream is told to reconnect, a clean exit inside the budget
        sreq = urllib.request.Request(base + "/sdk/stream", headers=sdk)
        stream = urllib.request.urlopen(sreq, timeout=10)
        start = time.time()
        if args.image:
            subprocess.run(["docker", "kill", "--signal=SIGTERM", "helios-smoke-api"], check=False)
        else:
            os.kill(api.pid, signal.SIGTERM)
        flipped = False
        for _ in range(40):
            code, body = http("GET", base + "/readyz", timeout=2)
            if code == 503 and isinstance(body, dict) and body.get("status") == "shutting_down":
                flipped = True
                break
            time.sleep(0.01)
        check(flipped, "after SIGTERM /readyz says shutting_down at once")
        got = b""
        try:
            while True:
                chunk = stream.read(256)
                if not chunk:
                    break
                got += chunk
        except Exception:
            pass
        check(b"event: reconnect" in got, "the open stream is told to reconnect", got.decode(errors="replace"))
        try:
            rc = api.wait(timeout=8)
        except subprocess.TimeoutExpired:
            rc = None
        check(rc == 0 and time.time() - start < 6, f"the API exits 0 inside its budget ({time.time() - start:.1f}s)", f"exit {rc}")
        api = None
    finally:
        if api is not None and api.poll() is None:
            api.kill()
        auth.terminate()
        psql("postgres", f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)")
    print("\n%s" % ("all checks passed" if fails == 0 else f"{fails} check(s) FAILED"))
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
