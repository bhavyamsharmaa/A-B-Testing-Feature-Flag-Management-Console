# Lifecycle: handing a workspace over, deleting it, exporting and explaining its history, and experiment controls

Things that must not go wrong when a workspace changes hands or ends. Four features, one migration (0014).

## 1. Ownership transfer

An **owner** offers ownership to an existing **admin** of the same workspace (Settings → Ownership, or `POST /workspaces/{wsId}/ownership-offers`).
Session only: API tokens are refused. One open offer per workspace (a partial unique index). The offer lasts **7 days**, the owner can cancel it,
the admin can decline it, and the admin sees it as a banner in the console (and in `/me` as `ownershipOffers`).

**Acceptance is one transaction, and the order is what keeps the workspace owned at every instant:**

1. lock the workspace row, then the new owner's per-user advisory lock (the owned-workspaces limit is counted under it), then every membership
   row of the workspace: the same locks `ChangeRole` and `RemoveMember` take, so a concurrent demotion or removal queues behind the transfer
   and then sees the new state (or the transfer queues behind it and then refuses);
2. re-check everything on the locked rows: the offer is still open and not expired, the workspace is not scheduled for deletion, the offerer is
   still an **owner**, the target is still a member and still an **admin**, and the target owns fewer than 5 workspaces (otherwise
   `409 QUOTA_EXCEEDED` with a plain message; the limit is never exceeded silently);
3. **promote the target to owner first, then demote the offerer to admin**, then move `workspaces.owner_id` (the column the limit counts) and mark the
   offer accepted. There are two owners for the length of one transaction, never none. The deferred last-owner trigger of migration 0007 is
   checked at commit and sees two owners become one. (Demoting first would also pass that trigger, because it is deferred, but an error or
   a reader between the two statements would see an ownerless workspace, so it is never written that way.)

A *personal* workspace stops being personal when it changes hands (a person has at most one, their own). An owner who wants to leave must transfer
first; leaving as the last owner is still `409 LAST_OWNER`, from the application and from the database. Offered, accepted, cancelled, declined and
expired offers are all audited (`ownership.*`). An expired offer is marked and audited when anything touches it (creating a new offer,
accepting it) and by `cmd/purgeworkspaces`.

## 2. Deleting a workspace

**Owner only, session only, after typing the workspace's exact name.** It is a *soft* delete with a **14-day grace period**.

### What "scheduled for deletion" means (`workspaces.deleted_at`, `purge_after`)

The workspace is **invisible**, through ONE place: the two membership queries in `rbac` (`resolveEnvironment`, `resolveWorkspace`) and
`workspaces.load` (which builds `/me`) all join `workspaces` on `deleted_at IS NULL`. Every route that resolves a workspace or an environment
goes through them, so every one answers `404` to every member and every API token exactly as to a stranger. A test walks the router registry
(about 54 workspace and environment routes, as a member and as a token, before and after) so a route that forgets cannot be added.

The other things that read a workspace without going through those queries each have the predicate too, and a test per item:

