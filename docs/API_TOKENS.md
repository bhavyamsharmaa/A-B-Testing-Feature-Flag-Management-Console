# API tokens

An API token lets a CI pipeline or a script call the Helios control plane without a person's login
session. It is an extra way to authenticate, never an extra way to be authorised: a token can only do
what the person who created it can do **now**, and only on the routes the allowlist names.
Rollout plan: `docs/ROLLOUT.md`, "Release 5". Schema: `0012_api_tokens.sql`. API: `api/openapi.yaml`.

## Using a token in CI

Create a token in the console (**API tokens**, admins and owners), copy it **once**, and store it in the
CI system's secret store. Find the ids you need with the token itself:

```bash
# HELIOS_TOKEN comes from your CI secret store; it is never written into a script or a URL.
curl -sS -H "Authorization: Bearer ${HELIOS_TOKEN}" \
  "${HELIOS_API}/workspaces/${HELIOS_WORKSPACE}/environments"          # the environments this token can see

curl -sS -X PATCH -H "Authorization: Bearer ${HELIOS_TOKEN}" -H "Content-Type: application/json" \
  -d '{"fallthroughVariationId":"off"}' \
  "${HELIOS_API}/environments/${HELIOS_ENV_ID}/flags/${FLAG_KEY}"      # an edit, recorded as the token in the audit log
```

`HELIOS_WORKSPACE` is shown on the API tokens page. In GitHub Actions: put the token in a repository or
environment **secret** and pass it as `env: HELIOS_TOKEN: ${{ secrets.HELIOS_TOKEN }}`. Pass it in the
`Authorization: Bearer` header **only**: a token in a query string or a body is ignored (the request has
no credential and gets 401), and a URL ends up in logs.

If a token leaks, revoke it in the console: it stops working at once, on every server. A secret scanner can
find tokens by their shape: `\bhlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}\b` (GitHub custom patterns, gitleaks,
trufflehog).

## What a token is

```
hlt_1a2b3c4d_<43 base64url characters>
```

`hlt_` and 8 hex characters are the **display prefix**: shown in the token list and in audit entries, not
secret. The 43 characters are 32 random bytes: **256 bits**. A user's login JWT starts with `eyJ`, never
`hlt_`, so the server tells the two apart by the first characters alone.

| Field | |
|---|---|
| name | 1-60 characters, shown in lists and in the audit log |
| role | `viewer`, `editor` or `admin`: a ceiling (see "Authority"). Never `owner`. Cannot be raised later: create a new token |
| environments | optional restriction to some environments of the workspace; none means all of them |
| expiry | **always**: 1-366 days from creation, default 90. There is no "never expires" |
| last used | written at most once a minute per token (so it can be a minute behind), never a write per request |
| revoked | revoking is immediate and permanent |

Limits: at most **10 active tokens per person** and **25 per workspace** per workspace (revoked and
expired ones do not count).

### Storage and why SHA-256

Only a **SHA-256 hash** of the token is stored (`api_tokens.token_hash`, unique index), found by an exact
indexed lookup, and compared again in constant time (`crypto/subtle`). The token is shown once, at creation,
and cannot be shown again.

A slow password hash (Argon2id, bcrypt) exists to make guessing a *low-entropy* secret expensive. A token
has 256 bits of randomness, so guessing is out of the question however fast the hash is: the hash only has
to hide the stored value if the database leaks, which SHA-256 does for a value with that much entropy. A slow
hash here would only add a hazard: every unauthenticated request carrying a made-up `hlt_...` string would
force a memory-hard hash, a cheap way to burn the server's CPU and memory. The SDK keys do use Argon2id
(with a cache that hides its cost, because `/evaluate` is the hot path and they pre-date this package); for
tokens a fast hash also allows lookup by the hash itself, with no prefix to scan.

## Authority (what a token may do)

**The effective role of a request is the lower of the token's role and its creator's *current* role in the
workspace.** The creator's membership is read from the database on every request, not cached with the
token:

* the creator is demoted: the token is capped at once (an admin token whose creator is now a viewer can
  only read);
* the creator is removed from the workspace: every request with their tokens fails as "not found",
  exactly like a stranger's (`404 ENVIRONMENT_NOT_FOUND` / `WORKSPACE_NOT_FOUND`);
