# Performance

How fast the data plane is, how that was measured, and what was changed because of it. The tool is
[`tools/loadtest`](../tools/loadtest/README.md); everything runs against the local stack only.

**Numbers are relative.** They were taken on one laptop (Apple M5, 10 cores, 16 GB, macOS) with the load generator,
the API, Postgres 16 and Redis all on it, over loopback. They say how this code compares with this code after a change on
this machine; they are not a capacity promise for any hosted environment, where a database a few milliseconds away
changes everything (every query below would cost its round trip). Run-to-run variation is about ±10%, so differences
under 10% are noise.

## Setup

* One API instance (`cmd/api` built from the tree), `RATE_LIMIT_DISABLED=true` (the point is to measure the server, not the
  limiter), Postgres pool at its default (10 connections here, `max(4, CPUs)`), Redis on, own database.
* Seed: the workspace under test has 50 flags (the per-workspace limit): plain, 30% rollout, attribute rules, segment rules
  (`inSegment` on a 200-member segment) and 3-way multivariate rollouts; 2 segments; 5 running experiments (so some
  evaluations also issue exposure tokens). 20 filler workspaces of 50 flags add database volume (1,050 flags in all).
* `/evaluate`: 32 workers, 8 flag keys per request, random subjects with attributes. `/track`: 32 workers, batches of 25
  events (10% `$exposure`, some with values and properties). Streams: 500 open `/sdk/stream` connections while one flag is
  edited every second. Mixed: all three at once (32 evaluate workers, 8 track workers, 250 streams plus the edits).
  Each run is 20 s after a 2 s warm-up.

## Baseline (before any change in this work)

| scenario | workers | req/s | events/s | p50 ms | p95 ms | p99 ms | max ms | errors | API RSS MB | API CPU % | DB conns | goroutines |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `/evaluate` | 32 | 17,400 | | 1.8 | 2.4 | 2.9 | 152 | 0 | 520-750 | 280 | 12 | 81 |
| `/track` (25 events) | 32 | 4,540 | 113,400 | 6.8 | 8.3 | 10.1 | 116 | 0 | 525-580 | 220 | 11 | 81 |
| 500 streams + 1 edit/s | 500 | (500 opened in 20 s) | | connect 3.0 | 4.6 | 5.9 | 8 | 0 | 577 | 7 | 12 | 2,519 |
| mixed: `/evaluate` | 32 | 11,200 | | 2.7 | 3.8 | 5.2 | 187 | 0 | 615-640 | 240 | 12 | 1,348 |
| mixed: `/track` | 8 | 1,960 | 48,900 | 3.8 | 5.2 | 7.6 | 166 | 0 | | | | |
| mixed: 250 streams + edits | 250 | | | connect 4.2 | 8.4 | 9.6 | 10 | 0 | | | | |

Stream edit-to-client delay (an edit via the API until the event is read by an open stream): p50 about 7 ms, p95 10-17 ms
with 250-500 streams; p99 up to 10 ms alone and about 140 ms in the mixed run (the stream tail is garbage-collection and
scheduling noise on a machine that is also running the load). At 256 workers (saturation) `/evaluate` still
did 18,400 req/s (p99 18 ms) and `/track` 4,800 req/s (p99 73 ms): the queue sits in the connection pool and nothing fails.

## What the profiles say

`go tool pprof -top -cum` on 13 s CPU profiles taken during the runs:

* **`/evaluate`: the database dominates.** Of the handler's CPU time, `loadConfigs` (the flag/config/experiment query)
  is 51% and `loadSegments` 45%: 96% of the handler is two queries per request, almost all of it socket reads and writes
  to Postgres. The evaluation itself (`Evaluate`, rule matching, bucketing) is not visible in the profile. This is what a
  per-instance configuration cache removes (docs/PERFORMANCE.md "Config cache" below).
* **Memory has a stampede in it.** `argon2.initBlocks` is 13% of all bytes allocated in the evaluate run (1.2 GB) and the
  API's RSS sits at 520-750 MB with a 5 MB live heap. SDK keys are verified with Argon2id (64 MB per hash) and the verified
  key is cached for 15 s; when the entry expires, every request in flight misses at once and each runs its own hash, so
  32 workers means about 2 GB of transient allocation. On a 512 MB instance that is an out-of-memory kill every 15
  seconds under load. One verification per key at a time (single flight) removes it.
* **`/track`: the insert is not the problem.** One multi-row `INSERT ... SELECT FROM unnest(...) ON CONFLICT DO NOTHING`
  per batch is 15% of the handler, the experiment lookup 8%, JSON decoding 9%, the rest is protocol and socket time. At
  113,000 events/s on this machine the write path is nowhere near its limit, so there is no throughput case for changing
  how it writes; what is missing is a bound on how many batches may write at once and a fast refusal when the database is
  saturated instead of an unbounded queue (see "Track").
* **Streams are cheap.** 500 streams cost 2,500 goroutines (5 per stream) and a few MB; the CPU is idle.

## Decisions

See the sections below as each change lands: the config cache, the key-verification single flight, the track writer bound and
backpressure. Things deliberately NOT optimised are listed at the end.