| Thing | What happens |
|---|---|
| SDK keys, `/evaluate`, `/track` | the key lookup joins `deleted_at IS NULL`: 401 |
| open `/sdk/stream`s | closed at once; the stream's periodic re-check also asks the database |
| other API instances | the cache is dropped locally and announced on the revocation channel (`{"workspaceId": ...}`); if the broadcast is lost, the cache TTL (15 s) and the streams' re-check bound it. Tested with a one-hour TTL (only the broadcast can act) and with the broadcast lost |
| API tokens | still authenticate (so the guards answer 404 like a stranger's) but nothing is recorded for them, not even a last-used time |
| scheduled changes | not claimed, not marked missed, not cancelled (they resume if it is restored) |
| outbox (webhooks, invite email) | not claimed (they resume if it is restored) |
| invites | cannot be accepted, are not listed in `/me` |
| an open ownership offer | cancelled |
| `/me` | the workspace is gone from `workspaces`; the **owner** (only) gets it under `pendingDeletion` with the date, and a user whose only workspace is pending deletion is *not* given a fresh personal workspace |

**Nothing is deleted or changed during the grace period**, so a **restore** (`POST /workspaces/restore`, owner only; the console's workspace menu shows
"Scheduled for deletion … Deleted for good on <date>" with a Restore button) clears three columns and everything is exactly as it was: a test dumps
every table's rows for the workspace before and after and compares them. Restore is refused (`410 GRACE_PERIOD_ENDED`) once `purge_after` has passed, by the database clock, even if
the purge job has not run yet, so a restore can never race a purge.

**The owned-workspaces limit** (5 per person) **counts a workspace pending deletion until it is purged.** It can come back, and letting its owner
create five more in the meantime would make Restore fail on the limit. (Documented choice; tested.)

### The purge (`cmd/purgeworkspaces`, or `POST /internal/workspaces/purge`)

Never in the API process; run it from a scheduler, hourly is plenty. The endpoint exists only with `PURGE_SECRET` (32+ characters), takes it in
`X-Helios-Purge-Secret` (constant-time compare, one fixed 401, browsers refused, per-IP rate limit before the secret is checked), and returns counts only.

For each workspace past `purge_after` (re-read from the database before every batch, so a workspace that was restored, or purged by another run, is left alone):

1. **events**, in batches of 2,000 (`DELETE ... WHERE ctid IN (SELECT ctid ... LIMIT n FOR UPDATE SKIP LOCKED)`), each its own short transaction;
2. **outbox**, 3. **segment_members**, 4. **audit_logs**, the same way. Children before parents: events point at environments and experiments, outbox rows at
   endpoints and invites, segment members at segments. The audit log has no foreign key and a trigger that forbids every delete; migration 0014
   lets the trigger allow a delete of *exactly* the rows of a workspace that is scheduled for deletion **and** past `purge_after`, nothing else;
5. last, **the workspace row** in one transaction with the tombstone and the final audit entry: its foreign keys with `ON DELETE CASCADE` remove the
   small tables (flags, configs, experiments and their variations, environments, API keys and tokens, segments, webhook endpoints, invites, members, offers,
   scheduled changes, roles), which by now hold hundreds of rows, not millions.

**No giant transaction and no long table lock:** only the rows of each batch are locked, and rows another transaction holds are skipped. **Resumable and
idempotent:** progress *is* the data. A crash between batches leaves the workspace scheduled with fewer rows and the next run carries on (tested by failing the
fourth batch); a purged workspace is simply not found; two runs at once cannot double-delete.

**What remains:** a tombstone (`workspace_tombstones`: workspace id, who asked, when asked, when purged, row counts: no names, emails or content) and a final
audit entry `workspace.purged` under the nil workspace id (no tenant can read it; the purged workspace's id is its `resource_id`). A test discovers every
table with a `workspace_id` column from the database catalog and fails if the purge neither deletes it nor names it as an exemption, and checks that after a purge no
table (but `workspace_tombstones`) has a row for the workspace.

## 3. Audit export and per-flag history

**Export** (`GET /environments/{envId}/audit-logs/export?format=csv|json&from=&to=`; admins and owners, session only; Audit page → Export…): the entries of
that environment plus the workspace-wide ones (the same scoping as the audit read route), for a range of **at most 90 days** (default 30), **at most 100,000
entries** (more is refused with `422 EXPORT_TOO_LARGE` before anything is sent: never silently cut), **streamed** in keyset batches of 1,000 so memory
is bounded, and rate limited (3 at once, then one every 10 s, per person). The export is itself audited (`audit.export`, critical) before the first byte.

* **CSV injection:** any text cell that begins with `=`, `+`, `-`, `@`, tab, carriage return or newline gets a leading `'`, so a spreadsheet shows text, never runs a formula.
  Quotes, commas and embedded newlines are escaped by `encoding/csv` and round-trip intact. The JSON export is exact.
* **No secrets:** values under secret-looking keys (`token`, `secret`, `hash`, `password`, `authorization`, `signature`, ...) are replaced by `[REDACTED]` at any depth,
  and so are secret-*shaped* strings anywhere (API tokens `hlt_`, webhook secrets `whsec_`, invite tokens `hinv_`, SDK keys `hsdk_`, bearer values, JWTs, password hashes).

**History** (`GET /environments/{envId}/flags/{key}/history`; any role that can see the flag; the flag drawer's History section): the changes to one flag in this environment
plus its workspace-wide events (creation, deletion), newest first, paged (`before`, `limit`), read only (there is no restore). Each entry has the **actor** (a person,
an API token with the person who made it, or a schedule with the person who scheduled it), the **time** (RFC 3339 UTC; the console shows it in the viewer's zone, with
the zone's name, and the exact UTC time on hover) and a readable **before/after of exactly the fields that changed**: enabled, default variation, rollout (percentages),
targeting rules (one line per rule). Secret-shaped values are redacted here too.

## 4. Experiment control

Results used to treat the flag's first variation as the control, implicitly. Now each experiment has **exactly one designated control**: a row per variation in
`experiment_variations` with `is_control`, a **partial unique index** (at most one per experiment), and validation plus the creating transaction (at least one). Migration 0014
backfills every existing experiment with its flag's first variation as control, so **no existing result changes** (the verify script and a test check it).

* The creator chooses it when creating the experiment (`controlVariationId`; the console's Create experiment dialog); omitted, it is the flag's first variation.
* It can be changed (`PATCH /environments/{envId}/experiments/{key}`, editors and above, session only) **until the first `$exposure`** is recorded; after that
  `409 CONTROL_FROZEN`. The check runs under a row lock and again just before the change commits, so an exposure that lands in between still wins (an exposure that commits in the same
  instant as the change can still slip through; the audit entry `experiment.control_change` records what was changed).
* Results compare every other variation with the designated control and list it first; the console marks it "Control" in text on the experiment card and in the results table.
  **Swap test:** the same data analysed with each variation as control gives the same p-value, the z statistic and the difference negated, and the interval's ends negated and swapped.
