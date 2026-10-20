# Operations runbook

What has to run besides the API, how often, what breaks when it does not, and what to look at. Nothing here is started by
the API: every job is a command that runs once and exits (a cron entry, a Cloud Run Job or a person starts it), and the
three that matter at second-and-minute scale also have a guarded HTTP endpoint, so a scheduler that can only make an HTTP
call can drive them. **A test fails if a `cmd/` directory or an `/internal/` route exists that this page does not list**
(`TestEveryCommandAndInternalRouteIsInTheRunbook`), so the list below is complete.

All commands read the same environment as the API (`DATABASE_URL`, and the ones named below), print counts and identifiers
only (never a payload, a name, an address or a secret), are safe to run again at once, and are safe to run from several places at
once. In the container image they are selected by name: `docker run IMAGE scheduler`; Render and Cloud Run Jobs take the
same text as the command.

## The jobs

| | **scheduler** | **outbox** | **purge-workspaces** | **purge-events** |
|---|---|---|---|---|
| **What it does** | Applies scheduled flag changes that have come due (through the ordinary edit code: approval rules, audit, versioning), marks ones overdue by more than the grace window as *missed* instead of running them late, cancels changes whose API token was revoked. | Sends what is queued in the outbox: webhook deliveries and invite emails, with retries and backoff, then deletes finished rows past their retention (delivered 14 days, dead 30). | Removes the data of workspaces whose 14-day deletion grace has ended, in bounded batches in dependency order; writes the tombstone and one platform audit entry; expires stale ownership offers. | Deletes events older than the retention window (90 days; never those of a running experiment or one stopped inside the window). |
| **Command** | `cmd/scheduler` (`-grace 15m -max-rows 200 -timeout 5m`) · image: `scheduler` | `cmd/outbox` (`-max-rows 500 -concurrency 8 -per-target 2 -timeout 5m`; one-off modes `-purge-only`, `-rekey`) · image: `outbox` | `cmd/purgeworkspaces` (`-batch 2000 -max-workspaces 20 -pause 0 -timeout 30m`) · image: `purge-workspaces` | `cmd/purgeevents` (`-retention-days 90 -batch -max-batches -pause -timeout 30m -dry-run`) · image: `purge-events` |
| **HTTP endpoint** | `POST /internal/scheduler/run` | `POST /internal/outbox/run` | `POST /internal/workspaces/purge` | none (command only) |
| **Secret header** | `X-Helios-Scheduler-Secret`, value `SCHEDULER_SECRET` | `X-Helios-Outbox-Secret`, value `OUTBOX_SECRET` | `X-Helios-Purge-Secret`, value `PURGE_SECRET` | n/a |
| **Cadence** | **every minute** (a change runs within a minute of its time) | **every minute** (webhooks are late by up to a minute otherwise) | **hourly** (a purge is not urgent; the grace period is 14 days) | **daily**, off-peak |
| **Safe concurrency** | any number: rows are leased (`FOR UPDATE SKIP LOCKED`); two runs never apply the same change | any number: leases; two runs never send the same row at once | any number: batches lock only their own rows; a second run skips locked rows | any number; idempotent |
| **If it stops** | Scheduled changes do not happen. After the grace window (15 min) the next run marks them *missed* (visible in the console, audited, a `scheduled_change.failed` webhook) rather than running them at a surprising time. Nothing else is affected. | Webhooks and invite emails are not sent; they queue and retry for about 22 hours (backoff 1m, 5m, 15m, 1h, 3h, 6h, 12h) and are then *dead*. Per workspace at most 1,000 webhook rows wait; beyond that the oldest are dropped (audited at most once every 10 minutes). Changes are never blocked. | Deleted workspaces stay *scheduled for deletion* and their data stays in the database past the promised 14 days (a privacy commitment, not a functional break); their slots stay counted toward the owned-workspaces limit; expired ownership offers stay listed until accepted or cancelled (acceptance re-checks expiry, so nothing wrong can be accepted). | The events table grows without bound (storage cost; the 90-day promise is broken). Experiment results are unaffected. |
| **Log lines to watch** | `scheduler: claimed N (reclaimed R), succeeded, failed, retrying, missed, skipped, released` · `scheduler: change <id>: <err>` · `scheduler: could not release …` | `outbox: claimed N (reclaimed R), delivered, retrying, dead, lease lost, purged, released` · `outbox: delivery <id>: <err>` · `outbox: WEBHOOK_SECRET_KEY is not set` | `purgeworkspaces: offers expired N, workspaces purged N, batches N, rows N` · `wspurge: purged workspace <id>` | `purgeevents: deleted N events older than <time> across E environments in B batches (truncated: bool)` |
| **Worry when** | `failed` or `missed` is more than 0; `reclaimed` is more than 0 often (workers die); no line for 5 minutes | `dead` rises; `retrying` stays high (a customer endpoint is down: not ours); `lease lost` is more than 0 often | `workspaces purged` is 0 for days while workspaces are past their date (query below); an error line | `truncated: true` repeatedly (raise `-max-batches` or run it more often) |

### Scheduling them

**Render cron job** (same image as the API, a separate Cron Job service per row, schedules are UTC):

