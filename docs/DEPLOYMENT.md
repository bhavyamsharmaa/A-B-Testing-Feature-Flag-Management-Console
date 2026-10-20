# Deploying the backend

How to run the API and its jobs on Render or on a container platform such as Cloud Run, and the things that behave
differently once there is more than one instance. Everything here is documentation plus the startup checks that enforce
it; nothing in this repository deploys anything. Platform flags are written from the platforms' documented behaviour and
should be checked against their current documentation when used.

## 1. What runs, and what it keeps

**One image, several commands** (`backend/Dockerfile`, build context `backend/`): the API (default), `scheduler`, `outbox`,
`purge-workspaces`, `purge-events`, `segment-usage`. Statically linked Go, a `scratch` base with only the CA certificates and a passwd entry,
user 65532 (never root), no shell, no source, no tests, no `.env`. Size, measured on the staged copy of the final stage for
linux/arm64: **72.7 MiB uncompressed, about 25 MiB compressed** (the API binary is 14.4 MiB; the other five commands and the
entrypoint are 6-11 MiB each). It could not be built with Docker where this was written (no container runtime was installed);
the figure comes from `tools/image/stage.sh`, which runs the Dockerfile's own build commands and lays the result out the same
way. Build it with `docker build -t helios-backend:local backend/`, and replace this paragraph with `docker images`'s number.
The Go version is pinned to an exact patch release (`GO_VERSION` in the Dockerfile); to pin by digest as well, look the
digest up with `docker buildx imagetools inspect golang:<tag>` and append `@sha256:…`.

**Statelessness.** The API keeps no session, file or queue on disk. Everything durable is in Postgres. Redis holds nothing:
it only carries notifications (flag changes, revocations) and the API works without it (changes then reach other instances when
their cache entries expire). Each instance has in-memory caches (SDK keys, flag configuration, API tokens, rate-limit
buckets) that are rebuilt on demand, so an instance can be stopped or added at any moment. The jobs hold no state between runs.

**Checks you can run on any artifact:**

```bash
tools/image/stage.sh                  # build the final stage's contents without Docker and inspect them (tools/imagecheck)
tools/image/smoke.py                  # entrypoint, production refusal, the API, every job, SIGTERM; against the local stack
docker export $(docker create IMAGE) | (cd tools/imagecheck && go run . -tar -)   # the same inspection of a real image
tools/image/smoke.py --image IMAGE    # the same smoke test against a container (untested here, see its header)
```

`imagecheck` fails if the image holds an environment file, a key or credentials, source, a test or test data, a setuid file,
a world-writable file, or anything outside `/app`, `/etc/passwd`, `/etc/group` and the CA bundle. A Go test reads the
Dockerfile and `.dockerignore` and fails if a base image is not pinned, the final stage can run as root, anything but
`go.mod`, `go.sum`, `cmd` and `internal` is copied from the build context, or the ignore file stops excluding secrets,
tests and test data.

## 2. Listening, shutting down, time limits

**Address.** `LISTEN_ADDR` (host:port) wins, then `PORT` (every interface), then `:8080`. Render and Cloud Run set `PORT`;
the image defaults to 8080. A value that is set but wrong stops the start with a message naming the setting. In production mode listening on
loopback is refused (nothing outside the container could reach it).

