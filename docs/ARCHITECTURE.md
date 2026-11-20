# Architecture

## System overview

Deployed on AWS, Emissary looks like this:

```
                        ┌──────────────────────── Route 53 ─────────────────────────┐
                        │  MX  <DOMAIN>  → inbound-smtp.<region>.amazonaws.com      │
                        │  MX  mail.<DOMAIN> → feedback-smtp.<region>.amazonaws.com│
                        │  DKIM CNAMEs, SPF TXT, DMARC TXT                         │
                        └──────────────────────────────┬──────────────────────────┘
                                                       │
        inbound mail                                   ▼
  ─────────────────► SES receipt rule (active rule set, S3 action)
                                                       │  raw EML, key = SES message id
                                                       ▼
                                              ┌────────────────┐
                                              │  S3 bucket     │
                                              │  (in stack)    │
                                              └───────┬────────┘
                                                      │ GetObject / ListObjectsV2
 https://mail.<DOMAIN>                                ▼
  ─────────────────► API Gateway (regional, proxy) ──► Lambda (Go, custom runtime)
        ▲                                              │  also: DynamoDB metadata table
        │ A alias record                               │  Secrets Manager (JWT secret)
        │ + ACM cert                                  ▼
        │                                       single http.Handler
        │                                       ┌─────────────────────────────┐
   browser ── React SPA ──────────────────────►│  /api/*  JSON API           │
                                               │  /*      embedded frontend  │
                                               └─────────────────────────────┘
        outbound mail ◄──────────────────── SES SendEmail ◄──── EmailSubmission/set
```

The same `http.Handler` runs without AWS when started natively: it
listens with `net/http` and uses the memory/log implementations.

## Application startup

`mailer/cmd/frontend/main.go`:

1. `config.Load` reads all environment variables (see
   [CONFIGURATION.md](CONFIGURATION.md)), applies defaults, and
   validates. Any variable whose value starts with `arn:aws:` is
   resolved from AWS Secrets Manager at this point (`envutil` +
   `awsutil`) — that is how the deployed Lambda receives its JWT
   secret as an ARN.
2. `handler.New` builds the pluggable components:
   - a `usermanager.UserManager` (the built-in administrator plus the
     selected backend),
   - an `auth.Authenticator` (HS256 JWT) over that manager,
   - a `storage.Storage` (S3 or memory),
   - a `metadata.MetadataStore` (memory, file or DynamoDB),
   - a `sender.Sender` (SES or log).
3. The routes, auth middleware and embedded frontend are wired into
   one `http.Handler`.
4. Runtime mode is detected with `awsutil.IsAwsLambda()`:
   - **In Lambda**, the handler is handed to the Lambda runtime via
     `aws-lambda-go-api-proxy`, so API Gateway events are translated
     to `net/http` requests. The binary is built as `bootstrap`
     (custom runtime `provided.al2023`).
   - **Otherwise**, the process listens on `LISTEN_ADDR`/`PORT`.

## Package map

| Package | Responsibility |
| --- | --- |
| `cmd/frontend` | Entry point; chooses Lambda vs. native listener. |
| `config` | Loads and validates environment configuration. |
| `envutil` | Cached environment lookups; resolves `arn:aws:` values via Secrets Manager. |
| `awsutil` | Lambda detection and Secrets Manager access. |
| `auth` | JWT issue/validate (`Claims`: `sub`, `sid`, `systemManager`, `EmissaryUserManagement`, `iat`, `exp`, optional `aud`/`iss`). |
| `usermanager` | Pluggable login backends; every backend also knows the built-in admin. |
| `storage` | Mail source: `ListEmails` (scoped by JMAP `accountId`/`mailboxId`), plus `GetEmail`, `AddEmail`, `DeleteEmail`, and support-only bucket operations. |
| `sender` | Write side of mail: `Send`. |
| `metadata` | Per-message JMAP keywords (`$seen`, `$flagged`, `$draft`, and the other supported system keywords) plus the server-owned `receivedAt` timestamp, keyed by email id and discarded when a message is deleted. |
| `email` | RFC 5322 parsing: `WireFormatEmail` (full structure) and `UserEmail` (simplified). |
| `handler` | The HTTP API, auth middleware, panic recovery, and the embedded frontend. |
| `gui` | `//go:embed all:frontend/dist` — the built SPA. |

## Pluggable components

Each seam is a small interface, selected by environment variable and
instantiated in `handler.New`:

