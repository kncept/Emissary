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
- [`api/users.yaml`](../api/users.yaml) — user management.
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
  diagnostics (System Managers only). **Every operation lives under the reserved
  URL prefix `/api/support/`.** The prefix is owned exclusively by this
  document: it never defines operations outside the prefix, and no other
  document may define a path under it. These endpoints require the
  `systemManager` JWT claim; see [SUPPORT.md](SUPPORT.md).

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
  are only logged server-side. A rate-limited login returns `429` with
  a `Retry-After` header (see [AUTH.md](AUTH.md)).
- A message outside the selected account is not discoverable: it is
  omitted from `Email/query` and reported in `Email/get`'s `notFound`
  list, the same as a message that does not exist.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Exchange credentials for a token pair. |
| `POST` | `/api/auth/refresh` | — (refresh token in body) | Rotate the token pair in the same session. |
| `POST` | `/api/auth/logout` | Bearer | Revokes the session, invalidating its refresh tokens. |
| `GET` | `/api/user/` | Bearer\* | List all users (paginated). |
| `POST` | `/api/user/` | Bearer\* | Create a new user. |
| `GET` | `/api/user/{email}` | Bearer\* | Get a user by email. |
| `PATCH` | `/api/user/{email}` | Bearer\* | Update a user. |
| `DELETE` | `/api/user/{email}` | Bearer\* | Delete another user (not yourself). |
| `GET` | `/api/user/password-change-supported` | Bearer | Whether this backend supports password changes. |
| `POST` | `/api/user/change-password` | Bearer | Change the authenticated user's own password. |
| `POST` | `/api/user/{email}/password` | Bearer† | Change a user's password. |
| `GET` | `/api/user/autocomplete` | Bearer | Search user IDs (email addresses) for admin masquerade. |
| `GET` | `/api/support/configurations` | Bearer‡ | List configuration sections and implementations. |
| `GET` | `/api/support/configurations/{name}` | Bearer‡ | Get full settings for one configuration section. |
| `GET` | `/api/support/bucket/root` | Bearer‡ | List unprocessed staging objects at the bucket root. |
| `POST` | `/api/support/process-email` | Bearer‡ | Run the inbound pipeline on a staging object. |
| `GET` | `/api/support/receiver/identities` | Bearer‡ | List receiving mail identities (verified SES identities) and their receiver rule coverage. |
| `GET` | `/api/support/receiver/rules` | Bearer‡ | List the configured receiver rules. |
| `PUT` | `/api/support/receiver/rules` | Bearer‡ | Add or replace receiver rules entries. |
| `DELETE` | `/api/support/receiver/rules/{identity}` | Bearer‡ | Remove a configured receiving identity (never the `_` fallback). |
| `GET` | `/.well-known/jmap` | Bearer | Get the JMAP Session object (capabilities, accounts). |
| `POST` | `/api/jmap/` | Bearer | JMAP batch API endpoint for all mail operations. |
| `POST` | `/upload/{accountId}` | Bearer | Upload binary data (attachments). |
| `GET` | `/download/{accountId}/{blobId}/{name}` | Bearer | Download binary data. |
| `GET` | `/api/email/raw` | Bearer | Get the original RFC 5322 message for one email id. |
| `GET` | `/eventsource` | Bearer | EventSource push connection. |

\* Requires `EmissaryUserManagement` JWT claim and an Emissary-mastered user manager (`ddb`, `filesystem` or `s3`). A user cannot delete their own account (`403 Forbidden`).

† Any authenticated user can change their own password. Administrators with `EmissaryUserManagement` can change any user's password. Requires an Emissary-mastered user manager (`ddb`, `filesystem` or `s3`).

`/api/user/password-change-supported` and `/api/user/change-password` need
only a valid bearer token: no management claim. The capability endpoint
returns `{"supported": true|false}` for every backend and is intended to be
cached in memory by clients; the Settings screen uses it to hide the password
UI when it is unsupported. The change endpoint takes no email in the path,
verifies the supplied current password, and revokes all sessions for that
account, so the user must sign in again. It returns `403` when the configured
backend cannot store a password verifier.

