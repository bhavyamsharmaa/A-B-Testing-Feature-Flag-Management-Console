# Promotion and scheduled changes

Two ways to change a flag's configuration that are not "open the console and edit":
**promotion** copies a flag's configuration from one environment to another, and
**scheduled changes** apply a change at a future time. Neither is a second edit path.
Both end in `flags.ApplyConfigChange` / `flags.KillFlag`, the functions behind
`PATCH /environments/{envId}/flags/{key}` and the kill route, so validation, the
production approval gate, the version bump, the audit entry and the Redis fan-out are
the ordinary ones. Rollout plan: `docs/ROLLOUT.md`, "Release 4". Schema: migration
`0011_scheduled_changes.sql`. API: `api/openapi.yaml`.

## One edit path

`ApplyConfigChange(tx, env, flagKey, actor, change, opts)` validates the change (rules,
rollout, fallthrough, segments that must exist), locks the segments the new rules name
`FOR SHARE` and then the config row `FOR UPDATE`, updates it with `version = version + 1` and
writes the `flag.update` audit row, all in the caller's transaction. The caller publishes the
Redis event after commit. The role check is the caller's, and each caller makes it its own way:

| Caller | Role check |
|---|---|
| `PATCH …/flags/{key}` | the route's guard: `rbac.FlagWrite` (editor; approver in production) |
| promotion apply | the same guard, on the **target** environment |
| scheduled change, when scheduled | the kind's requirement (`scheduling.RequirementFor`: kill = editor, everything else `FlagWrite`) |
| scheduled change, when executed | the same requirement, against the creator's **current** membership |

"Approver" is the workspace role owner or admin; there is no separate approval workflow, the role is
the gate.

## Promotion

Two stages, both on the **target** environment (`{envId}`), for one flag (`{key}`):

* **Preview** `POST …/flags/{key}/promotions/preview` `{sourceEnvironment, parts?}`: any role. The
  server compares the source and target configuration and returns, per part (`rules`, `rollout`,
  `fallthrough`, `enabled`), whether it is selected, whether it differs, and both values, plus
  `problems` (why it cannot be applied), `nothingToApply`, `canApply`, and the two versions.
  **By default `enabled` is not copied**: turning a flag on in production is a decision of its own.
  Ask for it explicitly with `parts: ["enabled"]`.
* **Apply** `POST …/flags/{key}/promotions` `{sourceEnvironment, parts?, expectedVersion, sourceVersion}`:
  needs what an edit of the target needs. Both versions come from the preview; if the target (409
  `VERSION_CONFLICT`) or the source (409 `SOURCE_CHANGED`) moved since, nothing is written. Never an
  overwrite of a concurrent edit.

Refused, with an explanation and nothing partly applied: source and target the same environment, a
source in another workspace (it is only looked up inside the target's workspace, so it is
"not found"), rules that name a segment which does not exist, rules or a rollout naming a variation
the flag does not have, a part list with an unknown part, nothing to copy. Segments are
workspace-level, so a rule naming one promotes unchanged. A flag's variations are shared by all its
environments, so they are always compatible; the variation check still runs, for data that is not.

The audit entry is the ordinary `flag.update`, whose "after" also carries `promotedFrom: <source
environment>`.

A "promote several flags" form was left out: it would need many config rows locked in one
transaction (a lock-order problem between two people promoting in opposite directions) and its own
console flow, and the brief made it optional.

## Scheduled changes

One flag, one environment, one future time. Kinds: `enable`, `disable`, `kill`, and `patch`
(`rollout`, `fallthroughVariationId` and/or `targetingRules`; the enabled state is the enable/disable
kinds). A row records its status, creator, run time (UTC), a creator-supplied idempotency key (unique
per workspace), the change, the flag configuration's version it was created against, the attempt
count, a result code and message, and timestamps.

**Status machine:** `pending → running → succeeded | failed | pending (retry)`; `pending → missed`;
`pending | running → cancelled`; `running → failed` (retries exhausted). `succeeded`, `failed`,
`missed` and `cancelled` are final.

**Limits** (`quota` and `scheduling`): at least **1 minute** ahead, at most **366 days** ahead, at most
**10** pending changes per flag and **100** per workspace (pending or running; finished ones do not
count; cancelling frees a slot). The caps are checked under the workspace row lock, so parallel
requests cannot pass both.

