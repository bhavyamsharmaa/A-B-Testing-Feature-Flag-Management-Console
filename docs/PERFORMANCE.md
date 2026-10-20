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

## What was changed, and the numbers

Same machine, same seed, 20 s runs, 32 workers unless stated. "Before" is the baseline above (and a control run of the new
binary with the cache switched off, `CONFIG_CACHE_TTL=off`, which matched it: 18,700 req/s).

| | before | after | change |
|---|---|---|---|
| `/evaluate` throughput, 32 workers | 17,400-18,900 req/s | 42,100 req/s | **2.4x** |
| `/evaluate` p50 / p95 / p99 | 1.8 / 2.4 / 2.9 ms | 0.7 / 1.1 / 1.7 ms | -60% / -54% / -41% |
| `/evaluate` at 256 workers | 18,400 req/s, p99 17.6 ms | 36,300 req/s, p99 9.9 ms | 2.0x |
| `/evaluate` in the mixed run | 11,200 req/s, p99 5.2 ms | 25,400 req/s, p99 3.0 ms | 2.3x |
| API memory (RSS) while serving | 520-750 MB | 66-130 MB | **-85%** |
| `/track`, 25 events per batch | 4,540 req/s (113,400 events/s) | 4,300 req/s (107,900 events/s) | -5% (the price of the bound, see below) |
| stream edit-to-client p50 / p95 | 6.8 / 9.7 ms | 6.9 / 9.8 ms | none |

### 1. Flag configuration cache (`internal/dataplane/configcache`)

The profile put 96% of `/evaluate`'s handler time in two queries. The first, the flags' configuration, is cached per
instance; the second, segment membership, is not (below). `CONFIG_CACHE_TTL` (default 10 s; `off` disables it),
`CONFIG_CACHE_MAX_ENTRIES` (1,000 environments) and `CONFIG_CACHE_MAX_MB` (32).

**How it stays correct.** Every flag mutation already published to the flag-change channel after it commits. The cache listens
two ways: **in process, synchronously** (the publisher the handlers use invalidates this instance's cache before it forwards to
Redis, so an edit is visible to the next request on the instance that took it whether or not Redis is up), and from other
instances through **one pattern subscription** (`flags:*`) per instance. Any message for an environment drops that
environment's entry. A **kill** takes exactly that path and never waits for anything else. Every entry also expires after the TTL, so a
lost message heals itself. Beyond that:

