# HTTP API

All endpoints are served by `cmd/frontend` on the same origin and port as the webmail client. The API lives under `/api`; unknown `/api` paths return `404`, and every other path serves the SPA. The wire contract is the OpenAPI documents in [`api/`](../api/).

## JMAP conformance claim

> Emissary implements **RFC 8620 (JMAP Core) and RFC 8621 (JMAP Mail)** for the capabilities `urn:ietf:params:jmap:core`, `urn:ietf:params:jmap:mail`, `urn:ietf:params:jmap:submission` and `urn:ietf:params:jmap:vacationresponse`, within the advertised per-account limits. Push supports webhook subscriptions and EventSource in `closeafter=state` and `closeafter=no` modes (the latter through a dedicated streaming Function URL).

"100% compliant" means every applicable MUST in RFC 8620 §§1–8 and RFC 8621 §§1–10 is implemented and tested; every SHOULD is implemented or documented as a profile choice; every advertised method, argument, property and SetError is reachable; [`api/jmap.yaml`](../api/jmap.yaml), Session capabilities, runtime capabilities and generated schemas are checked for agreement; and the external conformance suite's required tests and end-to-end client use cases pass. The suite runs locally and in CI and the release workflow; see [TESTING.md](TESTING.md#external-jmap-conformance-suite).

## Generated API artifacts

The OpenAPI specs are the single source of truth for HTTP paths, media types and schemas. `./run.sh generate:api` regenerates the checked-in artifacts; `./run.sh check:generated` fails when they are stale.

- **Backend (Go):** `ogen` generates types, decoders, validators and router middleware under `api/*/generated/`. The handler implements the generated auth, user and support server interfaces. JMAP dispatch in [`mailer/jmap/`](../mailer/jmap/) uses wire types generated from [`api/jmap.yaml`](../api/jmap.yaml), but has no generated server stub.
- **Frontend (TypeScript):** `orval` generates typed fetch helpers under `frontend/src/api/generated/`. Login and refresh use the generated auth helpers; the JMAP client uses generated schema types with a small hand-written wrapper because operations are method calls batched at one endpoint rather than one endpoint per operation.
- **Drift checks:** [`api/spec_test.go`](../api/spec_test.go) checks spec path ownership and spec↔runtime capability/method agreement. Generated code alone does not prove that a method behaves correctly.

