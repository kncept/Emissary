# HTTP API

All endpoints are served by `cmd/frontend` on the same origin and port
as the webmail client. The API lives under the `/api` prefix; unknown
`/api` paths return `404`, and every other path serves the SPA.

## Generated interface stubs

The wire contract is defined by the OpenAPI specifications and is the
single source of truth. Both the backend and the frontend consume
generated interface stubs, ensuring zero drift between server
implementation and client consumption.

- **Backend (Go)**: `ogen` generates server interfaces, request/response
  types, decoders, encoders, validators, and router middleware under
  `api/*/generated/`. The handler implements the generated server
  interfaces; the generated client interfaces are available for
  service-to-service calls.
- **Frontend (TypeScript)**: An OpenAPI-to-TypeScript generator
  produces typed interfaces and a fetch client under
  `frontend/src/api/generated/`. The frontend code imports this client
  instead of hand-written fetch wrappers, so every request/response
  shape is guaranteed to match the spec.

## Specs and path ownership

The wire contract is defined by four OpenAPI documents, and they must
never overlap:

- [`api/auth.yaml`](../api/auth.yaml) — authentication. **Every
  operation lives under the reserved URL prefix `/api/auth/`.** The
  prefix is owned exclusively by this document: it never defines
  operations outside the prefix, and no other document may define a
  path under it.
- [`api/users.yaml`](../api/users.yaml) — user management (admin only).
  **Every operation lives under the reserved URL prefix `/api/user/`.**
  The prefix is owned exclusively by this document: it never defines
  operations outside the prefix, and no other document may define a
  path under it. These endpoints require the `EmissaryUserManagement`
  JWT claim and an Emissary-mastered user manager (the `ddb` backend).
- [`api/jmap.yaml`](../api/jmap.yaml) — JMAP (RFC 8620/8621): the
  session resource, the batched API endpoint, binary upload/download
  and the EventSource connection. It defines **no** auth operations;
  none of its paths may fall under `/api/auth/`, `/api/user/` or
  `/api/support/`.
- [`api/support.yaml`](../api/support.yaml) — administrative support and
  diagnostics (admin only). **Every operation lives under the reserved
  URL prefix `/api/support/`.** The prefix is owned exclusively by this
  document: it never defines operations outside the prefix, and no other
  document may define a path under it. These endpoints require the
  `Admin` JWT claim; see [SUPPORT.md](SUPPORT.md).

The rule is enforced by `api/spec_test.go` (run by `./run.sh test`):
every path in `auth.yaml` must start with `/api/auth/`, every path in
`users.yaml` must start with `/api/user/`, every path in `support.yaml`
must start with `/api/support/`, no path in `jmap.yaml` may fall under
any of those prefixes, and the four documents must never share a
(method, path) operation.

`GET /api/user/autocomplete` is the one `/api/user/` operation that is
not generated from `users.yaml`: it is served directly by the configured
`UserManager` and is documented below.

## Conventions

- Requests and responses are JSON. Bodies are capped at 1 MiB.
- Authenticated endpoints require
  `Authorization: Bearer <token>`; the token comes from
  `POST /api/auth/login` and is an HS256 JWT (default TTL 24h).
- Missing/invalid tokens produce `401` with
  `WWW-Authenticate: Bearer realm="emissary"`.
- Errors are `{"error": "<message>"}`. Messages are generic; details
  are only logged server-side.
- A message outside the selected account is not discoverable: it is
  omitted from `Email/query` and reported in `Email/get`'s `notFound`
  list, the same as a message that does not exist.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Exchange credentials for a JWT. |
