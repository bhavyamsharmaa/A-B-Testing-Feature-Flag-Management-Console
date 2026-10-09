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

## Round 2: closing what remained

| Item | Status | What changed | How it was proven |
| --- | --- | --- | --- |
| Email confirmation enforced by the backend | Fixed where it can be; one limit that cannot be (see below) | `GET /me` (the bootstrap), `GET`/`POST /workspaces` and invite acceptance answer `403 EMAIL_NOT_CONFIRMED` and create nothing when the email is not confirmed. Read only from Supabase: the verified token's claims, and (with the public `SUPABASE_ANON_KEY`) Supabase's own `/auth/v1/user`. Console shows "Please confirm your email first" with resend and "I've confirmed" | An unconfirmed user gets no workspace, membership or audit row (20 requests at once), cannot accept an invite by token or id; after confirming the same user works. Each check was removed in turn |
| Rate limiting | Fixed | In-memory token buckets per IP, per user, per SDK key and for failed SDK-key attempts per IP; stream concurrency cap; 429 with `Retry-After`; health never limited; `RATE_LIMITS`, `RATE_LIMIT_DISABLED`, `TRUSTED_PROXY_HOPS` (defaults to 1 on Render), `STREAMS_PER_KEY` | Every limit fires in a test; the default limits never trip in normal traffic; eight limiters were removed in turn |
| Last owner at the database level | Fixed | Migration `0007`: a deferred constraint trigger refuses a transaction that leaves a workspace with members and no owner (the app reports it as 409 LAST_OWNER) | Raw SQL that removes or demotes the last owner fails; hand-over in one transaction works; with the app's row locks removed the database still stops two owners leaving at once; with the trigger removed too the workspace ends with zero owners |
| Revocation without the wait | Fixed | Revoking publishes the key prefix on `helios:apikey-revoked`; every instance drops it from its cache and closes streams on it at once. The 15s TTL and 10s re-check remain as the fallback | Two instances sharing a bus, and a real Redis: the other instance rejects the key in about 1-2 ms with its fallbacks set to an hour; with the broadcast lost it falls back within the TTL (about 390 ms at 400/200 ms). Publish, watch, invalidate and the stream wake-up were removed in turn |
| Removed members | Confirmed and pinned | Membership is checked on every request, so a removed member's unexpired JWT gets 404 on every workspace route (the every-route test runs one). Streams authenticate with SDK keys, not people, so there is no per-member stream; keys belong to the workspace and keep working until an admin revokes them. The remove dialog says so | Tests, and a regression where the guard caches membership is caught by three of them |
| Invites without the dashboard toggle | New switch, default unchanged | `INVITES_BY_ID=false` disables accepting by id and hides open invites in `/me`, leaving only the one-time link | Test, and mutation |

### What email confirmation can and cannot do

Documented Supabase access tokens carry **no** email-confirmation claim. In practice GoTrue puts `email_verified` in `user_metadata`, and a custom access-token hook can add more; the backend reads `user_metadata.email_verified`, a top-level `email_verified`, or a confirmation timestamp, and treats a contradictory token as unconfirmed. Supabase's own `GET /auth/v1/user` (called with the user's token and the public anon key) returns `email_confirmed_at` and overrides the claims.

**It cannot detect that "Confirm email" has been switched off.** Supabase's documentation says disabling it "implicitly confirms the user's email in the database", so with the toggle off every new address looks confirmed to the token, to `/auth/v1/user` and to this backend. Nothing in Supabase's data distinguishes that from a real confirmation, so a backend that only trusts Supabase cannot make open signup independent of that toggle. What protects you if it is switched off:

- Creating a workspace is harmless to other tenants (it only creates the caller's own).
- Accepting an invite **by link** still needs the secret token, which the inviter sent to the real mailbox.
- Accepting an invite **by id** (the banner in the console) needs only the account's email, so with the toggle off someone could register an invitee's address and accept. Set `INVITES_BY_ID=false` if you cannot guarantee the toggle stays on.

What it does stop: projects that let people sign in before confirming ("allow unverified sign-ins"), tokens that say unconfirmed, and anything that leaves Supabase's own user record unconfirmed.

Set `SUPABASE_ANON_KEY` (a public value) in production: the default mode then becomes `strict` (refuse unless Supabase says confirmed). Without it the backend only reads token claims, and with none present it lets the user through (`enforce`); check a real token to see whether `user_metadata.email_verified` is there.

## Known limitations

These are open on purpose, or cannot be closed from here.

- **Supabase sessions outlive removal (by design).** Removing someone from a workspace deletes their membership; their Supabase session and account are untouched. They lose workspace data at once (membership is checked on every request) and get a fresh personal workspace on their next `/me`. Ending the account is a Supabase action.
- **SDK keys outlive their creator.** Keys belong to the workspace. Revoke the ones you no longer trust after removing someone; the console says so.
- **The limiter is in memory, per process.** Right for one instance (Render today). With N instances each allows its own share, so the effective limits are N times the configured ones, and a restart resets all buckets. Moving to a shared store (Redis) is only worth it with several instances. Behind a proxy the client address comes from `X-Forwarded-For` only with `TRUSTED_PROXY_HOPS` (defaulted to 1 when `RENDER=true`); set it correctly elsewhere or all clients share one bucket.
- **Shared-address collateral.** Failed SDK-key attempts are counted per IP. A client behind the same NAT as an attacker can have an *uncached* key refused (429) once the attacker spends that IP's allowance; keys already cached are never delayed.
- **The database last-owner rule does not cover an empty workspace.** If every member leaves, the workspace is abandoned, not "members without an owner". The application's row locks stop owners leaving at once. A superuser session with `session_replication_role = replica` bypasses all triggers and foreign keys.
- **Account deletion** of the last owner of a workspace with other members is refused by the database until ownership is transferred.
- **Email confirmation cannot detect the "Confirm email" toggle being off** (above).
- **Open streams close within about 10 s of a revocation made on an instance that could not broadcast it.** A Redis outage during the revocation is the only way to hit that.
- **No email is sent by Helios.** Invite links are shared by the inviter; the token is in the link's path (the console sends no Referer, and removes it from the URL when used).
- **Migrations 0004-0007 have only run on local throwaway databases.** Rehearse on a copy, and apply them in order, before production.
- **Tests use a stub authenticator** for most database-backed tests; real JWT verification is covered by its own tests and by the Playwright flows against `devauth`, which is not Supabase.
