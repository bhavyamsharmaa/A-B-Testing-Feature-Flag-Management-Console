# Multi-tenancy: hostile review

A review of the workspace work that assumed a cross-tenant leak existed. Every fix
has a test, and each test was checked by removing the protection and watching it
fail (mutation checks), except where noted. Run them with the stack in
`docs/LOCAL_MULTITENANCY.md`.

## Findings

| # | Finding | Real bug? | Fix | Test |
| --- | --- | --- | --- | --- |
| 1 | Redis is used for pub/sub only (no cache keys, no counters). One channel pattern, `flags:{environmentId}`, subscribed by exact name, no workspace id in it | Hardening (env ids are globally unique, so no actual sharing) | `flags:{workspaceId}:{environmentId}` | A's flag change never reaches B's stream or the other environment's; every published channel is checked |
| 2 | An open `/sdk/stream` verified its key only when it connected: a **revoked key kept receiving flag changes** | **Yes** | The stream re-checks the key against the database every 10s and closes when revoked | stream closes after revocation (and fails if the re-check is removed) |
| 3 | Revoking a key left it working for up to the cache TTL (60s) | **Yes** (over the 30s you set) | The revoking instance drops its cache entry at once; TTL for other instances is now 15s | revoked key fails in <1ms on the revoking instance; a second instance honours it within its TTL (measured: 406ms at a 400ms TTL) |
| 4 | `/evaluate` selected flags by environment only, the one flag query with no workspace filter | Defence in depth (safe on consistent data) | Added `workspace_id` of the key | planted-inconsistency test |
| 5 | The "redundant" workspace filters were untested | Test gap | n/a | A test plants an inconsistent `flag_config` (foreign keys off for one local transaction) and checks A can't read, list, patch, kill, delete, experiment on or evaluate B's flag; removing any filter fails it. Update and Kill carry two layered filters (lock query and read-back); either one alone refuses and rolls back, both removed is what fails |
| 6 | 0006 changed the `workspace_members` key to `(user_id, workspace_id)`, which **removed the database backstop** against two concurrent first requests creating two personal workspaces; only an advisory lock remained | **Yes** (regression of mine) | `workspaces.is_personal` + a partial unique index; a lost race returns the winner's workspace | 20 parallel first requests -> one workspace, one owner membership. Either protection alone is enough; only removing both fails |
| 7 | The two concurrency tests passed with their protection removed (window too small) | Test gap | Test-only delay trigger widens the window | owners leaving at once never leaves zero (fails without the row locks) |
| 8 | No route-level proof of scoping | Test gap | The router exposes a registry; every route must declare how it is scoped | one test iterates over all 30 routes as another tenant (404, no data), signed out (401) and as a viewer (403); an unknown path parameter or a route mounted outside the registry fails it |
| 9 | Two keys can draw the same 32-bit display prefix; creation then returned 500 | **Yes** at scale | Retry with a fresh key | collision test |
| 10 | Credentials in logs and errors | No leak found | Source check that no log call is handed a credential-named value; connection-string redaction (pgx already masks the password, this is belt and braces) | an integration test runs failing flows with an SDK key and invite token in flight, breaks the schema to force real 500s, and asserts neither logs nor responses contain them; rejected JWTs aren't echoed or logged |
| 11 | Invite links carry a one-time token in the URL path | Low | `Referrer-Policy: no-referrer` (meta tag and header); the token leaves the URL on accept | n/a |

Also verified, with tests: SDK keys are stored as Argon2id hashes and never returned
again; mangled, truncated, mixed-up and upper-cased keys never resolve; invite tokens
are random, stored as SHA-256 only, single use, expire, and need the invited email
(case-insensitive); nobody promotes themselves or anyone above their role; admins
can't touch owners; the last owner can't leave, be demoted or removed; viewers get
403 (not 404) on every write; same tab, different person shows nothing of the
previous user.

## Dead code (nothing dropped)

`user_environment_roles` and `events` are read and written by **no** Go code, and the
empty `segments` package is imported by nothing and linked into no binary (checked
with `go list -deps`). A test fails if that changes.

## Not fixed / remaining

- **Invites trust the email in the token.** If Supabase "Confirm email" is off, someone
  could sign up with an address they don't own and accept an invite sent to it. Keep
  confirmation on (the brief says it stays required); the backend does not check it.
- **Other instances honour a revoked key for up to 15s**, and open streams close within
  10s of the next re-check. No cross-instance push (a Redis broadcast would remove it).
- **No rate limiting** on any endpoint (Supabase limits sign-in; SDK keys and invite
  tokens carry 256 bits, so guessing is not feasible, but the API can still be hammered).
- **Last-owner protection relies on row locks**, not a database constraint.
- **A removed member's already-issued JWT** stops working for workspace data at once
  (membership is checked per request), but their Supabase session itself lives on.
- The migrations 0004-0006 have run only on local throwaway databases.