| Seam | Interface | Selector | Implementations | Notes |
| --- | --- | --- | --- | --- |
| Inbound messages | `storage.Storage` | `STORAGE` | `s3`, `memory` | `s3` needs `S3_BUCKET`. Defaults to `memory`. |
| Outbound mail | `sender.Sender` | `SENDER` | `aws-ses`, `mta`, `log` | Defaults to `log`. The From address comes from the submitting user (or masquerade target), not from configuration. |
| Message state | `metadata.MetadataStore` | `METADATA` | `memory` (default), `file`, `ddb` | Stores JMAP keywords and the server-owned `receivedAt` timestamp, keyed by email id and owning user id. `ddb` uses `DDB_TABLE` or the `metadata` table derived from `TABLE_PREFIX`; its table has partition key `id`, sort key `userId`, and a `userId-index` GSI for `List`. Both `handler.New` and the `s3-receiver` build this store with `metadata.NewStore`. |
| Users | `usermanager.Manager` | `USER_MANAGER` | `none`, `ddb`, `s3`, `ldap` | Auto-selects `s3` when storage is S3, else `none`. `ddb` uses `USER_TABLE` or the `users` table derived from `TABLE_PREFIX`; `s3` stores each user as `users/<email>/user.json` in the mail bucket. Both are Emissary-mastered. `ldap` is unimplemented. |

## Inbound mail flow

1. A sender's MTA resolves the MX record for `DOMAIN` and delivers to
   the SES inbound endpoint for the region.
2. SES evaluates the active receipt rule set (created and activated by
   the CDK "in" stack; activation is account-wide per region).
3. The rule's S3 action writes the raw message to a **staging area** at the
   bucket root, keyed by the SES message id. The bucket policy
   grants SES `PutObject`; no consumer Lambda is involved for this initial write.
4. A consumer process (Lambda or scheduled job) reads from the staging area,
   validates recipients, and delivers each message to the appropriate user mailboxes:
   - For each valid recipient, the receiver calls
     `storage.Storage.AddEmail(ctx, recipient, parsedEmail, raw)` on the **configured**
     store (S3 in production, memory for local runs), passing the original
     staging bytes as `raw`. Delivery no longer writes an S3 key directly
     from the receiver.
   - `AddEmail` assigns the mailbox and generates the email id. `S3Storage`
     writes `users/<recipient-email>/inbox/<timestamp>_<generated-id>.eml`,
     deriving the timestamp from the `Date` header (falling back to the current
     time) as ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`, e.g. `20240308T143000`).
     Legacy deliveries used the SES message id in place of `<generated-id>`;
     both remain readable. `AddEmail` stores the supplied `raw` bytes verbatim,
     so the delivered `.eml` keeps every header, MIME part and transfer
     encoding (including attachments). JMAP-created mail has no wire bytes and
     is rendered from the parsed fields instead.
   - The receiver creates a metadata record in the metadata store keyed by the
     **id returned from `AddEmail`** (the full S3 object key for `S3Storage`).
     The record starts with an empty keyword set and a server-stamped
     `receivedAt` (RFC 3339 UTC): the message is unread and unflagged until a
     JMAP client sets `keywords/$seen` or `keywords/$flagged`. `receivedAt`
     is not client-assignable or editable.
   - The staging object is deleted after successful delivery to all recipients.
     Staging read/delete still use S3 directly and will be decoupled separately.
5. When a JMAP call lists or opens mail, `storage.S3Storage` paginates
   `ListObjectsV2` over the prefix for the call's `accountId`:
   `users/<accountId>/<mailboxId>/`. The account comes from the JWT
   `sub`, or from the target account supplied by a user with
   `EmissaryUserManagement` (masquerade). An `inMailbox` filter narrows the read to one JMAP
   mailbox; without it every mailbox in the account is searched.
   `GetObject`s each message, and parses it with `email.Parse`.
   Unparseable objects are logged and skipped, not fatal.
6. `handler` merges the full keyword map and `receivedAt` from the metadata
   store into each JMAP `Email/get` response, and sorts `Email/query` by that
   receive time. A missing metadata record is not an error: the email is
   returned with an empty `keywords` object and `receivedAt` falls back to the
   message `Date` header. Listing is always scoped to one account; there is no
   cross-account email listing API.

The email ID used throughout the JMAP API is the full delivered S3 object key
(e.g., `users/alice@example.com/inbox/20240308T143000_9f1c...eml`), including
the timestamp and `.eml` suffix. It encodes the account, JMAP mailbox, received
time and the storage-generated message id. Older deliveries that used the SES
message id as the suffix remain valid ids.

Durable per-session scratch state lives separately at `_storage/<sid>/`. JMAP
drafts are JSON objects under `_storage/<sid>/drafts/`, and the creation-id map
is `_storage/<sid>/created-ids.json`. `<sid>` is the server-generated JWT
`sid`, so drafts and creation references survive across Lambda invocations and
never appear in mailbox listings.

### Recipient validation and delivery

When an inbound message is processed for delivery (by a consumer
Lambda or scheduled job reading from the S3 bucket staging area), the following
validation and delivery process applies:

1. **Extract recipients** — Parse the staged object with
   `email.ParseWireFormat` (the raw wire-format type, unlike the simplified
   `UserEmail` used for delivered `.eml` files) and collect the SMTP envelope
   recipients from the `for <address>` clauses of the topmost `Received`
   header. SES writes one for each `RCPT TO` when it stores the message.
   `To`, `Cc`, and `Bcc` are sender-controlled and are never used for routing;
   messages without a usable envelope recipient are skipped.

2. **Resolve routing rules** — For each recipient, load the ordered
   `HandlerRules` for its mail identity (the hostname after `@`) from the
   receiver config store, falling back to the `_` entry. Rules are tried in
   order until one applies:

   - `DeliverToUser` applies only when `UserManager.Lookup(ctx, address)`
     returns a known user (including the built-in administrator).
   - `DeliverToMailIdentity` delivers to the identity's mailbox without
     requiring a user account.
   - `Bounce` sends a standard bounce via `sender.Sender` with the original
     message ID, a `BounceSender` address (e.g. `mailer-daemon@<DOMAIN>`),
     the failed recipient and a `UserUnknown` bounce type, over the
     configured transport (SES or log).
   - `Drop` discards the message for that recipient: no delivery and no
     bounce. This is the only path that silently discards mail.

   When no rule applies, or no rules can be loaded, the receiver bounces
   instead of discarding the message.

3. **Deliver to selected recipients** — For each recipient whose rule
   selected delivery:
   - Call `Storage.AddEmail(ctx, recipient, parsedEmail, raw)` on the
     configured store, passing the original staging bytes. The store assigns
     the id, writes the bytes verbatim to the recipient's `inbox`, and the
     receiver does not construct mailbox keys.
   - For `S3Storage`, the key is `users/<recipient-email>/inbox/<timestamp>_<generated-id>.eml`,
     with the timestamp derived from the `Date` header (or the current time)
     in ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`, e.g. `20240308T143000`).
   - Initialize metadata with the id returned by `AddEmail`, an empty keyword
     set and a server-stamped `receivedAt`.
   - Each recipient gets their own copy in their `inbox` mailbox