‡ Requires the `systemManager` JWT claim. See [SUPPORT.md](SUPPORT.md).

## JMAP Mail Operations

All email operations are performed via the JMAP batch endpoint (`POST /api/jmap/`).
Mail is modeled with JMAP's **account → mailbox → email** hierarchy: the
`accountId` selects the user's account and `filter.inMailbox` selects a
JMAP mailbox such as `inbox`, `sent`, `drafts`, `trash`, `archive` or
`junk`. The storage layer lists only that account's object-key prefix
(`users/<accountId>/<mailboxId>/`), so other users' mail is never read.

### Account selection and masquerade

The default `accountId` is the authenticated JWT `sub`. A call without
User Manager permission that supplies a different `accountId` is rejected.
A user with `EmissaryUserManagement` may supply any `accountId` to read or act
in that account's mailbox (masquerade); this applies to `Email/query`,
`Email/get`, `Email/set` and `EmailSubmission/set`.

### List Emails
- `Email/query` with filter `inMailbox: "inbox"`, sort by `receivedAt` descending
- `Email/get` with the returned IDs to fetch full email objects

### Get Single Email
- `Email/get` with the specific email ID

### Received time

`Email/receivedAt` is a server-owned value stored in the per-message metadata
record as `receivedAt` (RFC 3339 UTC). It is stamped when the record is created
— at inbound delivery or JMAP `Email/set.create` — and is never assignable,
editable or updatable by clients. A `receivedAt` property sent in a create or
update patch is ignored. `Email/query` sorts by this value; messages with no
stored value (delivered before the field existed) fall back to the RFC 5322
`Date` header, which is also reported separately as `sentAt`.

### Keywords (message state)

JMAP models message state as a `String[Boolean]` map of **keywords**
(RFC 8621 §4.1.1). Emissary stores these in the metadata service — alongside
the server-owned `receivedAt` field — and stitches them into every
`Email/get` response. Only keywords that are set are returned, with the value
`true`; clearing a keyword removes it from the map.

The following system keywords are supported:

| Keyword | Meaning |
| --- | --- |
| `$draft` | Message is a draft. |
| `$seen` | Message has been read. Used for JMAP unread counts; drafts are excluded from those counts. |
| `$flagged` | Message is flagged/starred. |
| `$answered` | Message has been replied to. |
| `$forwarded` | Message has been forwarded. |
| `$junk` | Message is junk/spam. |
| `$notjunk` | Message has been marked as not junk. |
| `$phishing` | Message has been marked as phishing. |

Keyword names are case-insensitive on input and normalized to lowercase on
storage and output.

**Mark read / unread**

```json
{ "method": "Email/set", "args": { "update": { "<id>": { "keywords/$seen": true } } } }
```

To mark unread, send `null` (not `false`) for the keyword:

```json
{ "method": "Email/set", "args": { "update": { "<id>": { "keywords/$seen": null } } } }
```

**Flag / unflag** uses `keywords/$flagged` in the same way.

Other keywords (for example `keywords/$answered`) follow the same pattern. A
whole `"keywords"` object may also be supplied to replace the entire keyword
set; `false` entries in that object are dropped.

JMAP `/set.update` is a patch: `true` adds a keyword, `null` removes it, and
`false` is invalid (`invalidProperties`). Unknown properties (such as
`mailboxIds`) are ignored rather than failing the update. Updates are scoped to
the requested `accountId`, so a crafted id cannot modify another user's mail.

