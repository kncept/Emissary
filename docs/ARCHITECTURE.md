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
        outbound mail ◄──────────────────── SES SendEmail ◄──── POST /api/emails/send
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
   - a `usermanager.Manager` (the built-in administrator plus the
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
| `cmd/sns-receiver` | Placeholder for a future SNS-to-S3 relay Lambda. |
| `config` | Loads and validates environment configuration. |
| `envutil` | Cached environment lookups; resolves `arn:aws:` values via Secrets Manager. |
| `awsutil` | Lambda detection and Secrets Manager access. |
| `auth` | JWT issue/validate (`Claims`: `sub`, `sid`, `admin`, `iat`, `exp`, optional `aud`/`iss`). |
| `usermanager` | Pluggable login backends; every backend also knows the built-in admin. |
| `storage` | Mail source: `ListAllEmails`, `GetEmail`, `DeleteEmail`. |
| `sender` | Write side of mail: `Send`. |
| `metadata` | Per-message read/flag state, discarded when a message is deleted. |
| `email` | RFC 5322 parsing: `WireFormatEmail` (full structure) and `UserEmail` (simplified). |
| `handler` | The HTTP API, auth middleware, panic recovery, and the embedded frontend. |
| `gui` | `//go:embed all:frontend/dist` — the built SPA. |
| `server` | An older HTTP server package with a different API shape; currently nothing imports it. It is kept for reference. |

## Pluggable components

Each seam is a small interface, selected by environment variable and
instantiated in `handler.New`:

| Seam | Interface | Selector | Implementations | Notes |
| --- | --- | --- | --- | --- |
| Inbound messages | `storage.Storage` | `STORAGE` | `s3`, `memory` | `s3` needs `S3_BUCKET`. Defaults to `memory`. |
| Outbound mail | `sender.Sender` | `SENDER` | `aws-ses`, `log` | Defaults to `aws-ses` when `SES_FROM` is set, else `log`. |
| Message state | `metadata.MetadataStore` | `METADATA` | `memory` (default), `file`, `ddb` | `ddb` uses `DDB_TABLE` or the `metadata` table derived from `TABLE_PREFIX`. |
| Users | `usermanager.Manager` | `USER_MANAGER` | `none` (default), `any`, `ddb`, `ldap` | `any` accepts any address in `DOMAIN` with any password. `ddb` uses `USER_TABLE` or the `users` table derived from `TABLE_PREFIX` and is Emissary-mastered. `ldap` is unimplemented. |

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
   - For each valid recipient, the message is copied to `users/<recipient-email>/inbox/<timestamp>_<ses-message-id>.eml`
   - The timestamp is derived from the message's `Date` header or `Received` headers (ISO 8601 UTC)
   - The staging object is deleted after successful delivery to all recipients
5. When a user lists or opens mail, `storage.S3Storage` paginates
   `ListObjectsV2` over their `users/<user-id>/<mailboxId>/` prefix,
   `GetObject`s each message, and parses it with `email.Parse`. Unparseable objects are logged and
   skipped, not fatal.
6. `handler` merges read/flag state from the metadata store into each
   response and filters the result by the caller's visibility.

The email ID used throughout the API is the full S3 object key (e.g., `users/alice@example.com/inbox/2026-11-17T10:30:45Z_01000192a3b4c5d6...eml`), which encodes the user, mailbox, received time, and SES message ID.

### Recipient validation and delivery

When an inbound message is processed for delivery (by a consumer
Lambda or scheduled job reading from the S3 bucket staging area), the following
validation and delivery process applies:

1. **Extract recipients** — Parse the message (`email.Parse`) and
   collect all envelope recipients from `To`, `Cc`, and `Bcc` headers.

2. **Validate each recipient** — For each recipient address, call
   `UserManager.Lookup(ctx, address)` using the configured user manager
   (see [Pluggable components](#pluggable-components)). This checks
   whether the address corresponds to a known user in the system
   (including the built-in administrator).

   - The `any` backend accepts any valid address at `DOMAIN`.
   - The `ddb` backend checks the DynamoDB users table.
   - The `none` backend accepts only the administrator.

3. **Handle invalid recipients** — If a recipient is not found
   (`ErrInvalidCredentials` / `ErrUserNotFound`), the message is
   bounced:
   - Construct a bounce message using the `sender.Sender` interface.
   - Call `sender.Send(ctx, bounceMessage)` where the bounce message
     includes the original message ID, a `BounceSender` address
     (e.g., `mailer-daemon@<DOMAIN>`), the failed recipient, and an
     appropriate bounce type (e.g., `UserUnknown`).
   - The bounce is sent via the configured outbound transport (SES or
     log).

4. **Deliver to valid recipients** — For each valid recipient:
   - Extract the received timestamp from the message headers
   - Copy the raw message to `users/<recipient-email>/inbox/<timestamp>_<ses-message-id>.eml`
   - Each recipient gets their own copy in their `inbox` mailbox

5. **Mixed validity** — If some recipients are valid and others are not:
   - Valid recipients each receive their own copy in their inbox.
   - Invalid recipients each generate a separate bounce.
   - The staging object is deleted after all deliveries and bounces complete.

This process ensures that mail is only delivered to known users while
providing proper SMTP bounce semantics for unknown addresses. Each user
has their own physical copy of the message in their mailbox structure,
enabling independent management (move, delete, flag) per user.

## Outbound mail flow

1. The client posts to `POST /api/emails/send`.
2. The handler resolves the From address: administrators may specify
   one (defaulting, on a reply, to the original recipient); ordinary
   users always send as their own address; otherwise the sender's
   configured `From` (`SES_FROM`) is used.
3. The message is handed to the sender — in production the SES v2
   `SendEmail` API, in development a log line.

## Authentication and visibility

- `POST /api/auth/login` authenticates through the user manager: the
  built-in administrator (`USERNAME`/`PASSWORD`, constant-time compare)
  is checked first by every backend, then the backend's own logic.
- A successful login returns an HS256 JWT plus its expiry. The SPA
  stores it in `localStorage` and sends it as
  `Authorization: Bearer <token>`.
- `requireAuth` middleware validates the signature, expiry, audience
  and issuer, then stashes the claims in the request context.
- Listing/opening/deleting applies `canAccessEmail`: admins see everything
  (optionally filtered by `?user=`), users see only their own
  conversations, and anything else is a `404`. Deleting a message removes
  the single underlying object, so it takes effect for every user who
  could see it.

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
- `email.UserEmail` — the simplified view used everywhere else: id,
  addresses, subject, date, text and HTML bodies.
- `email.Parse` is the convenience path used by storage; attachments
  and other non-text parts are ignored.

## Frontend

- React 19 + Vite + TypeScript SPA (React Router): `/login`,
  `/` (inbox with an admin user filter), `/email/:id`, `/compose`,
  `/compose/:id` (reply).
- The API client (`frontend/src/api.ts`) keeps the JWT in
  `localStorage` under `emissary_token`, attaches the Bearer header,
  and redirects to `/login` on a `401`.
- Vite builds into `mailer/gui/frontend/dist`, which `mailer/gui`
  embeds; the Go binary serves the SPA from the same origin and port
  as the API, so there are no CORS concerns.
- `email.EncodeID`/`DecodeID` exist to make storage keys URL-safe, but
  the current S3 layout uses raw SES message ids directly as API ids.

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