* the creator's account is deleted: the token fails as an unknown token (401);
* the creator is promoted: the token is still capped at its own role, which never changes.

A token can never exceed the person who made it: when creating, an admin or owner may choose a role up
to their own (and never `owner`).

**Environment restriction.** A token limited to some environments gets `404` for every other
environment, on every route including lists and the audit log. It also cannot use another environment as
the source of a promotion, sees only its own environments in `GET /workspaces/{wsId}/environments`, sees
only its own environments in a segment's usage, and cannot make **workspace-level changes** (create, edit
or delete segments: they reach every environment) though it may read them.

**The production approval gate** is the same as for a person with the effective role: changing production
(edit, promotion, scheduling anything but a kill) needs an approver, so an `editor` token, whoever made it,
gets `403` there; a kill needs only editor. There is no bypass.

### The allowlist

Tokens can reach **only routes explicitly marked token-accessible** in the router registry
(`backend/internal/server/tokenaccess.go`); everything else, including every route added in the future, is
refused with `403 TOKEN_NOT_ALLOWED` whatever the token's role. A test fails when a registered route is not
classified, so a new route cannot become reachable by accident.

| Reachable by a token | Never reachable |
|---|---|
| flags: list, read, **edit**, **kill** | creating and deleting flags, creating experiments |
| `GET /workspaces/{wsId}/environments` | **token management** (create, list, revoke): sessions only |
| experiments: read, start, stop, event counts, results | members and invites, workspace settings and switching, `/me` |
| segments: read, usage, create, edit, delete (unrestricted tokens only for changes) | SDK key management |
| promotion: preview and apply | `/evaluate`, `/track`, `/sdk/stream` (SDK keys), `/internal/scheduler/run` |
| scheduled changes: create, list, get, cancel | ownership transfer and workspace deletion (no such routes for tokens) |
| audit log: read | |

### Managing tokens

Creating, listing and revoking tokens is for **admins and owners**, in the console or with a user session.
**A token can never create, list or revoke tokens**, its own included (these routes are token-denied).
Editors and viewers cannot create tokens, so there are none for them to see: the rule is "admins and owners
manage all of a workspace's tokens, nobody else manages any". Any admin or owner can revoke any of the
workspace's tokens.

## Revocation: how fast, and the worst case

Revoking a token (a) drops it from the verification cache of the instance that handled the request at
once, and (b) announces it on the same Redis channel the SDK keys use; every other instance drops it
within milliseconds (measured: about 1-2 ms against a real local Redis, over a one-hour cache TTL).

If the announcement is lost (Redis down), an instance keeps trusting a token it already verified for at
most the **cache TTL: 15 seconds**. That is the worst case, and it applies only to revocation: what the token
may *do* is never cached, and **expiry is exact** even for a cached token. When an instance's subscription to
the channel (re)starts it clears its cache, since announcements may have been missed.

## Abuse limits and what is logged

* Failed token authentications are limited **per client IP** (30 at once, then one every 2 seconds),
  checked **before** any hashing or database work; once an IP has used its allowance, even a valid token
  from that IP waits (`429`, `Retry-After`, `RATE_LIMITED`).
* Each token has its own request bucket (200 at once, then 20 a second), so a runaway pipeline does not
  throttle the person who created it. Creating and revoking tokens has its own small limit.
* Every refusal of the credential itself (unknown, expired, revoked, malformed) is the **same** `401`
  with the same body, so a caller cannot tell whether a token exists.
* A token, its hash and the `Authorization` header are never logged (a test greps the real logs, and the
  log-credential test knows the new names).
* **Audit.** Every write made with a token is recorded with `actorType: api_token`, the actor shown as
  `name (hlt_xxxxxxxx)` and `actorDetail` giving the token id, name, prefix and creator. Creating and revoking
  tokens are audited (as the person), and a presented token that is expired, revoked or whose creator is
  gone is audited as `api_token.rejected` (at most once an hour per token and reason). Scheduled changes
  made through a token run later as their creator "via schedule" (see `docs/SCHEDULING.md`), with the
  creator's authority re-checked then.
