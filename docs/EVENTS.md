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

**Purging is not implemented yet.** When it is: a daily job runs

```sql
DELETE FROM events WHERE occurred_at < now() - interval '90 days' AND ...
```

in batches (`LIMIT`ed by primary key, a few thousand rows at a time, so no long
lock and bounded WAL), or, once partitioned, drops whole partitions. Exposures
of an experiment that is still running must be kept however old they are (a
running experiment older than 90 days would lose its denominators); the purge
therefore skips `$exposure` rows of `running` experiments. Deleting an experiment
or a workspace removes its events by `ON DELETE CASCADE`.

Until the job exists the table only grows: at the default limit (6,000 events per
key per minute) one busy key can add about 8.6 million rows a day, so the purge
and the partitioning decision below must precede any real traffic.

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
