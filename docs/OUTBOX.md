# The outbox: sending things outside the system reliably

Webhooks and invite emails are the same problem: *tell something outside Helios that something happened, even if
the process dies, the network is slow, or the receiver is down, without ever blocking or losing the change that
caused it.* They share one table (`outbox`), one claim, one retry policy and one worker.

## The guarantee

- **Written with the change.** A row is inserted in the **same database transaction** as the change that caused
  it (flag edit, kill, create, delete, promotion, experiment start/stop, scheduled change finishing, invite
  create/resend). A change that rolls back leaves no row, so nothing is sent about something that did not happen. A
  change that commits has its row, so nothing is lost if the process dies right after.
- **At least once.** If a worker dies after the receiver processed a request and before the result was recorded,
  the row is delivered again. **Every attempt of one delivery carries the same `delivery_id`** (the
  `Helios-Delivery-Id` header for webhooks). Receivers must dedupe on it. Exactly-once is not attempted: it is not
  achievable across a network call.
- **Never blocks the change.** A full queue drops the *oldest* webhook rows (audited), it never refuses or slows a
  user's edit. Emails are not dropped: they are limited per day (below).

## How a worker claims (the scheduler's pattern)

1. One statement moves due rows to `delivering` and stamps `lease_owner` and `lease_expires_at`, using
   `FOR UPDATE SKIP LOCKED` **in that statement only**. Two workers never claim the same row and neither waits for
   the other. The claim commits at once. (The claimability test is repeated on the row actually locked: the
   candidates come from a snapshot, and without the repeat two workers could claim one row. A test caught exactly that.)
2. The **network call is made with no transaction open**, so a slow customer pins nothing in the database.
3. The result is recorded in a short second transaction **guarded by the lease** (`status = 'delivering' AND
   lease_owner = me`). A worker whose lease was taken over records nothing.
4. A row whose lease expires (the worker died or hung) is claimable again. A row that has used all its attempts
   while its workers kept dying is marked `dead`, not retried forever.

**Fairness.** At most `PerTarget` (default 2) rows of one target (one endpoint, one recipient) are in flight at
once, counted across workers, and `Concurrency` (default 8) deliveries run at once in one worker: one slow
customer cannot take every worker.

## Retries

Exponential backoff with +-20% jitter: after attempt *n* fails the next is due after 1 m, 5 m, 15 m, 1 h, 3 h, 6 h,
12 h. After the 8th failure the row is `dead` (about 22 hours in all). Statuses: `pending` -> `delivering` ->
`delivered`, or `failed` (an attempt failed, a retry is scheduled) -> ... -> `dead`. Dead rows are kept for
inspection (the delivery log) and **purged after 30 days; delivered rows after 14**, by every worker run or by
`go run ./cmd/outbox -purge-only`.

## Running it

It is a function, not a process, like the scheduler:

```
DATABASE_URL=... WEBHOOK_SECRET_KEY=... go run ./cmd/outbox        # deliver what is due, then exit
DATABASE_URL=... go run ./cmd/outbox -purge-only
DATABASE_URL=... WEBHOOK_SECRET_KEY=... WEBHOOK_SECRET_KEY_PREVIOUS=... go run ./cmd/outbox -rekey
```

Run it every minute from a Cloud Run Job / Render cron / Cloud Scheduler, from as many places as you like at once.
Or call `POST /internal/outbox/run` with the `X-Helios-Outbox-Secret` header: it exists only when `OUTBOX_SECRET`
(at least 32 characters) is set; otherwise it answers 404 like any unknown path. It is rate limited per IP before the
secret is checked, compares the secret in constant time, refuses browsers (any `Origin` header), and answers with
counts only. A test ping or a resend from the console is delivered **at once, inside the request**, and does not wait
for the worker.

## Caps and limits

| Limit | Value | When exceeded |
|---|---|---|
| Queued webhook rows per workspace | 1,000 | the oldest are marked dead (`dropped: ...`) and one `outbox.overflow` audit entry is written (at most every 10 min); the change succeeds |
| Webhook endpoints per workspace | 10 | `409 QUOTA_EXCEEDED` |
| Invite emails per workspace per day | 50 | `409 QUOTA_EXCEEDED`, no invite is created |
| Invite emails per address per workspace per day | 3 (the invite and two resends) | `409 QUOTA_EXCEEDED` |
| Invite emails per address across all workspaces per day | 6 | **silent to the caller** (it would reveal other workspaces' activity): the invite is created and the response is the same, the email is recorded as failed ("too many invite emails to this address today") |
| Resend cooldown per invite | 10 minutes | `429 RESEND_COOLDOWN` with `Retry-After` |
| Test pings and resends | 10 at once, then one every 5 s per person | `429 RATE_LIMITED` |
| Payload | 16 KB (events are shrunk, not refused) | `truncated: true` |

The caps are soft by a handful of rows under concurrency (the count is not serialised): the price of never making
a user's change wait.

## What is never stored or logged

Errors in `outbox.last_error` come from a **fixed vocabulary** ("timed out", "the receiver answered 503", "destination
blocked: ..."). Go's own network errors embed the full URL, which can carry a token in its path, so they are never
stored; everything stored is printable, one line, truncated. A response body is read up to 64 KB and kept only as
a 200-character printable hint on a *failed* attempt (never on a success). Logs carry delivery ids and counts only:
no payload, no URL, no address, no secret, no signature, no invite token (a test captures the logs and looks).