**Shutdown.** On `SIGTERM` (or `SIGINT`) the API, in this order: (1) `/readyz` answers 503 `shutting_down` from that instant,
new streams are refused (503 with `Retry-After`), open streams get `event: reconnect` and are closed, job endpoints stop claiming
work; (2) waits `SHUTDOWN_READY_DELAY` (default 1 s, at most a quarter of the budget) so a load balancer that polls readiness
has seen it; (3) stops accepting and lets in-flight requests finish; (4) if `SHUTDOWN_TIMEOUT` (default **8 s**, 1-55 s) from the signal is
spent first, closes what is left and exits anyway. Connections that were accepted but never sent a request are closed at
once (net/http would otherwise treat them as busy for five seconds). Measured with the real binary and a real signal:
a request whose insert took 700 ms completed and was stored, the stream was told to reconnect, and the process exited 0
**582 ms** after the signal (test `TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget`). Both platforms send `SIGKILL` after their
grace period (about 10 s on Cloud Run; set Render's to match or longer), so keep `SHUTDOWN_TIMEOUT` under it. `/healthz` stays 200
during a shutdown (the process is alive); only `/readyz` flips. Jobs: docs/OPERATIONS.md, "Shutdown".

**Probes.** `/healthz` says the process is alive and checks nothing else, so a database outage never gets a healthy instance restarted.
`/readyz` checks the database and Redis with a one-second timeout each and reports fixed words only (`ok`, `degraded`, `unavailable`,
`shutting_down`; no address, version or error text). A Redis that is down makes the instance `degraded`, still ready, because
`/evaluate` does not need it; `READY_REQUIRE_REDIS=true` makes it not ready. Use `/readyz` for readiness and traffic decisions (Cloud Run
startup probe, a load balancer's health check) and `/healthz` for liveness (anything that restarts a container).
**On Render the single health check path decides both: use `/healthz`**, because Render restarts an instance whose check keeps failing, and
restarting every instance during a database blip only adds reconnects to the outage.

**Time limits.** Header read 5 s, whole request read 30 s, idle keep-alive 60 s, **no write timeout** (a stream's response lasts as long as the
connection). `/sdk/stream` connections therefore end only when the client leaves, the key is revoked, the server shuts down, or the
platform's own maximum request duration cuts them (Cloud Run: the service's request timeout, 5 minutes by default and at most 60;
set it to the maximum). SDKs must reconnect on end of stream (browsers' `EventSource` does by itself); the server tells them when it is
the one leaving. Each `/internal/*` job call is capped at 55 s.

## 3. Instances, concurrency and CPU

* **Minimum instances: at least 1 in production.** A cold instance connects the pool, fetches the auth keys and warms its caches
  (a second or two); do not make a user wait for it. Render: use a paid instance type that does not sleep when idle.
* **Cloud Run: allocate CPU always (`--no-cpu-throttling`).** With throttling, CPU is frozen between requests, which freezes the Redis subscriptions
  that invalidate the configuration cache and drop revoked keys; entries then heal only by their TTL (10 s, 15 s for keys). Always-allocated CPU keeps them live.
* **Concurrency.** A stream is a request that never ends: each takes one slot of the platform's per-instance concurrency. Measured on the load
  test (docs/PERFORMANCE.md): 500 streams cost 2,500 goroutines and almost no CPU. Set concurrency (Cloud Run) to a few hundred, and size instances for
  `/evaluate`'s CPU, not for streams. Per SDK key the API allows `STREAMS_PER_KEY` (default 100) streams per instance.
* **Scaling out** multiplies the per-instance things in section 4.

## 4. The four caveats of more than one instance

### 4.1 Rate limits are per instance

The limiter lives in each process's memory. With N instances the effective limit of every rule is **up to N times** what is
configured: 100 `/evaluate` per second per SDK key becomes up to 100N; the brute-force limits (`sdk_invalid`, 0.5/s per client IP, burst 30) and the
per-user limits on workspace writes, invites and token creation multiply the same way. A client is spread across instances by the load balancer, so
in practice the multiplier is between 1 and N. Consequences: abuse protection is N times looser, never stricter; no legitimate user is ever refused
because of another instance.

Options, in order of preference: (a) **divide the limits by the instance count** with `RATE_LIMITS` (per second/burst per rule, e.g. `sdk_key=33/170`
for three instances) and run a fixed or narrow range of instances; (b) put a limiter in front (the platform's WAF, Cloudflare, a gateway), which sees all
traffic once; (c) accept it, if N is small and the defaults are generous, which today they are; (d) a shared limiter in Redis is possible but not built: it adds a Redis
round trip to every request, which is the cost the hot path was just relieved of, and it makes Redis a dependency of availability. Set `TRUSTED_PROXY_HOPS`
(Render's `RENDER=true` defaults it to 1) or every client shares the proxy's address and one bucket.

### 4.2 The configuration cache is per instance

Each instance caches the flag configuration it serves. A change is announced on Redis and every instance drops its entry within milliseconds; if the announcement is lost
the entry expires after `CONFIG_CACHE_TTL` (10 s). Worst cases per kind of change, measured and asserted in tests: docs/PERFORMANCE.md, "Staleness bounds"
(a kill is on the fast path and never waits for the TTL when Redis is healthy). Set `CONFIG_CACHE_TTL=off` to read the database on every request.

### 4.3 Pool size times instances must fit the pooler

Every instance holds up to `DB_MAX_CONNS` Postgres connections (default: the CPU count, between 4 and 10), and logs `database pool: at most N connections from
this instance` at startup (never the URL). The total that must stay under the pooler's limit is

    max_instances x DB_MAX_CONNS   +   concurrently running jobs x their pools (up to 10 each)   +   anything else that connects (the console's tools, a migration)

Supabase's pooler has a pool size and a client-connection limit that depend on the plan and are set in the project's database settings; read them there and
do the sum. Worked example with invented numbers: a pooler pool of 15 and at most 2 API instances leaves 15 - 2 x 5 = 5 for jobs and humans, so set `DB_MAX_CONNS=5`.
At 5 connections an instance still sustains thousands of `/evaluate` per second on the load test's hardware because evaluation is cache-fed; `/track` batches share the
same pool and are bounded to `DB_MAX_CONNS - 2` at once (`TRACK_MAX_WRITERS`), answering `503 TRACK_BUSY` with `Retry-After` beyond a 500 ms wait (`TRACK_MAX_WAIT`).
The driver uses the simple query protocol (it works through Supabase's transaction pooler); do not add a second pooler in front without checking that.

### 4.4 Required settings, and settings that must never be on

**`HELIOS_ENV=production`** (the image sets it) switches on the startup checks. In production mode every command (`api`, `outbox`, the other jobs, each for the settings it uses)
**refuses to start**, before it connects to anything, if a development-only setting is on or something production needs is missing. Each problem is logged
by setting name only (never a value), then the process exits 1. Without `HELIOS_ENV`, or with `development`, nothing is refused (a laptop needs nothing). A value that is neither
(`prodution`) is an error, not development.

| required in production | why |
|---|---|
| `DATABASE_URL`, with `sslmode=require` or stronger | the default is a local database without TLS |
| `SUPABASE_URL` (https) | the auth keys come from it |
| `EXPOSURE_TOKEN_KEY` (`<id>:<32+ random bytes>`) | otherwise each instance signs with a random key of its own |
| `CORS_ALLOWED_ORIGINS` (https origins of the console) | otherwise the console cannot call the API from a browser |
| `PUBLIC_CONSOLE_URL` (https) | the link in invite emails |
| `REDIS_URL` (use `rediss://` for a hosted Redis) | flag changes reach other instances at once; `/sdk/stream` needs it (warned, not refused) |
| `SCHEDULER_SECRET`, `OUTBOX_SECRET`, `PURGE_SECRET` (32+ characters each), if the jobs are driven by HTTP | otherwise those endpoints do not exist |
| `WEBHOOK_SECRET_KEY` (`openssl rand -base64 32`), `MAIL_MODE=smtp` and the `SMTP_*` settings | webhooks and invite email (warned if missing) |
| `TRUSTED_PROXY_HOPS` (1 behind one proxy) | per-client rate limits (warned) |

| **never** in production (each is refused) | what it would do |
|---|---|
| `WEBHOOK_ALLOW_INSECURE_LOCAL` | webhooks could target http and loopback addresses: the guard against server-side request forgery is off |
| `MAIL_ALLOW_INSECURE_SMTP`, `SMTP_TLS=none` | email sent without encryption |
| `MAIL_SINK_DIR` | every message, invite links included, written to files |
| `RATE_LIMIT_DISABLED=true` | every rate limit off |
| `EMAIL_CONFIRMATION=off` | unconfirmed addresses can create workspaces and accept invites |
| `EXPOSURE_TOKEN_KEY` unset | a random per-process key (see above) |
| `DATABASE_URL` unset or `sslmode=disable` | local, unencrypted |
| `SUPABASE_URL` over http, a non-https or localhost `CORS_ALLOWED_ORIGINS` entry, `*` | development auth server, local pages |
| `LISTEN_ADDR` on loopback | unreachable |
| `MAIL_MODE=sink` (default) | invite email goes nowhere: warned, not refused (the invite link still works if the inviter shares it) |
| `PPROF_ADDR` | profiling on (loopback only): warned |

The list lives in code (`config.DevFlags`, `config.ProductionProblems`), and tests cover each line. `devauth` (the stand-in for Supabase Auth) is never in the image and refuses to start against a database that is not local.

## 5. Render

A *Web Service* from the Dockerfile (`backend/Dockerfile`, docker context `backend`); health check path **`/healthz`**; environment from the table above (secrets in Render's secret store, never in the repository);
`HELIOS_ENV=production` is already in the image; `TRUSTED_PROXY_HOPS` defaults to 1 on Render. One *Cron Job* per row of docs/OPERATIONS.md with the command shown there. Zero-downtime deploys work with the
shutdown sequence in section 2 (the old instance goes not-ready, its streams reconnect to the new one).

## 6. Cloud Run (or another container platform)

```bash
# written from the platform's documented behaviour; not run here, and flags change: check `gcloud run deploy --help`
gcloud run deploy helios-api --image REGION-docker.pkg.dev/PROJECT/REPO/helios-backend:TAG \
  --port 8080 --min-instances 1 --max-instances N --concurrency 200 --timeout 3600 --no-cpu-throttling \
  --set-env-vars HELIOS_ENV=production,DB_MAX_CONNS=5,RATE_LIMITS=… --set-secrets DATABASE_URL=…,EXPOSURE_TOKEN_KEY=…
# startup probe: /readyz   liveness probe: /healthz
gcloud run jobs create helios-scheduler --image … --args scheduler       # and outbox, purge-workspaces, purge-events
```

Jobs are Cloud Run Jobs started by Cloud Scheduler (or the HTTP endpoints called by it), using the cadences in docs/OPERATIONS.md. A job's `SIGTERM` grace is short: they release what they hold (docs/OPERATIONS.md).

## 7. Before the first deploy

1. `tools/image/stage.sh && tools/image/smoke.py` on the commit you will build.
2. Build the image; run `imagecheck` on it; record `docker images`.
3. Apply the migrations in order (docs/ROLLOUT.md), then deploy.
4. Start with one instance and the jobs; watch `readyz`, the startup log (it prints the pool size, the cache TTL, the shutdown budget and every production warning), then scale and revisit the limits in section 4.