| `POST` | `/api/auth/logout` | Bearer | Client-side token discard hook. |
| `GET` | `/api/user/` | Bearer\* | List all users (paginated). |
| `POST` | `/api/user/` | Bearer\* | Create a new user. |
| `GET` | `/api/user/{email}` | Bearer\* | Get a user by email. |
| `PATCH` | `/api/user/{email}` | Bearer\* | Update a user. |
| `DELETE` | `/api/user/{email}` | Bearer\* | Delete a user. |
| `POST` | `/api/user/{email}/password` | Bearer† | Change a user's password. |
| `GET` | `/api/user/autocomplete` | Bearer | Search user IDs (email addresses) for admin masquerade. |
| `GET` | `/api/support/configurations` | Bearer‡ | List configuration sections and implementations. |
| `GET` | `/api/support/configurations/{name}` | Bearer‡ | Get full settings for one configuration section. |
| `GET` | `/api/support/bucket/root` | Bearer‡ | List unprocessed staging objects at the bucket root. |
| `POST` | `/api/support/process-email` | Bearer‡ | Run the inbound pipeline on a staging object. |
| `GET` | `/.well-known/jmap` | Bearer | Get the JMAP Session object (capabilities, accounts). |
| `POST` | `/api/jmap/` | Bearer | JMAP batch API endpoint for all mail operations. |
| `POST` | `/upload/{accountId}` | Bearer | Upload binary data (attachments). |
| `GET` | `/download/{accountId}/{blobId}/{name}` | Bearer | Download binary data. |
| `GET` | `/eventsource/` | Bearer | EventSource push connection. |

\* Requires `EmissaryUserManagement` JWT claim and Emissary-mastered user manager (the `ddb` backend).

† Any authenticated user can change their own password. Administrators with `EmissaryUserManagement` can change any user's password. Requires Emissary-mastered user manager (`ddb` backend).

‡ Requires the `Admin` JWT claim (the built-in administrator). See [SUPPORT.md](SUPPORT.md).

## JMAP Mail Operations

All email operations are performed via the JMAP batch endpoint (`POST /api/jmap/`).
Mail is modeled with JMAP's **account → mailbox → email** hierarchy: the
`accountId` selects the user's account and `filter.inMailbox` selects a
JMAP mailbox such as `inbox`, `sent`, `drafts`, `trash`, `archive` or
`junk`. The storage layer lists only that account's object-key prefix
(`users/<accountId>/<mailboxId>/`), so other users' mail is never read.

### Account selection and masquerade

The default `accountId` is the authenticated JWT `sub`. A non-admin call
that supplies a different `accountId` is rejected. An administrator may
supply any `accountId` to read or act in that account's mailbox
(masquerade); this applies to `Email/query`, `Email/get`, `Email/set`
and `EmailSubmission/set`.

### List Emails
- `Email/query` with filter `inMailbox: "inbox"`, sort by `receivedAt` descending
- `Email/get` with the returned IDs to fetch full email objects

### Get Single Email
- `Email/get` with the specific email ID

### Mark Read/Unread
- `Email/set` with update to keywords: `{ $seen: true/false }`

### Flag/Unflag
- `Email/set` with update to keywords: `{ $flagged: true/false }`

### Delete Email
- `Email/set` with destroy containing the email ID

### Send Email
- `Email/set` create to store the email in the `drafts` mailbox
- `EmailSubmission/set` create to submit the email for delivery
- `onSuccessUpdateEmail` to move from drafts to sent
- `onSuccessDestroyEmail` to remove the draft

## `POST /api/auth/login`

Request:

```json
{ "username": "ebola@example.com", "password": "hunter2" }
```