New deliveries created by the receiver start with no keywords (unread and
unflagged) and a freshly stamped `receivedAt`. If a message has no metadata
record at all — for example one delivered before the metadata service existed —
`Email/get` still succeeds, returns an empty `keywords` object, and falls back
to the message `Date` for `receivedAt`. See
[ARCHITECTURE.md](ARCHITECTURE.md#inbound-mail-flow) for the metadata creation
point in the delivery pipeline.

### Delete Email
- `Email/set` with destroy containing the email ID

### View/Download raw MIME

`GET /api/email/raw?id=<email-id>` returns the original RFC 5322 bytes of a
message (all headers, MIME parts and transfer encodings) with a
`message/rfc822` content type. The JMAP `Email/get` representation is parsed
and intentionally drops non-text parts, so it cannot be used to reconstruct
the wire format. Delivered ids contain slashes, which is why the id is passed
as a query parameter instead of a URL path segment.

The account that owns the message is derived from its delivered object key (or
from the parsed record for storage backends with opaque ids). Users may only
read raw messages from their own account; callers with `EmissaryUserManagement`
may read any account, matching JMAP masquerade behavior. Missing messages
return `404`, and malformed or unauthorized ids return `400`/`403`.

The web UI uses this endpoint for both the "View MIME" modal and the
"Download" button.

### Send Email
- `Email/set` create to store the email in the `drafts` mailbox. With a JWT
  `sid`, the draft is written to durable per-session storage rather than the
  delivered-mail tree, so the flow works with `STORAGE=s3` in Lambda.
- `EmailSubmission/set` create to submit the email for delivery. The submitted
  `emailId` may be the session-relative draft id returned by `Email/set`.

**Locked From address.** The sender is always the authenticated user: a
create or submission acts on the caller's own account (`JWT sub`) and sends
as that address. A caller with `EmissaryUserManagement` may act on a
different `accountId` (masquerade) and then sends as that account's address.
The From address supplied by the client — the draft's `from` header in
`Email/set` or the envelope `mailFrom` in `EmailSubmission/set` — is ignored;
there is no way to send as any other address. An account address that is not
a valid email address (no `@`) is rejected with `invalidProperties`.

**Recipients.** `to`, `cc` and `bcc` each accept any number of addresses.
If an envelope `rcptTo` is supplied, it must cover every To, Cc and Bcc
address (otherwise a `forbidden` submission error); Bcc addresses appear only
in the delivery envelope, never in the message headers.

**Thread headers.** The draft's `inReplyTo` and `references` are carried into
the delivered message as `In-Reply-To` and `References` headers. The message
gets a `Message-ID` at `Email/set` create time; the sender reuses that id
instead of generating a new one, so the stored and delivered copies agree.

- When masquerading, Reply-To must match From. It defaults to From when
  omitted; a conflicting Reply-To is rejected with `invalidProperties`.
- The session draft and its metadata (keywords and `receivedAt`) are deleted
  after `EmailSubmission/set` sends successfully.
  `onSuccessUpdateEmail` and `onSuccessDestroyEmail` are accepted for JMAP
  compatibility but the server performs the cleanup itself.

### Reply and Forward (web UI)

The web UI's Reply and Forward flows build the outgoing message with these
conventions; they are a client-side contract, not JMAP requirements.

**Subject.** A reply is prefixed `RE: ` and a forward `FWD: `
(`RE: <subject>`, `FWD: <subject>`). If the original subject already begins
with the same prefix (case-insensitive, e.g. `Re:` or `re`), no second prefix
is added. A reply to a forwarded message therefore reads `RE: FWD: <subject>`.

**Body (top posting).** The user's new text is placed on top; underneath it
comes the original message's text, separated by a plain-text separator line
(`-----`). The layout of the delivered text body is:

```
<new text>
<blank line>
<blank line>
<blank line>
<blank line>
<blank line>
<blank line>
-----
<blank line>
<original message text>
```

that is, exactly six blank lines above the separator and one blank line
underneath it. In HTML mode the same layout is rendered with line breaks, a
separator element and the quoted text in a `<pre>` block; the plain-text
alternative carries the exact layout above. The quoted part is the original
message's text body (an HTML-only original is quoted as its extracted text).

**Thread headers.** Both replies and forwards set `In-Reply-To` to the
original message's `Message-ID`, and `References` to the original's
`References` list with the original's `Message-ID` appended (duplicates
removed, order preserved). These headers are not shown in the UI.