* **No stale load wins.** A load that began before an invalidation is marked stale and not stored, and a request that arrives
  after the invalidation starts a new load instead of joining the old one. A message that carries a flag's version sets a
  floor: a snapshot below it is not stored. A snapshot older than the one held (by any flag's version) never replaces it.
* **One loader per environment** (stampede protection), detached from any one caller, so a client that gives up does not fail the
  others. 100 simultaneous cold requests make one database read (tested).
* **Bounded:** entries and estimated bytes, least recently used out first. **Errors are never cached.** An environment with no
  flags is cached for 2 s only.
* **Never for a revoked key or a deleted workspace.** The SDK key is verified first, so a revoked key never reaches the
  cache. Deleting a workspace announces on the revocation channel and every instance drops all its environments; the load query
  itself reads nothing for a workspace scheduled for deletion.
* **Something that did not publish before now does:** starting or stopping an experiment changes which experiment `/evaluate`
  reports (and issues exposure tokens for), so those two actions now publish to the flag channel.

**Segment membership is deliberately not cached.** It is per subject and unbounded, and a stale membership would silently
change who gets a feature. `/evaluate` still reads it live (one indexed probe, only when a requested flag's rules use a segment);
that probe is now 92% of what is left of the handler and the next thing to look at if `/evaluate` needs to go faster.

**Staleness bounds** (measured by `internal/integration/config_cache_test.go`, two instances over one database and a real Redis,
TTL one hour so only the mechanism under test can explain a fresh answer):

| change | the instance that took it | the other instance, broker healthy | broker down, message lost, or writer did not publish |
|---|---|---|---|
| flag edit, promotion | next request | 1-2 ms measured (asserted under 500 ms) | at most `CONFIG_CACHE_TTL` (10 s) |
| **kill** | next request | 1-2 ms measured (asserted under 500 ms), never the TTL | at most the TTL |
| flag deleted | next request | within 2 s asserted (1-2 ms measured) | at most the TTL |
| scheduled change run by `cmd/scheduler` (another process) | n/a | 1 ms measured | at most the TTL |
| experiment started or stopped | next request | within 2 s asserted | at most the TTL |
| SDK key revoked | next request | within 1 s (existing broadcast) | the SDK-key cache TTL (15 s) |
| workspace deleted | next request (key refused, entries dropped) | entries dropped within 2 s asserted | key cache TTL (15 s); config TTL (10 s) |
| direct SQL change (a writer that does not publish) | at most the TTL | at most the TTL | at most the TTL |
| segment membership or segment rules | not cached: always current | | |

When the Redis connection drops and comes back, the cache is flushed on every (re)subscribe, because messages in the gap are gone.

### 2. One Argon2id verification per key at a time (`internal/platform/apikey`)

SDK keys are verified with Argon2id (64 MiB per hash) and cached for 15 s. At expiry every request in flight used to run its
own hash. Now the first request verifies and the rest wait for its answer: 100 concurrent cold requests make one
verification (tested), the failure of a wrong key is shared and never cached, and a caller that gives up does not fail the
others. This is the whole of the memory difference above (520-750 MB to 66-130 MB under the same load). It also closes a small
race: a verification already under way when a key is revoked could put the revoked key back in the cache; an invalidation
counter now stops that, and a request arriving after the revocation does not join the earlier verification.

### 3. `/track`: a bound and a fast refusal (`internal/dataplane/track/gate.go`)

The profile said the write path has room: one multi-row `INSERT ... ON CONFLICT DO NOTHING` per batch (already how it
worked) at 113,000 events/s, so **no change to how events are written**, and nothing that could lose one. Exactly as before, a
batch is answered only after its `INSERT` has returned (tested with a 600 ms insert: no answer, and no row, until it
finishes). What was missing is a limit. Without one a burst queues inside the connection pool without bound, each batch is
answered after the SDK has given up, and the pool that `/evaluate` and the console share is full.

Now at most `TRACK_MAX_WRITERS` batches (default: pool size minus 2) touch the database at once. A batch that cannot get a turn
within `TRACK_MAX_WAIT` (500 ms) is refused immediately with **503 `TRACK_BUSY` and a `Retry-After` of 1-3 seconds** (spread, so
retries do not return together); nothing was written, the events carry ids, and the retry stores each exactly once (tested). A
refused batch is given its rate-limit tokens back so the retry is not charged twice. A database error now also carries
`Retry-After`. Default tuning, measured: half the pool cost 11% of `/track` throughput, pool minus two about 5% (4,300 against
4,540 req/s), which is the price of keeping two connections for everything else. If an asynchronous buffer ever looks
necessary: it would acknowledge events that are not yet durable, so a crash loses them (everything in the buffer, up to the
flush interval). That trade was not needed here and was not made.

### Decided NOT to optimise

* **Segment membership reads:** correctness over speed (above).
* **The `/track` insert and experiment lookup:** 15% and 8% of a handler that is nowhere near its limit; caching the lookup would
  need invalidation on experiment state for no measurable gain.
* **JSON decoding, GC settings, response encoding:** under 10% each and not the constraint.
* **Streams:** 500 streams are 2,500 goroutines and an idle CPU.
* **Argon2id parameters:** they are the security of the keys; only the number of concurrent hashes was changed.
* **Indexes:** every query in the profile is an index lookup and none is slow, so there is no migration for performance.
