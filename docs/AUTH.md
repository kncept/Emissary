# Authentication

Emissary authenticates with a form login that issues a pair of signed JWTs: a
short-lived **access token** for API calls and a long-lived **refresh token**
for rotating the pair. Every login starts a new session: the session id (`sid`)
stays fixed while the refresh issue number (`issue`) advances on every
rotation, so a refresh token cannot be replayed or used out of sequence.

The wire contract is [`api/auth.yaml`](../api/auth.yaml); error conventions
are shared with the rest of the API ([API.md](API.md#conventions)). Tokens use
[`auth.Claims`](../mailer/auth/auth.go) and
[`auth.Authenticator`](../mailer/auth/auth.go).

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Form login; starts a session and issues the token pair. |
| `POST` | `/api/auth/refresh` | — (refresh token in body) | Exchange the current refresh token for a fresh pair. |
| `POST` | `/api/auth/logout` | Bearer | Revoke the session so its refresh tokens cannot be used. |

Login and refresh are public: credentials or the refresh token travel in the
JSON body. Malformed bodies are `400`; rejected credentials or tokens are `401`
with a generic message. Disabled users get `403`, passwordless users `401`, and
rejected logins over the rate limit `429` with `Retry-After`.

## Login

Login is form-based: `username` (email address) and `password`. The built-in
administrator (`USERNAME`/`PASSWORD`) is checked first, then the configured
user manager (`USER_MANAGER`); both use bcrypt at cost 12. A successful login
creates a new session and returns:

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "eyJ...",
  "tokenType": "Bearer",
  "expiresAt": "2026-11-12T10:00:00Z"
}
```

`expiresAt` is the access token's `exp`. Invalid credentials or a passwordless
user return `401`; a disabled user ([USERS.md](USERS.md)) is rejected with
`403` even with a correct password.

Failed logins are rate limited in two independent one-minute windows: per
client IP and per account (10 failures each by default). Success clears the
account window but not the IP window; over either limit the response is `429`
with `Retry-After` instead of checking the password. The limiter is per
process, so multi-instance deployments should limit at the edge. The leftmost
`X-Forwarded-For` is the client IP when present, but the independent
per-account limit prevents header spoofing. See
[CONFIGURATION.md](CONFIGURATION.md#server-and-authentication) for env vars.

## The token pair

Both tokens are HS256-signed JWTs (`JWT_SECRET`) with `iat`/`exp` in Unix
seconds plus `aud`/`iss` when configured; clients can decode them. See
[`Claims`](../mailer/auth/auth.go) for the complete struct. The `sub` claim is
the user's email address.

| | Access token | Refresh token |
| --- | --- | --- |
| Purpose | General access and API calls | Exchange for a fresh pair |
| Presented as | `Authorization: Bearer <token>` | JSON body of `/api/auth/refresh` only — never as a bearer |
| Duration | 9 hours | 30 days (expires with the session) |
| Earliest use | Immediately | 8 hours after issue (`nbf`) |
| Required claims | `sub`, `sid`, `iat`, `exp` | `sub`, `sid`, `issue`, `nbf`, `iat`, `exp` |

### Access token

Claims that must always be present:

| Claim | Type | Meaning |
| --- | --- | --- |
| `sub` | string | The user id (the user's email address). |
| `sid` | string | The session id; used to ensure strict access/refresh token stepping. |

Optional attributes include `systemManager` (support privileges) and
`EmissaryUserManagement` (manage users and masquerade), set by the user manager
from the built-in admin role or an Emissary user's roles. The obsolete `admin`
claim is no longer issued or honored. The default JMAP `accountId` is the
opaque `acct_` id derived from `sub`; a token with `EmissaryUserManagement`
may supply another known one to masquerade
([USERS.md](USERS.md#admin-masquerade)).

### Refresh token

Claims the refresh token must contain:

| Claim | Type | Meaning |
| --- | --- | --- |
| `sub` | string | The user id (email address); double-checks the session. |
| `sid` | string | The session id. |
| `issue` | integer | This token's issue number; strict ordering is enforced. |
| `nbf` | integer | Not-before time: 8 hours after `iat`; rejected before then. |

The login token carries the first issue number; each refresh increments it with
the same `sid`, and it expires with the 30-day session.

### JWT validation

Every token is fully validated: `HS256`, `typ: JWT`, signature over the exact
header and payload, non-empty `sub`/`sid`, `iat`, `exp > now`, `nbf <= now`
(when present), `iat` not in the future (60 seconds of skew) and `aud`/`iss`
when configured. Access tokens must not carry `issue`/`nbf`; refresh tokens
must carry both. `JWT_TTL` is legacy; lifetimes are fixed as above.

## Refresh and token stepping

`POST /api/auth/refresh` takes the current refresh token and returns a new pair
with `issue` incremented by one and `sid` unchanged:

```json
{ "refreshToken": "eyJ..." }
```

Validation steps:

1. **Full JWT validation** — signature, claims, expiry and the eight-hour `nbf`
   must be valid; a refresh inside `nbf` returns `401`.
2. **Session** — `sub` and `sid` identify an active session.
3. **Strict ordering** — the token must be the latest issued; the highest
   `issue` is persisted, so stepping survives restarts and cold starts.

A stale or unknown `issue` returns `401`; a successful refresh invalidates the
presented token. Consequences:

- Single-use per step: two concurrent refreshes with the same token yield one
  success and one `401`; if a refresh fails, discard both tokens and re-login.
- Refreshing never extends the 30-day session. A refresh in the final eight
  hours is rejected because the new token could not have a valid `nbf` before
  the session expires.

## Logout

`POST /api/auth/logout` takes no body, deletes the session named by the access
token's `sid` and returns `{ "success": true }`. The stable `sid` revokes the
whole refresh chain; the stateless access token remains valid until its 9-hour
expiry, so clients must discard both tokens locally. Calling logout twice is
harmless; live EventSource streams are closed.

## Client guidance

- Store both tokens (the SPA keeps them in `localStorage`).
- Send the access token as `Authorization: Bearer <accessToken>`; never send
  the refresh token as a bearer or in a URL.
- On `401`, call `/api/auth/refresh` once and retry; if it fails, clear the
  tokens and show the login form. Do not refresh before the refresh token's
  `nbf`: the 9-hour access token leaves a one-hour window.
- Treat the refresh token as a credential; do not log it.

## Status

The flow above is implemented, including issue stepping and full JWT validation.
The `ddb` manager is implemented; `ldap` is reserved but unimplemented. See
[API.md](API.md#post-apiauthlogin) for endpoint examples,
[ARCHITECTURE.md](ARCHITECTURE.md#authentication-and-visibility) for enforcement,
and [CONFIGURATION.md](CONFIGURATION.md#server-and-authentication) for env vars.