**Recipients.** A reply goes to the original's `Reply-To` address, or its
`From` address when there is no `Reply-To`. A forward starts with no
recipients; the user adds them. Both support multiple To, Cc and Bcc
addresses (comma-separated in the UI). The From address is locked as
described above — when masquerading, the reply or forward is sent as the
masquerade target.

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
`systemManager` and `EmissaryUserManagement` when true, `iat`, `exp`, and
the configured `aud`/`iss`. The refresh token carries `sub`, `sid`, the
current `issue`, `iat`, `exp` and `nbf` (eight hours after `iat`).

## `POST /api/auth/refresh`

Request:

```json
{ "refreshToken": "eyJ..." }
```

Returns the same shape as login with a fresh pair. The session id (`sid`)
is unchanged; the refresh token's `issue` is incremented by one, which
invalidates the token that was presented. The refresh token is unusable
before its `nbf` (eight hours after issue) and expires with the 30-day
session. A missing `refreshToken` is `400`; an invalid, expired, too-early
or spent token is `401`. Every token is fully validated — signature,
`alg`/`typ`, `sub`, `sid`, `iat`, `exp`, `nbf` and configured `aud`/`iss`.
See [AUTH.md](AUTH.md) for the full token design.

## `POST /api/auth/logout`

Requires a valid access token. The session named by its `sid` claim is
deleted, so every refresh token in that session can no longer be exchanged;
the endpoint returns `{ "success": true }`. Because `sid` is stable across
refreshes, one logout revokes the whole chain. Access tokens are stateless
and remain valid until their 9-hour expiry, so the client must still drop
both tokens locally. Repeating the call is harmless.

## `GET /api/user/autocomplete`

Searches user IDs (email addresses) for User Manager masquerade. The
endpoint is backed by `UserManager.Autocomplete`, not by the generated
management server, and is available to users with `EmissaryUserManagement`.

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
[SUPPORT.md](SUPPORT.md). They require the `systemManager` JWT claim and expose:

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
    "urn:ietf:params:jmap:core": {
      "maxSizeUpload": 10485760,
      "maxConcurrentUpload": 0,
      "maxSizeRequest": 10485760,
      "maxConcurrentRequests": 4,
      "maxCallsInRequest": 16,
      "maxObjectsInGet": 500,
      "maxObjectsInSet": 500,
      "collationAlgorithms": []
    },
    "urn:ietf:params:jmap:mail": {},
    "urn:ietf:params:jmap:submission": {}
  },
  "accounts": {
    "user@example.com": {
      "name": "user@example.com",
      "isPersonal": true,
      "isReadOnly": false,
      "accountCapabilities": {
        "urn:ietf:params:jmap:core": {
          "maxSizeUpload": 10485760,
          "maxConcurrentUpload": 0,
          "maxSizeRequest": 10485760,
          "maxConcurrentRequests": 4,
          "maxCallsInRequest": 16,
          "maxObjectsInGet": 500,
          "maxObjectsInSet": 500,
          "collationAlgorithms": []
        },
        "urn:ietf:params:jmap:mail": {
          "maxMailboxesPerEmail": 1,
          "maxMailboxDepth": 1,
          "maxSizeMailboxName": 255,
          "maxSizeAttachmentsPerEmail": 10485760,
          "emailQuerySortOptions": ["receivedAt"],
          "mayCreateTopLevelMailbox": false
        },
        "urn:ietf:params:jmap:submission": {
          "maxDelayedSend": 0,
          "submissionExtensions": {}
        }
      }
    }
  },
  "primaryAccounts": {
    "urn:ietf:params:jmap:mail": "user@example.com",
    "urn:ietf:params:jmap:submission": "user@example.com"
  },
  "username": "user@example.com",
  "apiUrl": "/api/jmap/",
  "downloadUrl": "/download/{accountId}/{blobId}/{name}?type={type}",
  "uploadUrl": "/upload/{accountId}",
  "eventSourceUrl": "/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
  "state": "session-0"
}
```

The size limits above come from `MAX_FILESIZE` (10 MiB by default). The
remaining limits describe the current server: binary upload is not
implemented (`maxConcurrentUpload: 0`), mailboxes are fixed and flat, only
`receivedAt` sorting is supported, delayed send is not supported,
and no RFC 4790 collation algorithms are advertised. VacationResponse is
omitted because its methods are not implemented.

## `POST /api/jmap/` (JMAP Batch Request)

Processes an ordered batch of JMAP method calls. The request and
response bodies are JSON `Request` and `Response` objects. Method
failures are returned inside `methodResponses` as `error`
invocations; only request-level failures use an HTTP error status.
A body larger than `MAX_FILESIZE` is rejected with HTTP 413 and a
problem details body (`urn:ietf:params:jmap:error:limit`,
`limit: "maxSizeRequest"`).

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
      "#ids": { "resultOf": "query1", "name": "Email/query", "path": "/ids/*" },
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
        "users/user@example.com/inbox/20240308T143000_01000192a3b4c5d6.eml",
        "users/user@example.com/inbox/20240308T091200_01000192abcdef.eml"
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

### Result references

An argument name prefixed with `#` (for example `#ids`) is an RFC 8620 result
reference, not a literal argument. Its value is an object with `resultOf`
(the method call id of an earlier call in the batch), `name` (the expected
response name of that call), and `path` (a JSON Pointer into that response's
arguments). Before invoking the method, the dispatcher replaces the argument
with the name without `#` and the value at that path, so the example above
calls `Email/get` with an `ids` argument taken from the earlier `Email/query`
response without an extra round trip.

