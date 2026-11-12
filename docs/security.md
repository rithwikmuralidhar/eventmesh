# Security

## Credentials

| Credential | Used for | Grants |
| --- | --- | --- |
| Session cookie `eventmesh_session` | Dashboard | All dashboard/management routes |
| API key `Authorization: Bearer em_...` | External clients | `POST /api/v1/events` only |

### Sessions, not JWTs

Server-side sessions can be revoked instantly (logout deletes the row; changing the password ends other sessions), and
the browser `EventSource` cannot send an `Authorization` header, so a cookie fits SSE. The cookie is 32 random bytes,
`HttpOnly`, `SameSite=Lax`, `Secure` when `EM_ENV=production`; only its SHA-256 is stored; 7-day sliding expiry.

### CSRF

State-changing cookie-authenticated routes require the custom header `X-Requested-With` (a cross-site form cannot set
it, and a cross-origin `fetch` with it triggers a CORS preflight that is refused for origins not on the allowlist), and
an `Origin` header, when present, must be on `EM_CORS_ALLOWED_ORIGINS` or be the API's own origin (needed when the API
serves the frontend). `middleware.CSRF`; covered by integration tests.

### Passwords

Argon2id, 64 MB, 3 iterations, parallelism 2, random salt, minimum length 10 (`auth.PasswordHasher`). Login and
registration are rate-limited per IP and per email, and unknown-email and wrong-password return the same error.
Argon2id is memory-hard because passwords have low entropy and must resist offline GPU guessing.

### API keys: hashed, not encrypted

Format `em_<8-char prefix>_<43 base62 chars>` from `crypto/rand`; shown once. They are looked up by `SHA-256(key)`.
A key already has 256 bits of randomness, so there is nothing to brute-force and a fast hash is correct (bcrypt/Argon2
would only slow every ingestion request). `last_used_at` is updated at most once a minute per key.

### Endpoint secrets: encrypted, not hashed

HMAC signing needs the plaintext secret at send time, so it cannot be hashed. It is encrypted with AES-256-GCM
(`EM_SECRET_ENCRYPTION_KEY`, 32 bytes base64, random nonce stored before the ciphertext) and decrypted only in the
worker when signing. It is shown once at creation and once after rotation and never appears in logs. The built-in
development key is public; `EM_ENV=production` refuses to start with it.

## Authorization

The user always comes from the session or API key, never from the client. Every query on a user-owned resource has
`user_id` in its `WHERE` clause (in the sqlc SQL itself). Another user's resource returns `404`, not `403`. Tests
(`TestUserBCannotTouchUserAResources`, `TestAuditLogsAreScopedToTheUser`, SSE isolation) cover endpoints, events,
deliveries, API keys, audit logs and the live stream.

## Webhook signing

Each request is a `POST` with `Content-Type: application/json`, `User-Agent: EventMesh/1.0` and:

```
X-EventMesh-Event-Id, X-EventMesh-Event-Type, X-EventMesh-Delivery-Id, X-EventMesh-Attempt,
X-EventMesh-Timestamp: <unix seconds>, X-EventMesh-Signature: v1=<hex HMAC-SHA256>
```

The signature is `HMAC-SHA256(secret, "<timestamp>.<exact body bytes>")`. **Consumers verify** by recomputing over the
raw body, comparing in constant time, and rejecting timestamps older than five minutes.

Go:

```go
func verify(secret, sigHeader, tsHeader string, body []byte) bool {
    ts, err := strconv.ParseInt(tsHeader, 10, 64)
    if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute { return false }
    mac := hmac.New(sha256.New, []byte(secret))
    fmt.Fprintf(mac, "%d.", ts)
    mac.Write(body)
    want := "v1=" + hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(want), []byte(sigHeader))
}
```

Node.js:

```js
const crypto = require('node:crypto')
function verify(secret, sigHeader, tsHeader, rawBody) {
  const ts = Number(tsHeader)
  if (!Number.isFinite(ts) || Math.abs(Date.now() / 1000 - ts) > 300) return false
  const want = 'v1=' + crypto.createHmac('sha256', secret).update(`${ts}.`).update(rawBody).digest('hex')
  const a = Buffer.from(want), b = Buffer.from(sigHeader)
  return a.length === b.length && crypto.timingSafeEqual(a, b)
}
```

The local receiver (`cmd/receiver`) runs the same verification (`webhook.Verify`) and reports it per request.

## SSRF protection

Webhook URLs are user-supplied. All checks live in `internal/ssrf` and are used by endpoint validation and the worker's
HTTP client: `http`/`https` only, no credentials in the URL, and loopback, private, link-local (including the cloud
metadata address), CGNAT, multicast, unspecified and reserved ranges are rejected for IPv4, IPv6 and IPv4-mapped IPv6.
The authoritative check is at **connection time**: the worker's `net.Dialer.Control` (`ssrf.DialControl`) inspects the
address the socket actually uses, which defeats DNS rebinding (a name that resolves to a public address at validation
time and a private one at connect time). Redirects are not followed; proxies are disabled; dial/TLS/header/total timeouts
are 5/5/10/15 s; at most 64 KB of a response is read and 2 KB stored. Creation-time validation is early feedback only.

**Local development exception:** `EM_SSRF_ALLOWED_HOSTS` (empty by default, `127.0.0.1,localhost` in `.env.example`)
exempts hosts from the IP checks so demo deliveries can reach the local receiver. A warning is logged at startup when
it is set, and production refuses to start with it non-empty. Tests cover both paths.

**Limits:** application-level SSRF protection cannot see network topology, a hostname allowlist is only as good as its
owner's DNS, and it does not constrain what an allowed host does. In production, network egress controls (an egress
proxy or firewall that blocks internal ranges) are the real defence.

## Rate limiting

One Redis token bucket (Lua, server time) for the global per-IP limit (100/s, burst 200), per-user ingestion
(50/s, burst 100) and per-endpoint outgoing delivery. API limits fail open with a warning if Redis is unreachable, because
refusing every request when the limiter is down would turn a Redis outage into a full outage; the trade-off is a window
with no limiting.

## Hardening and logging

Body size limits (256 KB payload), strict CORS allowlist, error responses never include SQL or stack traces,
production refuses development defaults. Logs never contain passwords, API keys, session tokens, endpoint secrets,
signatures or event payloads; `TestLogsNeverContainSecretsOrPayloads` captures the API and worker logs through a
delivery and a failure and asserts their absence.

## AI features

Not built (milestone 11 is optional). No data is sent to any AI provider.
