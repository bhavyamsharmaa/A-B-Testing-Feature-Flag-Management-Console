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

## The exposure token (signed exposures)

`/evaluate` returns `exposureToken` next to `experimentId` whenever the flag is in a
running experiment (and only then: other evaluations are unchanged). It proves the
server served this variation of this experiment to this subject.

* The SDK **must keep the token with the `Evaluation`** and put it on the `$exposure`
  event it queues for that evaluation: `"exposureToken": "<token>"`. `Expose(subject, ev)`
  does this from `ev.ExposureToken`. Do not parse, log or alter it.
* The token is bound to the exact subject, variation, experiment, environment and
  workspace, and expires after about 24 hours (server setting). It is **not** a secret
  about the subject (it contains only a hash-based signature) but it is a proof, so do
  not share it between subjects.
* If the SDK reports an exposure long after the evaluation (queued, offline), the
  token may have expired: keep the original evaluation time and expect an
  `EXPOSURE_TOKEN_EXPIRED` rejection for exposures older than the window; evaluating
  again and reporting then is the fix. Tokens are not refreshed by `/track`.
* Rejections (only when the server runs `EXPOSURE_VERIFICATION=required`):
  `EXPOSURE_TOKEN_MISSING`, `EXPOSURE_TOKEN_INVALID` (also used for a wrong subject,
  variation, experiment, or an unknown key), `EXPOSURE_TOKEN_EXPIRED`. They are
  permanent for that event: drop it and count it, do not retry the same event.
  Never send `exposureToken` on an event whose name is not `$exposure`.

### Server settings and key rotation

| Variable | Meaning |
|---|---|
| `EXPOSURE_VERIFICATION` | `off` (default: tokens are issued but ignored, old SDKs keep working), `report` (accept all, store whether each verified, results warn), `required` (reject exposures that do not verify) |
| `EXPOSURE_TOKEN_KEY` | **Secret.** `<id>:<secret>`: an id of 1-16 letters or digits and a secret of at least 32 bytes, e.g. `k1:` followed by `openssl rand -base64 48`. The same value on every instance |
| `EXPOSURE_TOKEN_KEY_PREVIOUS` | Optional, same format, a different id: only for rotation |
| `EXPOSURE_TOKEN_MAX_AGE` | How long a token is valid, default `24h` |

Without `EXPOSURE_TOKEN_KEY` a random development key is generated at startup (a log
line says so, without the value); its tokens do not survive a restart or work across
instances, so `required` refuses to start without a configured key.

Roll-out: `off` -> update SDKs to pass the token -> `report` (watch the
"unverified exposures" warning in the results fall to zero) -> `required`.

Rotation: (1) generate a new key with a new id; (2) set `EXPOSURE_TOKEN_KEY_PREVIOUS`
to the old `id:secret` and `EXPOSURE_TOKEN_KEY` to the new one on every instance and
deploy: new tokens are signed with the new key and tokens signed with the old key
still verify; (3) after `EXPOSURE_TOKEN_MAX_AGE` (24 h) remove
`EXPOSURE_TOKEN_KEY_PREVIOUS`. If a key leaks, set the new key and **no** previous
key at once: every outstanding token stops verifying and clients simply evaluate
again. The secret is never logged, returned or stored; keep it in the platform's
secret store, never in a file in the repository.

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
