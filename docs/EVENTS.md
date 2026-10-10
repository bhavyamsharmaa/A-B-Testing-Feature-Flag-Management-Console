# Event storage: retention, purging, indexes, partitioning

Events are what an experiment's results are computed from. This page records the
decisions behind the `events` table (migration `0008_event_ingestion.sql`) and
`POST /track`.

## What a row is

| Column | Meaning |
|---|---|
| `workspace_id`, `environment_id` | From the SDK key, never from the request. Composite foreign keys keep them consistent with the experiment and each other |
| `event_id` | Client-chosen, unique per `(workspace, environment)`: the retry key |
| `event_name` | A metric's event name, or `$exposure` |
| `subject_key` | Opaque string chosen by the customer (may be personal data) |
| `experiment_id`, `variation_id` | Nullable. `variation_id` is only set on `$exposure` rows |
| `value`, `properties` | Optional finite number; optional JSON object up to 2 KB |
| `occurred_at` / `created_at` | The client's timestamp / when the server stored it |

There is **no server-side join of an event to a variation at write time**. Results
join each event to its subject's exposure (`$exposure` is unique per subject and
experiment, so the join is one row).

## Retention: 90 days

`track.Retention` (90 days) is the window. `/track` refuses events older than it
(they would be purged at once) and more than 5 minutes in the future.

Purging is implemented as `internal/platform/eventpurge` and the admin command
`cmd/purgeevents` (a CLI, not an HTTP route, and **not scheduled**):

```bash
DATABASE_URL=... go run ./cmd/purgeevents -dry-run          # counts only
DATABASE_URL=... go run ./cmd/purgeevents                   # 90 days (HELIOS_EVENT_RETENTION_DAYS or -retention-days; minimum 7)
DATABASE_URL=... go run ./cmd/purgeevents -max-batches 200 -pause 100ms
```

Rules: events older than the retention window are deleted in small batches
(default 5,000 rows per statement, one environment at a time so each batch is a
range scan of `idx_events_time`). **Every event of an experiment that is running,
or that stopped inside the window, is kept regardless of age.** This is broader
than "exposures only": deleting a running experiment's old conversions while
keeping its exposures would silently bias its results. Events of experiments that
stopped before the window, and events that belong to no experiment, go.
The batch locks its rows with `FOR UPDATE SKIP LOCKED`, so two runs (or a run and
anything else) never wait for each other, double-delete or deadlock, and the
command can be repeated freely. It prints counts only, never an event's content.
Deleting an experiment or a workspace removes its events by `ON DELETE CASCADE`.

At the default limit (6,000 events per key per minute) one busy key can add about
8.6 million rows a day, so the purge must be scheduled before real traffic.

### Scheduling it later (not done)

The API cannot do this itself: on Cloud Run an instance may not exist at the time
a timer would fire, may be one of many, and is frozen between requests. The command
is built to be run from outside:

* **Cloud Run Job** running the same image with the command
  `purgeevents -max-batches 500 -pause 100ms`, triggered daily by **Cloud
  Scheduler**. `-max-batches` bounds one run (`Truncated: true` in the log line
  means more is left; the next run continues), and overlapping runs are harmless.
* Alternatively a scheduled GitHub Actions or `pg_cron` job; `pg_cron` would need
  the same predicate in SQL.
* Alert on the job failing, and watch the table size (`pg_total_relation_size`);
  when it passes about 50 million rows, plan partitioning (below).

## Index plan

Every index leads with `workspace_id`.

| Index | Serves |
|---|---|
| `events_event_id_unique (workspace, environment, event_id)` | idempotent retries |
| `events_one_exposure_per_subject (workspace, environment, experiment, subject) WHERE event_name='$exposure'` | exposure dedup; first wins |
| `idx_events_results (workspace, experiment, event_name, variation_id)` | per-variation counts of an event (exposures, conversions) |
| `idx_events_subject (workspace, experiment, subject_key)` | joining a subject's events to their exposure |
| `idx_events_time (workspace, environment, occurred_at)` | time series, the purge |

The results phase should start from these and measure before adding more: each
index costs write throughput on a table that is written on every request.

## Partitioning (not implemented)

Introduce it when `events` passes roughly 50-100 million rows or the purge's
`DELETE` becomes noticeable, **before** the table is large: converting a big table
is a rewrite. Shape: `PARTITION BY RANGE (occurred_at)`, one partition per week or
month, created ahead by the same job that drops expired ones. Two consequences to
plan for:

* every unique index on a partitioned table must include the partition key, so
  `events_event_id_unique` and `events_one_exposure_per_subject` would have to
  include `occurred_at`. That weakens dedup (a retry with a different timestamp
  would no longer collide), so the SDK must send a stable timestamp per event and
  exposure dedup would move to a small separate table
  `(workspace, environment, experiment, subject) -> variation, first_seen`;
* the composite foreign keys cannot point *into* a partitioned table, which is fine
  (nothing references `events`).

## Ingestion write path

`track.Writer` has one method, `Insert(ctx, scope, batch)`. Today it is a single
`INSERT ... SELECT FROM unnest(...) ON CONFLICT DO NOTHING RETURNING event_id` per
request. A queue-backed writer would publish the batch and let a worker do the
same insert; the handler only needs the set of ids that were new (for the
`accepted` / `duplicate` result), which a queue would have to answer
asynchronously (return `accepted` for everything not yet known to be a duplicate).

## Privacy

Subject keys and properties are customer data. They are never logged (a test in
`internal/integration` and the credential check in `internal/tenancycheck`
enforce it), error messages returned to the client are fixed texts that never
quote the event, and the log line for a failed insert carries only the SQLSTATE
and constraint name. Deleting a workspace deletes its events.