**Idempotency:** `idempotencyKey` is required (1-128 characters). The same key with the same request
returns the existing change (`200`, header `Idempotent-Replay: true`, no second row, no second audit
entry), even at the cap; the same key with a different request is `409 IDEMPOTENCY_CONFLICT`. Keys are
per workspace.

### Permissions: checked when scheduling, and again when it runs

A production change needs the same role as an immediate one: kill is editor-or-above everywhere;
enable, disable and patch need an approver in production. That is checked when the change is
**scheduled** (403 otherwise, so an unauthorised role cannot even queue an approval-gated change) and
checked **again when it executes**, from the creator's membership as it is then. A creator who was
removed from the workspace, whose account is gone, or who was demoted below what the change needs, has
the change **fail** (`CREATOR_REMOVED` / `CREATOR_NOT_AUTHORIZED`, with the reason) and an audit entry
(critical); it never runs with stale authority. A creator who was promoted needs nothing else.

Cancelling: the creator may always cancel; anyone else needs the role the change itself needs.

### Execution

Executing goes through `ApplyConfigChange` / `KillFlag`: same validation, same version bump, same
Redis event, and the `flag.update` / `flag.kill` audit entry shows the actor as the creator **"via
schedule"** (`alice@example.com via schedule`, with `via` and `scheduledChangeId` in the entry). A
`scheduled_change.succeeded` row records the state change.

* **Patches and drift.** A patch stores the parts it touches as they were when scheduled. At run time,
  if the flag's version is unchanged it applies; if it moved, it still applies when the touched parts
  are exactly as they were (someone only toggled `enabled`), and otherwise **fails** with `CONFLICT`:
  it does not guess and does not overwrite the later edit. `enable`, `disable` and `kill` set a state,
  so version drift does not matter.
* **Missed runs.** A change overdue by more than the **grace window** (default 15 minutes,
  `SCHEDULER_GRACE` or `-grace`, 1 minute to 24 hours) is marked `missed`, audited (critical) and not
  applied: a stale change is never run at a surprising time. This includes a retry that would land
  late.
* **Retries.** An error that is not a refusal of the change itself (a database error, a timeout, a
  deadlock) is transient: the row goes back to `pending` with a backoff of 30 s, 1 m, 2 m, 4 m (under
  the grace window in total), at most **5 attempts**, then `failed` (`RETRIES_EXHAUSTED`). Refusals
  (invalid, conflict, flag gone, creator not authorised) fail at once. Each retry and failure is audited.
* **Audit.** `scheduled_change.create`, `.cancel`, `.succeeded`, `.failed`, `.missed` and `.retry`, each
  in the transaction of its state change. The claim (`pending → running`) is bookkeeping and is not
  audited; its attempt count is on the row.

### How rows are claimed (and why)

Not a long-lived process: Cloud Run instances scale to zero, so `Runner.Run` is a function. Each call
(1) marks overdue rows `missed` and rows a worker kept dying on `failed`, (2) claims due rows, (3)
executes each in its own transaction, until nothing is due or 200 rows were handled.

**Claim: a lease column, taken with `FOR UPDATE SKIP LOCKED`.** One statement moves due rows from
`pending` to `running`, stamping `lease_owner`, `lease_expires_at` (now + 2 minutes) and
`attempts + 1`, selecting them `FOR UPDATE SKIP LOCKED`. A row another worker is claiming at that
instant is skipped, not waited for, so two workers never claim the same row and neither blocks the
other. The claim commits at once; the change runs in a separate transaction that re-reads the row
`FOR UPDATE` and proceeds only if it is still `running` under this worker's lease.

Why not hold the row lock from claim to completion (`SKIP LOCKED` alone)? It would pin a connection
and a transaction for the whole execution, and a crash or an error would roll back the claim together
with the attempt count, so a change that crashes its worker would be retried forever. With a lease the
attempt is recorded the moment it starts, a dead worker's row is recognisable by its expired lease,
**another worker takes it over** (`running` with `lease_expires_at <= now` is claimable), and retries are
bounded by the attempt count.

