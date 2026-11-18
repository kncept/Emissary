# Authentication

Emissary authenticates with a form login that issues a pair of signed
JWTs: a short-lived **access token** for API calls, and a long-lived
**refresh token** for rotating the pair. Every login starts a new
session, and the two tokens step together, so a token cannot outlive
its session or be used out of sequence.

The normative wire contract is [`api/auth.yaml`](../api/auth.yaml);
this document describes the design. Error conventions (status codes,
`{"error": "<message>"}` bodies, generic messages) are shared with the
rest of the API — see [API.md](API.md).

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Form login; starts a session and issues the token pair. |
| `POST` | `/api/auth/refresh` | — (refresh token in body) | Exchange the current refresh token for a fresh pair. |

Both endpoints are public: the credentials (login) or the refresh
token (refresh) travel in the JSON body, so no `Authorization` header
is involved. A malformed body is a `400`; a rejected credential or
token is a `401` with a generic message (details are only logged
server-side).

## Login

Login is form-based: `username` (the user's email address) and
`password`. The built-in administrator (`USERNAME`/`PASSWORD`) is
checked first, then the configured user manager backend
(`USER_MANAGER`). Support for other login types will be added later,
as required.

A successful login creates a new session and returns the token pair:

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "eyJ...",
  "tokenType": "Bearer",
  "expiresAt": "2026-11-12T10:00:00Z"
}
```

`expiresAt` is the access token's `exp` claim. Invalid credentials
return `401`; a disabled user (see [USERS.md](USERS.md)) is rejected
with `403`, even with a correct password. Users created without a
password are always rejected with `401` — a blank password never
authenticates anyone.

## The token pair

Both tokens are HS256-signed JWTs (signed with `JWT_SECRET`; standard
`iat`/`exp` claims in Unix seconds, plus `aud`/`iss` when
configured). Both are full JWTs rather than opaque strings, so a
client can decode and inspect the claims.

| | Access token | Refresh token |
| --- | --- | --- |
| Purpose | General access and API calls | Exchange for a fresh pair |
| Presented as | `Authorization: Bearer <token>` | JSON body of `/api/auth/refresh` only — never as a bearer |
| Duration | 9 hours | 30 days |
| Required claims | `sub`, `sid` | `sub`, `sid`, `issue` |

### Access token

The following claims must always be present:

| Claim | Type | Meaning |
| --- | --- | --- |
| `sub` | string | The user id (the user's email address). |
| `sid` | string | The session id; used to ensure strict access/refresh token stepping. |

In addition to standard claims, the JWT MAY have the following
attributes: `Subscribed` (boolean), `systemManager` (boolean),
`EmissaryUserManagement` (boolean). The user manager decides which apply
to the authenticated user. The built-in administrator receives both
privilege claims; an Emissary-managed user receives them from their
System Manager and User Manager roles. The obsolete `admin` claim is no
longer issued or honored.

The `sub` claim is also the default JMAP `accountId`: mail operations are
scoped to that account's mailbox prefix. A user without User Manager
permission cannot override it; a token with `EmissaryUserManagement: true`
may supply another `accountId` on a JMAP method call to masquerade
(see [USERS.md](USERS.md#admin-masquerade)).

### Refresh token

The refresh token must contain:

| Claim | Type | Meaning |
| --- | --- | --- |
| `sub` | string | The user id (the user's email address); used to double-check the session. |
| `sid` | string | The session id. |
| `issue` | integer | The issue number of this refresh token; strict ordering is enforced. |

The refresh token issued at login carries the session's first issue
number; every subsequent refresh increments it by one.

## Refresh and token stepping

`POST /api/auth/refresh` accepts the session's current refresh token
and returns a new access token and a new refresh token with `issue`
incremented by one:

```json
{ "refreshToken": "eyJ..." }
```

The server validates every refresh request:

1. **Signature and expiry** — the token must be a valid, unexpired
   HS256 JWT.
2. **Session** — `sub` and `sid` identify an active session, and
   `sub` double-checks that the token belongs to the expected user.
3. **Strict ordering** — the token must be the latest one issued for
   its session. The server persists the highest `issue` issued per
   session, so stepping survives restarts and Lambda cold starts.

A successful refresh invalidates the token that was presented; a
refresh token with a stale or unknown `issue` is rejected with `401`.
Every refresh therefore moves the session exactly one step forward —
this is the strict access/refresh token stepping that `sid` exists
for. Consequences:

- The refresh token is single-use per step. If a refresh fails,
  discard both tokens and re-login.
- Two concurrent refreshes with the same token: the first wins, the
  second gets `401`.

## Client guidance

- Store both tokens (the SPA keeps them in `localStorage`).
- Send the access token as `Authorization: Bearer <accessToken>`;
  never send the refresh token as a bearer.
- When an API call returns `401`, call `/api/auth/refresh` once and
  retry; if the refresh fails, clear the stored tokens and show the
  login form.
- Treat the refresh token as a credential: do not log it, and never
  put it in a URL.

## Status

This document is the contract to implement. The currently deployed
handler still issues a single access token (default TTL 24 hours) with
no refresh flow. The `ddb` user manager is implemented; the `ldap`
user manager is reserved but unimplemented.

See [API.md](API.md) for the endpoints as implemented,
[ARCHITECTURE.md](ARCHITECTURE.md) for the authentication and
visibility flow, and [CONFIGURATION.md](CONFIGURATION.md) for
`USERNAME`, `PASSWORD`, `JWT_SECRET` and `USER_MANAGER`.