See [DEVELOPMENT.md](DEVELOPMENT.md#api-interface-generation).

## Deployment constraints

The SPA and API share an origin; CORS is not enabled. Session URLs are absolute HTTPS URLs built from `JMAP_PUBLIC_URL`. Long-lived EventSource streams use the separate `eventSourceUrl` advertised in the Session (`JMAP_EVENTSOURCE_URL`, normally the dedicated streaming Function URL); clients must use the advertised URL rather than constructing their own.

## Specs and path ownership

Four OpenAPI documents define the wire contract, and their URL prefixes are exclusive:

| Spec | Prefix | Contents |
| --- | --- | --- |
| [`api/auth.yaml`](../api/auth.yaml) | `/api/auth/` | Authentication. Every operation lives under this prefix. |
| [`api/users.yaml`](../api/users.yaml) | `/api/user/` | User management; requires `EmissaryUserManagement` and an Emissary-mastered user manager. |
| [`api/jmap.yaml`](../api/jmap.yaml) | — | Session, batched methods, binary upload/download. No paths under the other prefixes. |
| [`api/support.yaml`](../api/support.yaml) | `/api/support/` | System Manager tools; requires `systemManager` ([SUPPORT.md](SUPPORT.md)). |

A document never defines operations outside its prefix, and no other document may define a path under it. [`api/spec_test.go`](../api/spec_test.go) (run by `./run.sh test`) enforces the rule: every `auth.yaml` path starts with `/api/auth/`, every `users.yaml` path with `/api/user/`, every `support.yaml` path with `/api/support/`, no `jmap.yaml` path falls under any of them, and the four documents never share a (method, path) operation.

`GET /api/user/autocomplete` is the one `/api/user/` operation not generated from `users.yaml`: it is served directly by the configured `UserManager` ([USERS.md](USERS.md#api-endpoint-autocomplete)).

## Conventions

- Requests and responses are JSON. REST bodies are capped at 1 MiB; JMAP batches are capped by `MAX_FILESIZE` (10 MiB by default, see [CONFIGURATION.md](CONFIGURATION.md#server-and-authentication)).
- Authenticated endpoints require `Authorization: Bearer <token>` from `POST /api/auth/login`; missing/invalid tokens yield `401` with `WWW-Authenticate: Bearer realm="emissary"`.
- Errors are `{"error": "<message>"}`; messages are generic, with details logged server-side. A rate-limited login returns `429` with `Retry-After` ([AUTH.md](AUTH.md)).
- A message outside the selected account is not discoverable: it is omitted from `Email/query` and reported in `Email/get`'s `notFound` list, just like a message that does not exist.

## Endpoints

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/auth/login` | — | Exchange credentials for a token pair. |
| `POST` | `/api/auth/refresh` | — (token in body) | Rotate the token pair in the same session. |
| `POST` | `/api/auth/logout` | Bearer | Revoke the session and its refresh tokens. |
| `GET` / `POST` | `/api/user/` | Bearer\* | List / create users. |
| `GET` / `PATCH` / `DELETE` | `/api/user/{email}` | Bearer\* | Get / update / delete a user (not yourself). |
| `GET` | `/api/user/password-change-supported` | Bearer | Whether the backend supports password changes. |
| `POST` | `/api/user/change-password` | Bearer | Change the authenticated user's password. |
| `POST` | `/api/user/{email}/password` | Bearer† | Change a user's password. |
| `GET` | `/api/user/autocomplete` | Bearer | Search user IDs for masquerade. |
| `GET` | `/api/support/configurations` | Bearer‡ | List configuration sections. |
| `GET` | `/api/support/configurations/{name}` | Bearer‡ | Settings for one section (secrets redacted). |
| `GET` | `/api/support/bucket/root` | Bearer‡ | List unprocessed staging objects. |
| `POST` | `/api/support/process-email` | Bearer‡ | Run the inbound pipeline on a staging object. |
| `GET` / `PUT` | `/api/support/receiver/rules` | Bearer‡ | List / replace receiver rules. |
| `DELETE` | `/api/support/receiver/rules/{identity}` | Bearer‡ | Remove an identity's rules (not `_`). |
| `GET` | `/api/support/receiver/identities` | Bearer‡ | Verified SES identities and rule coverage. |
| `GET` | `/.well-known/jmap` | Bearer | JMAP Session object. |
| `GET` | `/eventsource` | Bearer or `token` query | REST-compatible stream (`closeafter=state`). |
| `GET` | `/eventsource/stream` | Bearer or `token` query | Dedicated streaming endpoint (`closeafter=no`). |
| `POST` | `/api/jmap/` | Bearer | JMAP batch endpoint. |
| `POST` | `/upload/{accountId}` | Bearer | Upload a blob. |
| `GET` | `/download/{accountId}/{blobId}/{name}` | Bearer | Download a blob. |

\* Requires `EmissaryUserManagement` and an Emissary-mastered user manager (`ddb`, `filesystem` or `s3`). A user cannot delete their own account (`403`). Management details are in [USERS.md](USERS.md).

† Any authenticated user may change their own password; administrators may change others'. The two password endpoints need no management claim, but return `403` when the backend cannot store a verifier. `/api/user/password-change-supported` returns `{"supported": true|false}` for every backend and is meant to be cached by clients; the Settings screen uses it to hide password UI when unsupported. `/api/user/change-password` verifies the current password and revokes all sessions for the account, so the user must sign in again. See [USERS.md](USERS.md#change-password).

‡ Requires `systemManager`; see [SUPPORT.md](SUPPORT.md).

## Auth endpoints

`POST /api/auth/login` takes `{"username": "...", "password": "..."}` and returns the token pair shown in [AUTH.md](AUTH.md#login); `POST /api/auth/refresh` takes `{"refreshToken": "..."}` and returns a fresh pair; `POST /api/auth/logout` takes no body and returns `{"success": true}`. Token design, lifetimes, validation and client guidance are authoritative in [AUTH.md](AUTH.md).

## `POST /api/auth/login`

See [AUTH.md](AUTH.md#login) for the request/response example, issued claims, failure statuses and rate limits.

## `POST /api/auth/refresh`

See [AUTH.md](AUTH.md#refresh-and-token-stepping) for single-use refresh tokens, the eight-hour `nbf`, session ordering and error statuses.

## `POST /api/auth/logout`

See [AUTH.md](AUTH.md#logout) for session revocation and client responsibilities.

## `GET /api/user/autocomplete`

Searches user IDs for User Manager masquerade. Backed by `UserManager.Autocomplete`, not the generated management server; available to users with `EmissaryUserManagement`. Query parameter `q` is required and must be at least 3 characters. A `200` response is `{"users": ["user@example.com", "other@example.com"]}` with up to 10 matches; an empty list means no matches, not `404`. UI details are in [USERS.md](USERS.md#admin-masquerade).

## JMAP Mail Operations

All email operations use the batch endpoint `POST /api/jmap/`. Mail follows JMAP's **account → mailbox → email** hierarchy: `accountId` is an opaque RFC 8620 `Id`, and `filter.inMailbox` selects a mailbox such as `inbox`, `sent`, `drafts`, `trash`, `archive` or `junk`. The dispatcher resolves the public id to an internal user id; listing is scoped to that account's object-key prefix (`users/<internal-user-id>/<mailboxId>/`), so other users' mail is never read. Full method schemas are in [`api/jmap.yaml`](../api/jmap.yaml); dispatch lives in [`mailer/jmap/`](../mailer/jmap/).

| Capability | Implemented methods |
| --- | --- |
| `core` | `Core/echo`, `Blob/copy`, `PushSubscription/get`, `PushSubscription/set` |
| `mail` | `Mailbox/get`, `Mailbox/set`, `Mailbox/changes`, `Mailbox/query`, `Mailbox/queryChanges`, `Email/get`, `Email/set`, `Email/changes`, `Email/query`, `Email/queryChanges`, `Email/copy`, `Email/import`, `Email/parse`, `Thread/get`, `Thread/changes`, `SearchSnippet/get` |
| `submission` | `Identity/get`, `Identity/set`, `Identity/changes`, `EmailSubmission/get`, `EmailSubmission/set`, `EmailSubmission/changes`, `EmailSubmission/query`, `EmailSubmission/queryChanges` |
| `vacationresponse` | `VacationResponse/get`, `VacationResponse/set` |

`PushSubscription` methods are account-less and capability-independent (see [PushSubscription](#pushsubscription)); `EmailDelivery` state is reported in change events even though no `EmailDelivery/get` method exists.

### Account selection and masquerade

The default `accountId` is `acct_` + base64url(SHA-256(lowercased JWT `sub`)); the `sub` is the email address and is never advertised as an account id. A caller without User Manager permission cannot select another account. With `EmissaryUserManagement`, any known opaque account id may be used for `Email/query`, `Email/get`, `Email/set` and `EmailSubmission/set`; older clients may send their own email address as an alias, but the server never returns it. Activation and UI behavior are in [USERS.md](USERS.md#admin-masquerade).

### List Emails

`Email/query` filters by `inMailbox` and usually sorts by `receivedAt` descending. The returned ids are passed to `Email/get` to fetch full objects or referenced with `#ids` so both calls run in one batch (see [Result references](#result-references)). Use `position`/`limit` for paging and the returned `queryState` with `Email/queryChanges` to poll; a query state is scoped to the filter and sort that produced it.

```json
{
  "method": "Email/query",
  "args": {
    "accountId": "acct_<opaque>",
    "filter": { "inMailbox": "inbox" },
    "sort": [{ "property": "receivedAt", "isAscending": false }],
    "collapseThreads": false,
    "position": 0,
    "limit": 50
  }
}
```

### Get a Single Email

`Email/get` with a specific id returns the full object; use `properties` to narrow the response (see [Property selection on `/get` methods](#property-selection-on-get-methods)). Ids that do not resolve are listed in `notFound` rather than producing a method error, matching the behavior for messages outside the selected account.

### Delete Email

`Email/set` with `destroy: ["<id>"]` removes the delivered message and its metadata record. Destroying a message does not delete blobs it references: blobs have their own reference-counted lifecycle described in [Binary Upload/Download](#binary-uploaddownload).

### Received time

`Email/receivedAt` is server-owned, stored in the per-message metadata record as RFC 3339 UTC. It is stamped when the record is created — at inbound delivery or `Email/set.create` — and is never assignable, editable or updatable by clients; a `receivedAt` property in a create or update patch is ignored. `Email/query` sorts by it; messages with no stored value (delivered before the field existed) fall back to the RFC 5322 `Date` header, which is also reported separately as `sentAt`. Clients needing chronological order should sort by `receivedAt` rather than the `Date` header.

### Stored mailbox and subject

The per-message metadata record also holds the `mailbox` id currently holding the message and its decoded `subject`. Both are captured when the record is created (inbound delivery, `Email/set.create`, import/copy or the Sent copy) and the mailbox is rewritten whenever an `Email/set` update moves the message with `mailboxIds`. These are stored copies: storage remains authoritative for placement and the parsed message for the subject, so a legacy record without them is backfilled on the next update. See [CONFIGURATION.md](CONFIGURATION.md#metadata-jmap-keywords-received-time-mailbox-and-subject) for backend details.

### Keywords (message state)

JMAP models message state as a `String[Boolean]` map of **keywords** (RFC 8621 §4.1.1). They are stored in the metadata service alongside `receivedAt` and stitched into every `Email/get` response; only keywords that are set are returned, with value `true`. Supported system keywords:

| Keyword | Meaning |
| --- | --- |
| `$draft` | Message is a draft. |
| `$seen` | Message has been read; used for unread counts (drafts excluded). |
| `$flagged` | Message is flagged/starred. |
| `$answered` | Message has been replied to. |
| `$forwarded` | Message has been forwarded. |
| `$junk` | Message is junk/spam. |
| `$notjunk` | Message has been marked as not junk. |
| `$phishing` | Message has been marked as phishing. |

Names are case-insensitive on input and normalized to lowercase on storage and output. `/set.update` is a patch: `true` adds a keyword, `null` removes it, and `false` is invalid (`invalidProperties`). A whole `keywords` object may replace the entire set; `false` entries in that object are dropped.

**Mark read / unread**

```json
{ "method": "Email/set", "args": { "update": { "<id>": { "keywords/$seen": true } } } }
```

To mark unread, send `null` (not `false`) for the keyword:

```json
{ "method": "Email/set", "args": { "update": { "<id>": { "keywords/$seen": null } } } }
```

**Flag / unflag** uses `keywords/$flagged` in the same way; other keywords (for example `keywords/$answered`) follow the same pattern. Unknown properties (for example `mailboxIds`) are ignored on update rather than failing it; updates are scoped to the requested `accountId`, so a crafted id cannot modify another user's mail.

New deliveries created by the receiver start with no keywords (unread and unflagged) and a freshly stamped `receivedAt`. If a message has no metadata record at all — for example one delivered before the metadata service existed — `Email/get` still succeeds, returns an empty `keywords` object, and falls back to the message's `Date` for `receivedAt`. See [ARCHITECTURE.md](ARCHITECTURE.md#inbound-mail-flow) for the metadata creation point.

### View/Download raw MIME

Raw MIME is fetched through JMAP, not a bespoke endpoint:

1. `Email/get` with `properties: ["id", "blobId"]` returns the opaque, URL-safe `blobId` of the complete RFC 5322 message.
2. Substitute `accountId`, `blobId`, `name` and `type=message/rfc822` into the Session's `downloadUrl` to receive the exact stored bytes with a `message/rfc822` content type.

```json
{
  "method": "Email/get",
  "args": {
    "accountId": "acct_<opaque>",
    "ids": ["<id>"],
    "properties": ["id", "blobId"]
  }
}
```

The `blobId` is an RFC 8620 `Id` (URL-safe), so it can be interpolated into the download path directly. The account is the one returned by `Email/get`. Missing emails or blobs return `404`, unauthorized accounts `403`. Normal users can only resolve messages in their own account; `EmissaryUserManagement` callers may masquerade. The web UI uses these two steps for both the **View MIME** modal and the **Download** button.

### Send Email

`Email/set.create` stores the email in the account's `drafts` mailbox; `EmailSubmission/set.create` submits it, accepting that email id as `emailId`. A creation-id reference lets both calls share one batch (see [Creation-id references](#creation-id-references)). On success the server files a copy of the message in the account's `sent` mailbox with a new email id.

**Locked From address.** The RFC 5322 `From` header comes from the selected Identity (the submission's `identityId`), not from the client-supplied draft header. By default that is the caller's own account; a caller with `EmissaryUserManagement` acting on another account (masquerade) is limited to an Identity owned by that account. An Identity address that is not a valid email address (no `@`) is rejected with `invalidProperties`.

**Envelope MAIL FROM.** RFC 8621 §7 requires an `envelope.mailFrom`; senders must supply it. An empty address (`{"email": ""}`) is accepted as the null reverse path. A non-empty `mailFrom` must match the selected Identity's email address; a mismatch is rejected with the `forbiddenMailFrom` SetError.

**Recipients.** `to`, `cc` and `bcc` each accept any number of addresses. `envelope.rcptTo` is required and must cover every To, Cc and Bcc address (otherwise a `forbidden` submission error); Bcc addresses appear only in the delivery envelope, never in the message headers.

**Thread headers.** The draft's `inReplyTo` and `references` are carried into the delivered message as `In-Reply-To` and `References`. The message gets a `Message-ID` at `Email/set` create time; the sender reuses that id instead of generating a new one, so the stored and delivered copies agree. When masquerading, Reply-To must match From (defaulting to From); a conflicting Reply-To is rejected with `invalidProperties`.

**Draft cleanup.** After a successful `EmailSubmission/set`, the web UI uses `onSuccessDestroyEmail` to remove the source draft; `onSuccessUpdateEmail` is also supported for JMAP clients. Session scratch drafts (legacy, per-JWT `sid`) are deleted by the server regardless. Neither path affects the copy filed in `sent`.

### Reply and Forward (web UI)

The web UI's Reply and Forward flows build outgoing messages with these conventions; they are a client-side contract, not JMAP requirements (see [`html.ts`](../frontend/src/html.ts)):

- **Subject:** a reply is prefixed `RE: ` and a forward `FWD: `. If the original subject already begins with the same prefix (case-insensitive), no second prefix is added, so a reply to a forwarded message reads `RE: FWD: <subject>`.
- **Body (top posting):** the user's new text goes on top, the original text is quoted below, separated by a plain-text separator line (`-----`). The exact delivered text-body layout is:

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

That is, exactly six blank lines above the separator and one below it. HTML mode renders the same layout with line breaks, a separator element and the quoted text in a `<pre>` block; the plain-text alternative carries the exact layout above. The quoted part is the original's text body (an HTML-only original is quoted as its extracted text).

- **Thread headers:** both set `In-Reply-To` to the original `Message-ID`, and `References` to the original's `References` list with its `Message-ID` appended (duplicates removed, order preserved). These headers are not shown in the UI.
- **Recipients:** a reply goes to the original's Reply-To address, or From when there is no Reply-To; a forward starts with no recipients. Both support multiple To, Cc and Bcc addresses. From is locked as described above — when masquerading, the message is sent as the masquerade target.

## Support API

The `/api/support/` endpoints ([SUPPORT.md](SUPPORT.md#endpoints)) require the `systemManager` claim and cover configuration inspection, staging-object listing/processing, and the receiver rules editor. Their behavior is documented in [SUPPORT.md](SUPPORT.md); this page only lists their paths under [Endpoints](#endpoints).

## `GET /.well-known/jmap`

Returns the JMAP Session object with capabilities, accounts and absolute endpoint templates. Account identifiers are opaque `acct_` ids (`acct_` + base64url(SHA-256(email))); the email address appears only in `username` and the account's `name`. Illustrative shape (full schemas in [`api/jmap.yaml`](../api/jmap.yaml)):

```json
{
  "capabilities": {
    "urn:ietf:params:jmap:core": {
      "maxSizeUpload": 10485760,
      "maxConcurrentUpload": 4,
      "maxSizeRequest": 10485760,
      "maxConcurrentRequests": 4,
      "maxCallsInRequest": 16,
      "maxObjectsInGet": 500,
      "maxObjectsInSet": 500,
      "collationAlgorithms": ["i;ascii-casemap"]
    },
    "urn:ietf:params:jmap:mail": {},
    "urn:ietf:params:jmap:submission": {},
    "urn:ietf:params:jmap:vacationresponse": {}
  },
  "accounts": {
    "acct_<opaque>": {
      "name": "user@example.com",
      "isPersonal": true,
      "isReadOnly": false,
      "accountCapabilities": {
        "urn:ietf:params:jmap:core": {
          "maxSizeUpload": 10485760,
          "maxConcurrentUpload": 4,
          "maxSizeRequest": 10485760,
          "maxCallsInRequest": 16,
          "maxObjectsInGet": 500,
          "maxObjectsInSet": 500,
          "collationAlgorithms": ["i;ascii-casemap"]
        },
        "urn:ietf:params:jmap:mail": {
          "maxMailboxesPerEmail": 1,
          "maxMailboxDepth": 1,
          "maxSizeMailboxName": 255,
          "maxSizeAttachmentsPerEmail": 10485760,
          "emailQuerySortOptions": ["receivedAt"],
          "mayCreateTopLevelMailbox": true
        },
        "urn:ietf:params:jmap:submission": {
          "maxDelayedSend": 0,
          "submissionExtensions": {}
        },
        "urn:ietf:params:jmap:vacationresponse": {}
      }
    }
  },
  "primaryAccounts": {
    "urn:ietf:params:jmap:mail": "acct_<opaque>",
    "urn:ietf:params:jmap:submission": "acct_<opaque>",
    "urn:ietf:params:jmap:vacationresponse": "acct_<opaque>"
  },
  "username": "user@example.com",
  "apiUrl": "https://jmap.example.com/api/jmap/",
  "downloadUrl": "https://jmap.example.com/download/{accountId}/{blobId}/{name}?type={type}",
  "uploadUrl": "https://jmap.example.com/upload/{accountId}",
  "eventSourceUrl": "https://stream.example.com/eventsource/stream?types={types}&closeafter={closeafter}&ping={ping}",
  "state": "s-8Kf3pQ1vZx7LmT2nR9wYb4"
}
```

`state` is an opaque but stable fingerprint of the Session properties (including across Lambda cold starts), changing when a capability, limit, URL, account or username changes. Limits are advertised both server-wide and per account under `accountCapabilities`:

| Limit | Value | Meaning |
| --- | --- | --- |
| `maxSizeUpload`, `maxSizeRequest`, `maxSizeAttachmentsPerEmail` | 10485760 | 10 MiB, from `MAX_FILESIZE`. |
| `maxConcurrentUpload`, `maxConcurrentRequests` | 4 | Concurrent uploads and batches served at once. |
| `maxCallsInRequest` | 16 | Method calls allowed in one batch. |
| `maxObjectsInGet`, `maxObjectsInSet` | 500 | Objects a single `/get` or `/set` may address. |
| `maxMailboxDepth` | 1 | Only top-level mailboxes are supported. |
| `maxMailboxesPerEmail` | 1 | Each email belongs to one mailbox. |
| `emailQuerySortOptions` | `receivedAt` | The only supported sort property. |
| `maxDelayedSend` | 0 | Delayed send is not supported. |
| `collationAlgorithms` | `i;ascii-casemap` | The advertised RFC 4790 collation. |

`eventSourceUrl` points at the dedicated streaming endpoint (`JMAP_EVENTSOURCE_URL`); all other templates use `JMAP_PUBLIC_URL`. Basic auth is not advertised or accepted; the browser EventSource API may pass the token in a query parameter. VacationResponse settings are persisted with the account and automatic replies follow RFC 3834 loop-prevention rules ([USERS.md](USERS.md#vacation-response)).

## `POST /api/jmap/ (JMAP Batch Request)`

Processes an ordered batch of JMAP method calls. Method failures (including unknown method names) appear in `methodResponses` as `error` invocations with HTTP 200; only request-level failures use an HTTP error status and an `application/problem+json` RFC 7807 problem details body.

`Content-Type: application/json` is required. The entire request is rejected unless it is a single valid I-JSON `Request`: `using` must match the capabilities advertised by the Session, `methodCalls` must contain well-formed three-element Invocation tuples with unique `methodCallId` values, and `createdIds` (when present) must be an object whose keys and values are valid RFC 8620 `Id` strings (1–255 URL-safe base64url characters, without padding). Unknown properties are ignored for forward compatibility.

| Problem type | Status | Cause |
| --- | --- | --- |
| `urn:ietf:params:jmap:error:notJSON` | 400 | Wrong content type, malformed JSON, duplicate object members, trailing data, invalid UTF-8. |
| `urn:ietf:params:jmap:error:notRequest` | 400 | JSON parsed but did not match the `Request` type signature. |
| `urn:ietf:params:jmap:error:unknownCapability` | 400 | `using` contained a capability the server does not advertise. |
| `urn:ietf:params:jmap:error:limit` | 400 | `maxCallsInRequest`, `maxObjectsInGet` or `maxObjectsInSet` exceeded. |
| `urn:ietf:params:jmap:error:limit` | 413 | Body exceeded `maxSizeRequest` (`MAX_FILESIZE`); the `limit` is `maxSizeRequest`. |

Every limit problem includes a `limit` property naming the advertised capability value that was exceeded. Example request listing emails:

```json
{
  "using": ["urn:ietf:params:jmap:mail"],
  "methodCalls": [
    ["Email/query", {
      "accountId": "acct_<opaque>",
      "filter": { "inMailbox": "inbox" },
      "sort": [{ "property": "receivedAt", "isAscending": false }],
      "collapseThreads": false
    }, "query1"],
    ["Email/get", {
      "accountId": "acct_<opaque>",
      "#ids": { "resultOf": "query1", "name": "Email/query", "path": "/ids/*" },
      "properties": ["id", "subject", "receivedAt", "keywords"]
    }, "get1"]
  ]
}
```

Example response:

```json
{
  "methodResponses": [
    ["Email/query", {
      "accountId": "acct_<opaque>",
      "queryState": "state123",
      "ids": ["<id-1>", "<id-2>"],
      "total": 2
    }, "query1"],
    ["Email/get", {
      "accountId": "acct_<opaque>",
      "state": "state123",
      "list": [{ "id": "<id-1>", "subject": "Hello" }],
      "notFound": []
    }, "get1"]
  ],
  "sessionState": "state123"
}
```

Method-level failures use the same envelope: the failing call gets an `error` invocation with `type` and `description`, while other calls in the batch still run. Unknown method names are `unknownMethod`; a stale `ifInState` is `stateMismatch`:

```json
{
  "methodResponses": [
    ["Email/set", {
      "accountId": "acct_<opaque>",
      "oldState": "state123",
      "newState": "state123",
      "created": {},
      "updated": {},
      "destroyed": [],
      "notCreated": {},
      "notUpdated": { "<id>": { "type": "invalidProperties", "description": "false is not a valid keyword patch value" } },
      "notDestroyed": {}
    }, "set1"],
    ["error", { "type": "stateMismatch", "description": "ifInState does not match" }, "set2"]
  ],
  "sessionState": "state123"
}
```

### Property selection on `/get` methods

The implemented `/get` methods (`Mailbox/get`, `Thread/get`, `Email/get`, `Identity/get` and `PushSubscription/get`) accept the RFC 8620 `properties` argument. When omitted or `null`, every property is returned; when it is an array, only those properties are returned and `id` is always included (so `properties: []` returns just `id`). Unknown property names are rejected with a method-level `invalidArguments` error in `methodResponses`, not an HTTP error. State tokens are computed from the full objects, so they do not change with the requested selection.

`Email/get` also accepts the RFC 8621 body arguments:

| Argument | Effect |
| --- | --- |
| `bodyProperties` | Selects properties of each returned `EmailBodyPart`; `partId` is needed to resolve `bodyValues`. Unknown names are also `invalidArguments`. |
| `fetchAllBodyValues` | Includes body values for every available text part, overriding the narrower `fetchTextBodyValues` / `fetchHTMLBodyValues` flags. |
| `maxBodyValueBytes` | Truncates each `bodyValues` value to at most this many octets and sets `isTruncated: true`. Truncation never splits a UTF-8 codepoint and avoids ending inside an HTML tag. |

A request that fetches text bodies but caps each value looks like:

```json
{
  "method": "Email/get",
  "args": {
    "accountId": "acct_<opaque>",
    "ids": ["<id>"],
    "properties": ["id", "subject", "body", "bodyValues"],
    "bodyProperties": ["partId", "type", "subtype", "charset"],
    "fetchTextBodyValues": true,
    "maxBodyValueBytes": 4096
  }
}
```

Dynamic `header:` property names are accepted syntactically; this server does not retain raw headers, so no value is returned for them today.

### Blob/copy

`Blob/copy` copies immutable blobs between different accounts; the source and destination account ids must differ. The optional `ifFromInState` argument guards the source account's blob state using `stateMismatch`. Failures are reported per blob in `notCopied`. An unknown source account produces the method-level `fromAccountNotFound` error. Blob bytes are stored under `users/<account>/_blobs`, which is reserved and never exposed as a mailbox. RFC 8620 §6.3 does not define an `onSuccessDestroyOriginal` argument; Emissary therefore does not accept one (use `Email/copy` when the source email itself must be removed).

```json
{
  "method": "Blob/copy",
  "args": {
    "fromAccountId": "acct_<source>",
    "toAccountId": "acct_<dest>",
    "blobIds": ["<blob-id>"],
    "ifFromInState": "blob-2"
  }
}
```

### PushSubscription

`PushSubscription/get` and `PushSubscription/set` are account-less: they do not accept or return `accountId` or state tokens. Subscriptions are scoped to the authentication credentials that created them, are removed on logout, and are pruned at expiry. The private `url` and `keys` properties are never returned; explicitly requesting either from `/get` is rejected with `forbidden`, and attempting to update either is rejected with `invalidProperties`.

On creation the server immediately POSTs a `PushVerification` object to the registered URL with `Content-Type: application/json` and an RFC 8030 `TTL` header:

```json
{
  "@type": "PushVerification",
  "pushSubscriptionId": "sub-1",
  "verificationCode": "<random-code>"
}
```

The client confirms by updating the subscription with the delivered code:

```json
{ "update": { "sub-1": { "verificationCode": "<random-code>" } } }
```

No other push is made to the URL before that confirmation. Push URLs must be HTTPS and resolve to public addresses; private, loopback and link-local targets are rejected to prevent server-side request forgery, and redirects are revalidated. Subscriptions are capped per credential and subject to a creation rate limit. Once verified, the subscription receives `StateChange` webhook POSTs (same payload as the EventSource `state` event, with `TTL: 60`); unverified subscriptions never receive one. Delivery is best effort and is rate limited: an endpoint answering `429` puts the credential into a one-minute backoff and carries the missed type states into the next notification.

### Result references

An argument name prefixed with `#` (for example `#ids`) is an RFC 8620 result reference, not a literal argument. Its value is an object with `resultOf` (the method call id of an earlier call in the batch), `name` (the expected response name of that call), and `path` (a JSON Pointer into that response's arguments). Before invoking the method, the dispatcher replaces the argument with the name without `#` and the value at that path, so the example above calls `Email/get` with an `ids` argument taken from the earlier `Email/query` response without an extra round trip.

```json
{ "#ids": { "resultOf": "query1", "name": "Email/query", "path": "/ids/*" } }
{ "#emailIds": { "resultOf": "get1", "name": "Email/get", "path": "/list/*/id" } }
```

The first expands `Email/query`'s `ids` into individual values; the second collects the `id` of every object in `Email/get`'s `list`.

`path` follows RFC 6901, with one addition: while the current value is an array, the token `*` maps the remaining tokens over every element and flattens array results (`/ids/*` therefore expands the `ids` array into individual values).

A reference fails if `resultOf` is not an earlier method call id in the same batch, the response name does not match `name`, or `path` does not resolve. The whole method call is then rejected with an `invalidResultReference` error. Supplying both `ids` and `#ids` is an `invalidArguments` error. Result references are resolved before creation references, so a `#create1` value inside a referenced result is still replaced with the server-assigned id.

### Creation-id references

A `/set` method call may assign a client-side creation id to each object it creates. Later calls in the same batch can refer to the server-assigned id with `#<creationId>` (for example `EmailSubmission/set` with `"emailId": "#create1"` after an `Email/set` created `create1`). The dispatcher resolves these references in argument values and map keys before invoking each method. Unresolved references are passed through unchanged and are reported as `notFound` by the method handler:

```json
[
  ["Email/set", {
    "accountId": "acct_<opaque>",
    "create": { "create1": { "mailboxIds": { "drafts": true }, "subject": "Hello" } }
  }, "c1"],
  ["EmailSubmission/set", {
    "accountId": "acct_<opaque>",
    "create": { "sub1": {
      "emailId": "#create1",
      "identityId": "<identity-id>",
      "envelope": {
        "mailFrom": { "email": "user@example.com" },
        "rcptTo": [{ "email": "to@example.com" }]
      }
    } }
  }, "c2"]
]
```

When the request includes a `createdIds` property (it may be empty), the response includes a `createdIds` map of every creation id used in the batch to its server-assigned id, allowing a client to resume after a dropped connection. Requests that omit `createdIds` do not receive the property back, though creation references still resolve within those requests. With a JWT `sid`, the map is also stored at `_storage/<sid>/created-ids.json`, so a later Lambda invocation can resolve earlier references without the client echoing the map.

Email IDs are stable, URL-safe base64url strings. The physical delivered message filename and object key are internal storage details and are never returned as JMAP ids. Moving a message between mailboxes changes its physical storage location but keeps the same JMAP id. Legacy tokens and full object keys are still accepted on input so older stored messages remain addressable, and the `mailboxIds` returned by `Email/get` reflect the mailbox the message is currently stored in.

## Binary Upload/Download

Both endpoints require the same bearer token as the JMAP API. `accountId` is scoped to the authenticated user; only a caller with user-management privileges may address another account, and it must exist.

- `POST /upload/{accountId}` — the raw request body is uploaded as a blob. The request `Content-Type` is echoed as the blob's `type`. A successful response is `{"accountId": "...", "blobId": "...", "type": "application/pdf", "size": 12345}`. Bodies larger than the advertised `maxSizeUpload` are rejected with an RFC 7807 problem response, as are uploads that exceed the per-user unreferenced blob quota. No more than `maxConcurrentUpload` uploads are served at once.
- `GET /download/{accountId}/{blobId}/{name}?type=...` — streams the bytes for `blobId`. The optional `type` query parameter overrides the response `Content-Type` (the uploaded type is used when omitted), and `name` is returned as the `Content-Disposition` filename. Successful responses are marked `private, immutable` so they can be cached safely.

Blobs are immutable and content-addressed: re-uploading identical bytes returns the same `blobId` and resets its expiry. Metadata (uploader, upload time and reference count) lives beside each blob in `users/<account>/_blobs` for every storage backend. Unreferenced blobs have a separate per-uploader quota (the configured maximum upload size); when an upload would exceed it, the oldest unreferenced blobs for that uploader are deleted first. Otherwise an unreferenced blob survives for at least one hour. Referenced blobs are pinned: they are never evicted for quota or age, and `DeleteBlob` rejects any blob with a positive reference count. `Blob/copy` holds a temporary reference while it copies, so a blob cannot be destroyed inside the method call that still uses it. Unreferenced blobs are visible only to the user who uploaded them, even in a shared account.

## State changes

The per-type `state` tokens returned by `Foo/get` and the `queryState` tokens returned by `Foo/query` are persisted with the account's mail data, so they survive restarts and do not regress when concurrent Lambda invocations update the same account. A state token changes only when the underlying data (or, for query states, the ordered query result set) changes; query states are scoped to the filter and sort that produced them.

Clients detect changes by polling:

- `Foo/changes` with `sinceState` returns `created`, `updated`, `destroyed`, `newState` and `hasMoreChanges`.
- `Foo/queryChanges` with `sinceQueryState` returns `added` (with the new `index` of each item), `removed`, `newQueryState` and `canCalculateChanges`. When `canCalculateChanges` is `false`, refetch the query with `Foo/query`.

A `Foo/changes` response illustrates the shape:

```json
{
  "accountId": "acct_<opaque>",
  "oldState": "email-3",
  "newState": "email-4",
  "created": ["<id-3>"],
  "updated": ["<id-1>"],
  "destroyed": ["<id-2>"],
  "hasMoreChanges": false
}
```

If `maxChanges` was supplied and `hasMoreChanges` is `true`, call again with `newState` as the next `sinceState` until it is `false`.

### EventSource

The Session object advertises an `eventSourceUrl` URI template with the variables `types`, `closeafter` and `ping`. That template points at `GET /eventsource/stream`, served by a dedicated response-streaming Function URL with a 15-minute timeout. Both stream endpoints accept the same bearer token as the JMAP API or, for the browser `EventSource` API (which cannot set request headers), the token in a `token` query parameter. Query tokens are secrets and should not be logged or shared. WebSocket push is not implemented; webhooks and EventSource are the chosen push options.

- `GET /eventsource` is the REST-compatible endpoint behind API Gateway. It accepts/defaults `closeafter=state` and rejects `closeafter=no` with `400`: the REST integration has a 29-second hard timeout. The REST deployment returns `404` for `/eventsource/stream`, while local and test servers expose both paths.
- `GET /eventsource/stream` accepts `closeafter=state` or `closeafter=no`. The default is `no`; a stream with pings stays open beyond the REST Lambda's 30-second timeout and is terminated by the dedicated function's 15-minute limit or by the client.
- `types` is a comma-separated list of JMAP type names or `*` for every type Emissary tracks. Only the listed types are included in each event.
- `ping` is a positive number of seconds; `0` disables pings. Requests below 10 seconds are raised to 10 and requests above 300 seconds are lowered to 300. The default is 30 seconds.

Successful responses use `text/event-stream`, disable caching and flush each event. A `state` event's data is an RFC 8620 `StateChange` object:

```json
{
  "@type": "StateChange",
  "changed": {
    "acct_<opaque>": {
      "Email": "email-4",
      "EmailDelivery": "email-delivery-2"
    }
  }
}
```

Each `state` event carries an `id` encoding the *entire* visible type-state snapshot immediately after the change. On reconnect the client's automatic `Last-Event-ID` header is compared with the current durable state; missed type tokens are sent as a catch-up event before the stream continues. `ping` events are `{"interval": n}` and deliberately carry no `id`.

Changes are polled from the durable state store (about every two seconds), so events also cover writes made by another process. Multiple changes within the rate-limit window are coalesced into one event with the latest token for each affected type; if an event is dropped, the next `/get` or `/changes` still finds the data. Live streams are closed on logout or session revocation, and at most eight concurrent streams are accepted per credential.

`EmailDelivery` is advanced by the inbound pipeline after a message is stored and its metadata is saved. It is reported in `StateChange` events even though there is no `EmailDelivery/get` method, matching the RFC 8620 example.
