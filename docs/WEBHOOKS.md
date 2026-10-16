# Webhooks

Webhooks tell other systems when something changes in a workspace. Workspace **admins and owners** manage
them in the console (**Webhooks**) or with a browser session. API tokens cannot: the routes are token-denied, so
a pipeline cannot register an endpoint or read a subscription.

Delivery is **at least once**, signed, and goes through the shared outbox (`docs/OUTBOX.md`): a row is written in the
same transaction as the change, so a change is never lost and a rolled-back change sends nothing.

## Endpoints

| Field | Notes |
|---|---|
| URL | `https` only, on a **public** host, port 443 or 8443, at most 2,048 characters, no credentials, no fragment. Validated when you save it **and again before every delivery**. |
| Events | one or more of the nine below |
| Environment | optional filter: only events of that environment |
| Enabled | a disabled endpoint gets nothing new queued |

At most **10 endpoints** per workspace. There are no redirects: a `3xx` answer is a failed delivery.

## Events

Every delivery is `POST` with a JSON body of this envelope (key order is not significant; unknown fields should be ignored):

```json
{
  "id": "0b4c7c1e-...",                  // the event id: the same for every endpoint's delivery of this event
  "type": "flag.updated",
  "version": 1,                          // schema version of the envelope and of every event's "data"
  "createdAt": "2026-11-03T10:00:00Z",
  "workspaceId": "…",
  "environment": { "id": "…", "key": "production" },
  "actor": { "type": "user", "id": "…" },
  "data": { … }
}
```

**actor** is who caused it, by id, never by email: `{"type":"user","id":"<user id>"}`, optionally with
`"via":"schedule"` and `"scheduledChangeId"`; or `{"type":"api_token","id":"<token id>","name":"<the token's name>"}`.
A token is never recorded as the person who made it.

| Event | When | `data` |
|---|---|---|
| `flag.created` | a flag is created | `{"flag":{"key","name"}}` |
| `flag.updated` | a flag's configuration in an environment is edited (including by a schedule) | `{"flag":{"key","name"},"version":{"before","after"},"changes":{…}}` |
| `flag.killed` | the kill switch is used | `{"flag":{…},"changes":{"enabled":{"before","after":false}}}` |
| `flag.deleted` | a flag is deleted | `{"flag":{"key","name"}}` |
| `experiment.started` | an experiment starts | `{"experiment":{"key","name"},"flag":{"key"},"status":{"before","after"}}` |
| `experiment.stopped` | an experiment stops | same shape |
| `promotion.applied` | a configuration is promoted from another environment (sent *instead of* `flag.updated`) | like `flag.updated` plus `"promotion":{"sourceEnvironment","targetEnvironment"}` |
| `scheduled_change.executed` | a scheduled change ran | `{"flag":{"key"},"scheduledChange":{"id","kind","runAt","attempts","resultCode"}}` |
| `scheduled_change.failed` | a scheduled change failed or was missed | same shape, `resultCode` says why |

`changes` holds only what changed: `enabled` (`before`/`after` booleans), `fallthroughVariationId` (ids),
`rollout` (a map of variation id to number, before/after), and `targetingRules`, which is **only**
`{"before":{"ruleCount":n},"after":{"ruleCount":m}}`. **Never in any event:** SDK keys, API tokens, signing secrets,
member email addresses, or the contents of targeting rules (attributes, values, segments). Where there are rules,
read them from the console or the API with a token. The message of a failed scheduled change is not included (it can
name the person who scheduled it); `resultCode` is.

A body over 16 KB is shrunk to its identifiers with `"truncated": true`.

There is also `webhook.ping`, sent only by **Send test**: `{"message":"This is a test event from Helios."}`. It cannot
be subscribed to and does not count toward an endpoint's health.

## Headers

| Header | Meaning |
|---|---|
| `Helios-Delivery-Id` | one id per delivery, **identical on every retry and resend**. Dedupe on it. |
| `Helios-Event-Id` | the event id (the same across endpoints) |
| `Helios-Event-Type` | e.g. `flag.updated` |
| `Helios-Timestamp` | unix seconds when *this attempt* was signed |
| `Helios-Signature` | `v1=<hex>`; during a secret rotation, two values: `v1=<new>,v1=<old>` |
| `User-Agent` | `Helios-Webhooks/1` |

## Verifying a delivery

The signature is the hex **HMAC-SHA256**, keyed with your endpoint's secret (`whsec_…`, shown once), of

```
<Helios-Timestamp> + "." + <Helios-Delivery-Id> + "." + <raw request body>
```

Verify against the **raw bytes** you received (not a re-serialised object), accept if **any** `v1=` value matches
(compare in constant time), and **reject a timestamp more than five minutes from your clock**: that is what stops
someone replaying a captured request. Because the delivery id and the timestamp are inside the signed string,
neither can be swapped on a captured request.

### Go

```go
func verify(secret, sigHeader, timestamp, deliveryID string, body []byte) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-ts)) > 300 {
		return false // missing, malformed, or outside five minutes
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + deliveryID + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	ok := false
	for _, part := range strings.Split(sigHeader, ",") {
		if got, found := strings.CutPrefix(strings.TrimSpace(part), "v1="); found && hmac.Equal([]byte(got), []byte(want)) {
			ok = true
		}
	}
	return ok
}
```

### Node