**One execution, one transaction, one savepoint.** The row lock is held while the change is applied.
The change itself runs in a savepoint inside that transaction: if it is refused, only the attempt rolls
back and the failure is still recorded in the same transaction as the state change. A change is
therefore applied completely or not at all, and the flag's audit entry and the scheduled change's
status commit together.

**Cancelling a running change.** Cancel is one `UPDATE … WHERE status IN ('pending','running')`. If the
executor holds the row, the cancel waits for it, then finds a finished change and is refused (`409
NOT_CANCELLABLE`). If the cancel gets there first, the executor re-reads the row, sees `cancelled`, and
does nothing. Never half applied.

A worker that comes back after its lease was taken over finds the row no longer its own and does
nothing. A flag deleted while a change is pending takes the change with it (`ON DELETE CASCADE`).

### Running it

Two ways, both safe to run at once from any number of places:

* **`cmd/scheduler`**: `DATABASE_URL=… [REDIS_URL=…] go run ./cmd/scheduler [-grace 15m] [-max-rows 200]`.
  Runs once, prints counts, exits. A Cloud Run Job or a cron entry. With `REDIS_URL` it tells the SDK
  streams; without it changes still apply and SDKs learn at their next poll.
* **`POST /internal/scheduler/run`**: for a Cloud Scheduler HTTP job.
  * Enabled only when **`SCHEDULER_SECRET`** (at least 32 characters, a secret: keep it in the secret
    store, never in a log or a repository) is set on the API; unset or shorter, the path answers
    exactly like any unknown path (404).
  * The caller sends it in the header **`X-Helios-Scheduler-Secret`**. It is compared in constant time
    (`crypto/subtle` on equal-length digests; a test checks the source for it). A missing or wrong
    secret, or a request with an `Origin` header (a browser), gets one fixed `401` with no detail.
  * It never logs the secret or the request, and answers with counts only:
    `{"missed":0,"claimed":1,"reclaimed":0,"succeeded":1,"failed":0,"retried":0,"skipped":0}`.
  * Rate limited per client IP (class `scheduler_run`, 10 at once then one per 5 seconds) **before** the
    secret is checked, so guessing is as slow as using. No CORS: the CORS middleware skips `/internal/`,
    so a browser gets no headers and no preflight answer.

### Calling it from Cloud Scheduler later

Not set up by this work (no Google Cloud access). When you do:

1. Generate a secret (`openssl rand -base64 48`), store it in Secret Manager, and give the API's Cloud
   Run service `SCHEDULER_SECRET` from it. Redeploy.
2. Create a Cloud Scheduler job: frequency `* * * * *` (every minute), target **HTTP**, method **POST**,
   URL `https://<api host>/internal/scheduler/run`, header `X-Helios-Scheduler-Secret: <the secret>`
   (Cloud Scheduler can read the header value from Secret Manager; do not paste it into the console form
   where it would be shown), attempt deadline 60 s, retries 0 or 1: a retry is harmless, claims are safe.
   An instance that has scaled to zero is started by the request.
3. Watch the job's response counts and the audit log. A change runs within about a minute of its time;
   one that the job missed for more than the grace window is marked `missed`, not run late.

Alternative: a Cloud Run **Job** built from this repository running `go run ./cmd/scheduler` (or the
compiled binary) with `DATABASE_URL` (and `REDIS_URL`) from Secret Manager, triggered by the same
schedule. It needs no inbound secret at all.

Setting `SCHEDULER_GRACE` longer than the schedule interval is what lets a short outage (a deploy, a
cold start) pass without a change being marked `missed`.

## Console

* **Flag drawer → Schedule a change**: kind, environment, date and time with an explicit time zone and a
  line "This runs at … UTC"; a time that does not exist (the hour skipped when clocks go forward) is
  refused, and an ambiguous one (the hour repeated when they go back) is resolved to the first and said so.
  The role needed is shown, and a production change by someone who is not an approver is disabled with
  the reason.
* **Scheduled changes** (flag drawer): upcoming and past changes with status, reason and cancel.
  Viewers see the list, read-only.
* **Promote** (flag drawer): choose a source environment and the parts, read the diff in words
  ("rules: differ" with both values), and apply. The enabled state is off by default and says why.