| job | Command | Schedule |
|---|---|---|
| scheduler | `/app/helios scheduler` | `* * * * *` |
| outbox | `/app/helios outbox` | `* * * * *` |
| purge-workspaces | `/app/helios purge-workspaces` | `7 * * * *` |
| purge-events | `/app/helios purge-events` | `17 3 * * *` |

Give each the environment variables it needs and nothing more: `DATABASE_URL` for all; `REDIS_URL` for scheduler and
purge-workspaces (so running instances are told at once; without it they catch up within the config-cache TTL of 10 s);
`WEBHOOK_SECRET_KEY` and the SMTP settings for outbox. Render bills a cron job per minute of run time and a run that
starts every minute is busy for seconds: set the service's timeout a little above the command's `-timeout`.

**An external scheduled call** (anything that can send an HTTP request on a schedule: a hosted cron service, a GitHub Actions
`schedule:` workflow, a Cloud Scheduler job). Each call is one POST with the secret in the header and no body:

```bash
curl -fsS -X POST -H "X-Helios-Scheduler-Secret: $SCHEDULER_SECRET" https://<api-host>/internal/scheduler/run
curl -fsS -X POST -H "X-Helios-Outbox-Secret: $OUTBOX_SECRET"       https://<api-host>/internal/outbox/run
curl -fsS -X POST -H "X-Helios-Purge-Secret: $PURGE_SECRET"         https://<api-host>/internal/workspaces/purge
```

The answer is JSON counts (the same numbers as the log line). The endpoint does not exist (404) until its secret is set to at
least 32 characters; a wrong or missing secret, or any request carrying an `Origin` header, gets one fixed 401; each is rate
limited per client IP before the secret is checked; each run is capped at 55 seconds. GitHub Actions' `schedule:` runs
at best every 5 minutes and may be delayed under load, which is fine for `purge-workspaces` and acceptable for `outbox`, but too coarse
for `scheduler` (use a per-minute caller or a cron job). There is no endpoint for purge-events: run the command. Never put
the secrets in a URL, a log or a workflow file; use the platform's secret store.

### Doing it by hand

```bash
DATABASE_URL=… go run ./cmd/scheduler            # or: docker run --env-file … IMAGE scheduler
DATABASE_URL=… go run ./cmd/purgeevents -dry-run  # counts what would go, deletes nothing
```

One-off, not scheduled: `outbox -rekey` (re-encrypts webhook secrets after rotating `WEBHOOK_SECRET_KEY`; docs/WEBHOOKS.md),
`outbox -purge-only` (retention only), `cmd/segmentusage` (below).

## The other commands in `cmd/`

| | what | scheduled? |
|---|---|---|
| `cmd/api` | the HTTP API (image: `api`, the default) | it is the service |
| `cmd/segmentusage` (image: `segment-usage`) | read-only check to run **before rolling the backend back** to a version older than segments; lists the flags whose rules an older backend would silently ignore; `-fail-if-any` exits 1 | no |
| `cmd/mkkey` | mints an SDK or server key from the command line for an environment (shown once) | no; not in the image (use the console) |
| `cmd/devauth` | the development stand-in for Supabase Auth | never deployed; not in the image |
| `cmd/helios` | the image's entrypoint that picks one of the above | n/a |

## Shutdown: what a stop does to a job

Every command handles `SIGTERM` (what a platform sends first) and `SIGINT`: it stops claiming work, finishes what it has
begun (a scheduled change's transaction and a webhook delivery are short and are completed, not abandoned), **releases
whatever it claimed but had not started** (the row goes back to pending with the attempt it never made taken off, and the next run picks it up at once), and
exits. The log line then shows `released N`. If a job is killed outright (`SIGKILL`, a crash, the machine), its claimed rows
keep a lease of 2 minutes; the next run takes them over when it expires (`reclaimed N` in the log), after a bound on attempts
(scheduler 5, outbox 8) so a row that keeps killing its worker is eventually marked failed or dead instead of looping. The
API's job endpoints do the same when the API itself is shutting down (the run is cancelled; the request is drained). The API's
own shutdown (readiness first, streams told to reconnect, requests drained, all inside `SHUTDOWN_TIMEOUT`, default 8 s) is in
docs/DEPLOYMENT.md.

## Checking by hand

All of these read counts, nothing else (run them on a read-only connection):

```sql
-- scheduled changes waiting, running, overdue
SELECT status, count(*), min(run_at) FROM scheduled_changes WHERE status IN ('pending','running') GROUP BY status;
-- the outbox backlog and anything stuck under an expired lease
SELECT status, count(*), min(next_attempt_at) FROM outbox WHERE status IN ('pending','failed','delivering') GROUP BY status;
SELECT count(*) FROM outbox WHERE status = 'delivering' AND lease_expires_at < now();
-- workspaces past their purge date that are still there
SELECT count(*) FROM workspaces WHERE deleted_at IS NOT NULL AND purge_after < now();
-- the size of the events table
SELECT count(*), min(occurred_at) FROM events;
```

## Rolling a job out or back

A job is a command in the same image as the API, so it moves with it. Add them one at a time and watch the first runs' log
lines (the cadence column above is the target). Stopping a job is always safe: nothing is lost, work waits, and
the table above says what users would notice meanwhile.