4. **Mixed outcomes and retries** — Different recipients of one message can
   take different paths: delivery targets each get their own copy, bounced
   recipients each generate a separate bounce, and dropped recipients get
   neither. The staging object is deleted only after every recipient has been
   handled successfully. A failed delivery, bounce or delete leaves the object
   in place and returns an error so the Lambda invocation is retried.

   After each recipient is handled, the receiver records a marker derived from
   the staging key and that recipient under `_processed/` in the mail bucket.
   A retry skips recipients with a marker, so a partial failure only repeats
   the work that failed and a failure to delete staging cannot cause a second
   delivery. Markers are removed once staging has been deleted; a stale marker
   is harmless because SES message ids are unique.

Each delivery target has their own physical copy of the message in their
mailbox structure, enabling independent management (move, delete, flag) per
user. Delivery, bouncing and dropping are decided independently for every
recipient by the configured rules for its domain.

## Outbound mail flow

1. The client creates a draft with `Email/set` and submits it with
   `EmailSubmission/set`, both on the JMAP batch endpoint. With a JWT `sid`,
   `Email/set` writes the draft to `_storage/<sid>/drafts/` instead of trying
   to add a message to the delivered-mail tree (which `S3Storage` does not
   support). `EmailSubmission/set` reads that draft first and falls back to
   delivered mail.
2. The handler verifies the submitted email belongs to the request's
   `accountId`, then locks the From address to the authenticated user: the
   submission sends as the caller's own account, or as the target account
   when a caller with `EmissaryUserManagement` masquerades as another
   `accountId`. There is no configured From address, and a From supplied by
   the client (envelope sender or draft header) is ignored.
3. When masquerading, Reply-To must match From (it defaults to From when
   omitted); a conflicting Reply-To is rejected with `invalidProperties`.