Response (`200`):

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "eyJ...",
  "tokenType": "Bearer",
  "expiresAt": "2026-11-12T10:00:00Z"
}
```

`401 invalid credentials` on failure. The administrator is checked
first, then the configured user manager backend. The access token carries the
user id (the user's email address) in `sub`, the session id in `sid`,
`admin` only when true, `iat`, `exp`, and the configured `aud`/`iss`.

## `POST /api/auth/logout`

JWTs are stateless, so nothing is invalidated server-side; the endpoint
exists as a stable hook for future revocation. Returns
`{ "success": true }`. The client is expected to drop the token.

## `GET /api/user/autocomplete`

Searches user IDs (email addresses) for admin masquerade. The endpoint is
backed by `UserManager.Autocomplete`, not by the generated management
server, and is available to administrators.

Query parameter `q` is required and must be at least 3 characters.

Response (`200`):

```json
{
  "users": [
    "user@example.com",
    "other@example.com"
  ]
}
```

Returns up to 10 matches. An empty list means there are no matches (not a
`404`).

## Support API

The administrative support endpoints under `/api/support/` are documented in
[SUPPORT.md](SUPPORT.md). They require the `Admin` JWT claim and expose:

- `GET /api/support/configurations` — configuration sections and their
  implementations.
- `GET /api/support/configurations/{name}` — full settings for one section,
  with secrets redacted.
- `GET /api/support/bucket/root` — staging objects at the bucket root (S3
  storage only).
- `POST /api/support/process-email` — run the inbound pipeline on a staging
  object (S3 storage only).

## `GET /.well-known/jmap`

Returns the JMAP Session object with capabilities, account information,
and API endpoints:

```json
{
  "capabilities": {
    "urn:ietf:params:jmap:core": {},
    "urn:ietf:params:jmap:mail": {},
    "urn:ietf:params:jmap:submission": {},
    "urn:ietf:params:jmap:vacationresponse": {}
  },
  "accounts": {
    "user@example.com": {
      "name": "user@example.com",
      "isPersonal": true,
      "isReadOnly": false,
      "accountCapabilities": {
        "urn:ietf:params:jmap:core": {},
        "urn:ietf:params:jmap:mail": {},
        "urn:ietf:params:jmap:submission": {},
        "urn:ietf:params:jmap:vacationresponse": {}
      }
    }
  },
  "primaryAccounts": {
    "urn:ietf:params:jmap:core": "user@example.com",
    "urn:ietf:params:jmap:mail": "user@example.com",
    "urn:ietf:params:jmap:submission": "user@example.com",
    "urn:ietf:params:jmap:vacationresponse": "user@example.com"
  },
  "username": "user@example.com",
  "apiUrl": "/api/jmap/",
  "downloadUrl": "/download/{accountId}/{blobId}/{name}",
  "uploadUrl": "/upload/{accountId}/",
  "eventSourceUrl": "/eventsource/",
  "state": "state123"
}
```

## `POST /api/jmap/` (JMAP Batch Request)

Processes an ordered batch of JMAP method calls. The request and
response bodies are JSON `Request` and `Response` objects. Method
failures are returned inside `methodResponses` as `error`
invocations; only request-level failures use an HTTP error status.

Example request to list emails:

```json
{
  "using": ["urn:ietf:params:jmap:mail"],
  "methodCalls": [
    ["Email/query", {
      "accountId": "user@example.com",
      "filter": { "inMailbox": "inbox" },
      "sort": [{ "property": "receivedAt", "isAscending": false }],
      "collapseThreads": false
    }, "query1"],
    ["Email/get", {
      "accountId": "user@example.com",
      "ids": { "resultOf": "query1", "name": "Email/query", "path": "/ids/*" },
      "properties": ["id", "from", "to", "subject", "preview", "receivedAt", "keywords", "mailboxIds"]
    }, "get1"]
  ]
}
```

Example response:

```json
{
  "methodResponses": [
    ["Email/query", {
      "accountId": "user@example.com",
      "queryState": "state123",
      "ids": [
        "users/user@example.com/inbox/2026-11-17T10:30:45Z_01000192a3b4c5d6.eml",
        "users/user@example.com/inbox/2026-11-17T09:12:00Z_01000192abcdef.eml"
      ],
      "total": 2
    }, "query1"],
    ["Email/get", {
      "accountId": "user@example.com",
      "state": "state123",
      "list": [...],
      "notFound": []
    }, "get1"]
  ],
  "sessionState": "state123"
}
```

Email IDs are the full delivered object keys, including the received
timestamp and `.eml` suffix. They contain `/`, so clients must URL-encode
them when placing an ID in a route. The `mailboxIds` returned by
`Email/get` reflect the mailbox the message is actually stored in.

## Binary Upload/Download

- `POST /upload/{accountId}` — Upload binary data (e.g., attachments). Returns `{ "accountId": "...", "blobId": "...", "type": "application/pdf", "size": 12345 }`
- `GET /download/{accountId}/{blobId}/{name}?type=...` — Download binary data

## EventSource Push

- `GET /eventsource/?types=*&ping=30` — Long-running Server-Sent Events connection for push notifications.