`path` follows RFC 6901, with one addition: while the current value is an
array, the token `*` maps the remaining tokens over every element and flattens
array results (`/ids/*` therefore expands the `ids` array into individual
values).

A reference fails if `resultOf` is not an earlier method call id in the same
batch, the response name does not match `name`, or `path` does not resolve.
The whole method call is then rejected with an `invalidResultReference`
error. Supplying both `ids` and `#ids` is an `invalidArguments` error. Result
references are resolved before creation references, so a `#create1` value
inside a referenced result is still replaced with the server-assigned id.

### Creation-id references

A `/set` method call may assign a client-side creation id to each object it
creates. Later calls in the same batch can refer to the server-assigned id
with `#<creationId>` (for example `EmailSubmission/set` with
`"emailId": "#create1"` after an `Email/set` created `create1`). The
dispatcher resolves these references in argument values and map keys before
invoking each method. Unresolved references are passed through unchanged and
are reported as `notFound` by the method handler.

When the request includes a `createdIds` property (it may be empty), the
response includes a `createdIds` map of every creation id used in the batch
to its server-assigned id. Requests that omit `createdIds` do not receive
the property back, though creation references still resolve within those
requests. A client may send that map back as the request's `createdIds` to
continue a batch after a dropped connection. For authenticated requests with
a JWT `sid`, the map is also persisted under
`_storage/<sid>/created-ids.json`, so a later Lambda invocation can resolve
earlier references without the client echoing the map.

Email IDs are the full delivered object keys, including the received
timestamp (ISO 8601 basic UTC, e.g. `20240308T143000`) and `.eml` suffix.
They contain `/`, so clients must URL-encode
them when placing an ID in a route. The `mailboxIds` returned by
`Email/get` reflect the mailbox the message is actually stored in.

## Binary Upload/Download

- `POST /upload/{accountId}` — Upload binary data (e.g., attachments). Returns `{ "accountId": "...", "blobId": "...", "type": "application/pdf", "size": 12345 }`
- `GET /download/{accountId}/{blobId}/{name}?type=...` — Download binary data

## EventSource Push

- `GET /eventsource?types=*&ping=30` — Long-running Server-Sent Events connection for push notifications.