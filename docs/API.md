# HTTP API

All endpoints are served by `cmd/frontend` on the same origin and port
as the webmail client. The API lives under the `/api` prefix; unknown
`/api` paths return `404`, and every other path serves the SPA.

## Conventions

- Requests and responses are JSON. Bodies are capped at 1 MiB.
- Authenticated endpoints require
  `Authorization: Bearer <token>`; the token comes from
  `POST /api/auth/login` and is an HS256 JWT (default TTL 24h).
- Missing/invalid tokens produce `401` with
  `WWW-Authenticate: Bearer realm="emissary"`.
- Errors are `{"error": "<message>"}`. Messages are generic; details
  are only logged server-side.
- A message outside the caller's visibility returns `404`, the same as
  a message that does not exist.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Exchange credentials for a JWT. |
| `GET` | `/api/auth/check` | — | Report whether the presented token is valid. |
| `POST` | `/api/auth/logout` | Bearer | Client-side token discard hook. |
| `GET` | `/api/emails` | Bearer | List visible messages (admins may filter). |
| `GET` | `/api/emails/{id}` | Bearer | Fetch a single message. |
| `POST` | `/api/emails/{id}/read` | Bearer | Set the read flag. |
| `POST` | `/api/emails/{id}/flag` | Bearer | Set the flagged flag. |
| `POST` | `/api/emails/send` | Bearer | Send a message via the configured sender. |

## `POST /api/auth/login`

Request:

```json
{ "username": "ebola@example.com", "password": "hunter2" }
```

Response (`200`):

```json
{
  "token": "eyJ...",
  "tokenType": "Bearer",
  "expiresAt": "2026-11-12T10:00:00Z",
  "username": "ebola@example.com",
  "email": "ebola@example.com",
  "admin": false
}
```

`401 invalid credentials` on failure. The administrator is checked
first, then the configured user manager backend. The token carries the
user id in `sub` (mirrored in `sid`), `admin` only when true, `iat`,
`exp`, and the configured `aud`/`iss`.

## `GET /api/auth/check`

Returns `{ "authenticated": false }` without a valid token, otherwise:

```json
{ "authenticated": true, "username": "ebola@example.com", "admin": false }
```

## `POST /api/auth/logout`

JWTs are stateless, so nothing is invalidated server-side; the endpoint
exists as a stable hook for future revocation. Returns
`{ "success": true }`. The client is expected to drop the token.

## `GET /api/emails`

Returns a JSON array (never `null`; an empty inbox is `[]`) of the
caller's visible messages:

```json
[
  {
    "id": "h9k2m1q...",
    "from": "sender@example.com",
    "to": "ebola@example.com",
    "subject": "Hello",
    "body": "plain text body",
    "timestamp": "2026-11-11T09:30:00Z",
    "read": false,
    "flagged": false
  }
]
```

- `id` is the S3 object key of the message (the SES message id).
- `to` is the first recipient only; `body` is the plain-text part.
- Ordering is not guaranteed; clients should sort by `timestamp`.
- **Visibility**: ordinary users see only messages where they appear in
  `From`, `To`, `Cc` or `Bcc`. Administrators see everything and may
  append `?user=<address>` to restrict the listing to one address.
- Read/flag state is merged from the metadata store; if metadata is
  unavailable the messages are still served with both flags `false`.

## `GET /api/emails/{id}`

Returns the same shape as one list entry. `404 email not found` when
the id does not exist **or** is not visible to the caller.

## `POST /api/emails/{id}/read`

Request: `{ "read": true }`. Response: `{ "success": true }`.
Applies only to messages visible to the caller.

## `POST /api/emails/{id}/flag`

Request: `{ "flagged": true }`. Response: `{ "success": true }`.
Applies only to messages visible to the caller.

## `POST /api/emails/send`

Request:

```json
{
  "from": "optional@example.com",
  "to": "recipient@example.org",
  "subject": "Subject line",
  "body": "Message text",
  "replyToId": "optional id of the message being replied to"
}
```

Rules:

- `to`, `subject` and `body` are required; `to` must be a single valid
  address.
- **Administrators** may set `from` to any address. When replying
  (`replyToId` set) and `from` is omitted, the From address defaults to
  the original message's first recipient.
- **Ordinary users** always send as their own address; a supplied
  `from` is ignored.
- With no From resolved, the sender's configured address (`SES_FROM`)
  is used.

Responses: `200 {"success": true, "message": "Email sent"}`,
`400 invalid request` / `invalid from address` / `invalid to address`,
or `502 could not send email` when the provider (SES) fails.