4. The message is handed to the configured `sender.Sender` implementation.
   Both wire senders serialize the message once with a shared builder that
   writes the RFC 5322 headers (including `In-Reply-To` and `References` when
   the draft carries them, and the message's stored `Message-ID`) and the
   MIME body. `aws-ses` sends that raw message with the SES v2
   `SendRawEmail` API, with the envelope recipients passed separately;
   `mta` resolves the recipient domains' MX records and delivers the same
   bytes directly over SMTP on port 25, after a DMARC/SPF alignment warning
   check; `log` writes a line to the process log.
5. On success, the session draft is deleted; on failure it is kept so the
   client can retry without re-entering the message.

## Authentication and visibility

- `POST /api/auth/login` authenticates through the user manager: the
  built-in administrator (`USERNAME`/`PASSWORD`, constant-time compare)
  is checked first by every backend, then the backend's own logic.
- A successful login returns an HS256 JWT plus its expiry. The SPA
  stores it in `localStorage` and sends it as
  `Authorization: Bearer <token>`.
- `requireAuth` middleware validates the signature, expiry, audience
  and issuer, then stashes the claims in the request context.
- The JMAP dispatcher uses the JWT `sub` as the default `accountId` and
  rejects a different one for users without `EmissaryUserManagement`. A
  user with that claim may supply any `accountId` to masquerade.
- `Email/query`, `Email/get`, `Email/set` and `EmailSubmission/set` are
  scoped to that account. Storage uses a mailbox-key prefix rather than a
  header-based `canAccessEmail` filter; messages outside the account are
  absent from query results and reported as `notFound` by `Email/get`.
- Each delivered message has one object per recipient mailbox, so
  deleting a message affects only that account's copy.

## Request handling details

- All API routes live under `/api`; unknown `/api` paths return a JSON
  `404`, and every other path serves the SPA.
- The SPA server falls back to `index.html` for client-side routes and
  sets long-lived immutable caching only for `/assets/`.
- Request bodies are capped at 1 MiB.
- A top-level `recover` turns panics into a JSON `500`.
- Errors sent to clients are generic; the underlying cause is logged
  server-side only.

## Email data model

- `email.WireFormatEmail` — lossless, lenient RFC 5322 parser
  (headers, MIME tree, transfer encodings, charsets, folded headers).
  Parse errors are collected, not fatal. It can serialize back to wire
  format. MIME parts are capped at 10 MiB.
- `email.EmailRef` — metadata about where a message lives: its storage id
  plus the derived `AccountID`/`MailboxID`.
- `email.UserEmail` — the simplified message content used everywhere else:
  addresses, subject, date, text and HTML bodies.
- `email.ListedEmail` — a storage listing result pairing an `EmailRef` with
  its `UserEmail`.
- `email.Parse` is the convenience path used by storage; attachments
  and other non-text parts are ignored. Use `email.EmailRefFromID` to build
  the ref for a stored id.

## Frontend

- React 19 + Vite + TypeScript SPA (React Router): `/login`,
  `/` (inbox of the current JMAP account), `/email/:id`, `/compose`,
  `/compose/:id` (reply).
- The API client (`frontend/src/api.ts`) keeps the JWT in
  `localStorage` under `emissary_token`, attaches the Bearer header,
  and redirects to `/login` on a `401`. JMAP calls use the masquerade
  target from `localStorage['masquerade_as']` as their `accountId` when
  a user with `EmissaryUserManagement` has selected one.
- Vite builds into `mailer/gui/frontend/dist`, which `mailer/gui`
  embeds; the Go binary serves the SPA from the same origin and port
  as the API, so there are no CORS concerns.
- Delivered email ids are full object keys and contain `/`, so the SPA
  URL-encodes them when building `/email/:id` links.

## Generated interface stubs

Both the backend and the frontend use interface stubs generated from
the OpenAPI specifications (`api/auth.yaml`, `api/users.yaml`,
`api/jmap.yaml`). This ensures the wire contract is the single source
of truth and eliminates drift between server and client types.

- **Backend (Go)**: `run.sh generate_api` invokes `go tool ogen` to
  produce Go types, request/response decoders, validators, router
  middleware, and server/client interface stubs under `api/*/generated/`.
  The handler implements the generated server interfaces.
- **Frontend (TypeScript)**: The OpenAPI specs are used to generate
  TypeScript types and a typed API client (via `openapi-typescript` /
  `openapi-fetch` or equivalent). The generated client replaces
  hand-written fetch wrappers and the manual `types.ts` definitions,
  so the frontend consumes the exact same request/response shapes the
  backend produces.

## Deployment shape

Three CDK stacks (see [DEPLOYMENT.md](DEPLOYMENT.md)):

1. **id** — SES domain identity for `DOMAIN` with MAIL FROM
   `mail.<DOMAIN>`, plus the DKIM, MX, SPF and DMARC records.
2. **in** — inbound MX record, the raw-message S3 bucket, the receipt
   rule set (recipients from `SUBDOMAINS`), rule-set activation, and an
   SSM parameter publishing the bucket name.
3. **client** — the Lambda (from `mailer/cmd/frontend/bootstrap`), API
   Gateway with a TLS certificate and `mail.<DOMAIN>` alias record,
   the DynamoDB metadata table, and the JWT secret in Secrets Manager.
   This stack is skipped when `bootstrap` has not been built.