```js
const crypto = require('node:crypto')

function verify(secret, sigHeader, timestamp, deliveryId, rawBody) {
  const ts = Number(timestamp)
  if (!Number.isFinite(ts) || Math.abs(Date.now() / 1000 - ts) > 300) return false
  const want = crypto.createHmac('sha256', secret).update(`${timestamp}.${deliveryId}.`).update(rawBody).digest('hex')
  let ok = false
  for (const part of sigHeader.split(',')) {
    const p = part.trim()
    if (!p.startsWith('v1=')) continue
    const got = Buffer.from(p.slice(3))
    if (got.length === want.length && crypto.timingSafeEqual(got, Buffer.from(want))) ok = true
  }
  return ok
}
```

A golden vector (also the repo's `TestGoldenSignatureVector`, computed independently with Node): secret
`whsec_golden_test_secret_do_not_use`, timestamp `1700000000`, delivery id
`11111111-2222-3333-4444-555555555555`, body `{"id":"evt_1","type":"flag.updated","data":{"flag":{"key":"checkout"}}}`
gives `acf5edffd32e8c912dbdb105e4fad4f177f53abc655707b4a87aea5ce817ca1c`.

Answer `2xx` quickly (within 10 seconds) and do the work afterwards. Anything else, including a redirect or a timeout, is
retried: 1 m, 5 m, 15 m, 1 h, 3 h, 6 h, 12 h, then given up on (about 22 hours). The console's delivery log shows each
attempt's HTTP status, time and a short reason, and **Resend** sends a failed delivery again with the same delivery id.

## Secrets

- A secret is generated per endpoint (256 random bits, `whsec_` prefix) and shown **once**, when the endpoint is
  created and when it is rotated. The console keeps only its last four characters.
- **Rotation:** **Rotate secret** makes a new one and keeps the old one signing for **24 hours**: each delivery carries
  both signatures, so you can update the receiver without missing an event. After 24 hours only the new secret signs.
- **How it is stored.** The worker must be able to sign, so the secret cannot be hashed. It is stored **encrypted**
  with AES-256-GCM, under a key that lives only in the environment (`WEBHOOK_SECRET_KEY`, 32 random bytes, base64),
  never in the database: a database backup or a read-only SQL account yields ciphertext. Each value has its own random
  nonce and is bound to its endpoint's id, so ciphertext copied between rows does not open. **The server fails closed:**
  without the key, endpoints cannot be created (`503 WEBHOOKS_UNAVAILABLE`), nothing is signed and nothing is sent.
  There is no plaintext fallback.
- **Key rotation:** set the new key as `WEBHOOK_SECRET_KEY` and the old one as `WEBHOOK_SECRET_KEY_PREVIOUS` (both
  open, only the new one seals), run `go run ./cmd/outbox -rekey` to re-encrypt every stored secret, then drop the old
  key. **Back the key up with your database backups, in a separate place:** if it is lost, stored secrets cannot be
  read and every endpoint's secret must be rotated.
- A secret never appears in a response (other than the one that creates or rotates it), the delivery log, the audit
  log or the server logs.

## SSRF protection

A webhook makes the server call a URL a workspace admin chose, from inside our network, so the destination is
restricted and checked at the moment of connection (`internal/platform/netguard`):

- **https only**, ports 443 and 8443, no credentials, at most 2,048 characters, no whitespace or backslash.
  `localhost`, `*.localhost`, `.local`, `.internal`, `.lan`, bare single-label names are refused.
- **Blocked addresses:** loopback, RFC 1918 private, link-local (including `169.254.169.254`, the cloud metadata
  service), CGNAT (`100.64/10`), unique-local IPv6 (`fc00::/7`), multicast, unspecified, reserved, documentation and
  benchmarking ranges, NAT64, Teredo, and **IPv4-mapped / IPv4-compatible / 6to4 IPv6 forms of all of these**
  (`::ffff:10.0.0.1`, `2002:7f00:1::`). Numeric spellings of an address (`2130706433`, `0x7f000001`, `0177.0.0.1`,
  `127.1`) are refused as unusual forms.
- **The address actually connected to is checked.** The hostname is resolved by the dialer itself; *every* address it
  returns must be allowed (a name that points partly inside is refused whole), and the connection is made to a literal
  address that was checked, never re-resolved. A `net.Dialer` `Control` hook then checks the exact address being
  connected, in the connect path. So **DNS rebinding** (a name that answers with a public address when you create the
  endpoint and a private one when the delivery connects) cannot reach an internal address: there is no second
  resolution between the check and the connect, and the connect itself is checked. A test uses a resolver that
  answers public once and private afterwards.
- **No redirects** are followed (re-validating each hop would also work; refusing is simpler to reason about). **No
  environment proxy** is used (it would route around the dialer).
- **Timeouts:** connect 5 s, TLS 5 s, response headers and total 10 s. The response body is read up to **64 KB** and only
  a 200-character printable hint of a *failed* attempt's body is kept; the console shows it as plain text.
- A development switch, `WEBHOOK_ALLOW_INSECURE_LOCAL=true`, allows `http` and loopback addresses (any port) so a receiver
  on `127.0.0.1` can be tested. It relaxes nothing else (private ranges and metadata stay blocked). Never set it in production.

## Health

Each endpoint shows **Healthy**, **Failing** (N failed deliveries in a row), **Disabled** or **No deliveries yet**, with a
word and a symbol (never colour alone). An endpoint is **disabled automatically** after **50 failed deliveries in a
row, or when every delivery has failed for 3 days**; this is audited (`webhook_endpoint.auto_disable`) and shown with
the reason. Re-enabling clears the counters. A test ping never counts for or against health.
