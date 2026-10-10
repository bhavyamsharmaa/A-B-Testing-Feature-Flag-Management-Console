# What the Helios SDKs need for tracking and exposures

The helios-go SDK (a separate repository) is **not** changed by this work. This
page is the specification for that separate task. The server side is
`POST /track` and the `experimentId` field of `/evaluate` (see `api/openapi.yaml`).

## The flow an application sees

1. `Evaluate` / `BoolVariation` ... returns the value **and** `ExperimentID`
   (already a field of `helios-go`'s `Evaluation`; the server now fills it in).
2. When the application actually *shows* the variation (not merely evaluates it),
   it calls an exposure method. The SDK queues an `$exposure` event.
3. Later, when the user does something measurable, the application calls `Track`.
   The SDK queues the event.
4. A background goroutine sends batches.

## API surface

```go
// Expose records that subject was shown the variation of the experiment in ev.
// A no-op when ev.ExperimentID == "" (not in an experiment) or the SDK is closed.
func (c *Client) Expose(subjectKey string, ev Evaluation)

// Track records an event. Options: WithValue(float64), WithExperiment(id),
// WithProperties(map[string]any) (<= 2 KB), WithTime(time.Time).
func (c *Client) Track(subjectKey, name string, opts ...TrackOption)

// Flush sends everything queued and waits (bounded by ctx).
func (c *Client) Flush(ctx context.Context) error

// Close flushes (bounded) and stops the sender. Call it on shutdown.
func (c *Client) Close(ctx context.Context) error
```

`Expose` sets `name:"$exposure"`, `experimentId` and `variationId` from the
`Evaluation`. `Track` with an experiment id attributes the event to that
experiment; without one it is stored but belongs to none.

## Event ids and retries (the important part)

* The **SDK generates the event id** (a UUIDv4 or `<random>-<counter>`; 1-128
  characters of letters, digits, `_ . : -`) **when the event is queued**, never when
  it is sent. Every retry of the same event, in the same or a later batch, sends the
  same id; the server then answers `duplicate` and stores nothing twice.
* The SDK also stamps `timestamp` at queue time (RFC 3339), not at send time, so a
  retried event keeps its time and old events stay inside the 90-day window.
* Exposures are additionally deduplicated by the server per subject and experiment
  (first wins), so the SDK **may** also drop repeated exposures for the same
  `(subject, experiment)` locally (a small bounded LRU) to save bandwidth. It must
  not rely on this for correctness.

## Batching

* Queue in memory, bounded (default 10,000 events). When full, drop the **oldest
  non-exposure** event first and count the drops (expose a `Dropped()` counter).
  Never block the caller and never panic.
* Send when 100 events are queued (the server maximum), or every 5 seconds
  (configurable), or on `Flush` / `Close`. A request body is at most 256 KB: split
  earlier if the batch would exceed it.
* One sender goroutine; `Close` is idempotent.

## Reading the response

`200` with `results` (one per event, in order). Per event:

| status | meaning | SDK action |
|---|---|---|
| `accepted`, `duplicate` | stored | done |
| `rejected` `INVALID_EVENT`, `INVALID_VARIATION` | the event is wrong | drop it, count it, log the code (never the event) |
| `rejected` `EXPERIMENT_NOT_FOUND`, `EXPERIMENT_NOT_RUNNING` | the experiment is gone or stopped | drop it |

Whole-request outcomes:

| response | SDK action |
|---|---|
| `400`, `413` | a bug (bad batch); drop the batch, log once |
| `401` | the key is invalid or revoked; stop sending, surface an error to the application's error handler, keep the queue bounded |
| `429` | wait `Retry-After` seconds (plus jitter), then resend the **same** batch |
| `503` `TRACK_UNAVAILABLE`, network errors, `5xx` | nothing was stored; retry the same batch with exponential backoff (start 1 s, double, cap 60 s, full jitter) |

Cap total retry time per batch (for example 10 minutes), after which the events
are dropped and counted.

## Shutdown

`Close(ctx)` must: stop accepting new events, send what is queued (respecting
`ctx`), and return. An application that never calls `Close` loses at most the last
flush interval of events. Document that for short-lived programs (CLI jobs,
serverless) `Flush` must be called before exit.

## Privacy

The SDK must never log subject keys, event names with values, or properties, and
should document that subject keys are sent to Helios as given: customers who treat
user ids as personal data should send a stable pseudonymous id.

## Tests the SDK needs

Retry sends identical ids and timestamps; `429` honours `Retry-After`; a full queue
drops the oldest non-exposure event; `Close` flushes; a `401` stops the sender; no
event content in logs; the race detector passes (`-race`).
