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
| `storage` | Read side of mail: `ListAllEmails`, `GetEmail`. |
| `sender` | Write side of mail: `Send`. |
| `metadata` | Per-message read/flag state. |
| `email` | RFC 5322 parsing: `WireFormatEmail` (full structure) and `UserEmail` (simplified). |
| `handler` | The HTTP API, auth middleware, panic recovery, and the embedded frontend. |
| `gui` | `//go:embed all:frontend/dist` — the built SPA. |
| `server` | An older HTTP server package with a different API shape; currently nothing imports it. It is kept for reference. |

## Pluggable components

Each seam is a small interface, selected by environment variable and
instantiated in `handler.New`:

| Seam | Interface | Selector | Implementations | Notes |
| --- | --- | --- | --- | --- |
| Inbound messages | `storage.Storage` | `STORAGE` | `s3`, `memory` | `s3` needs `S3_BUCKET`. Defaults to `s3` when `S3_BUCKET` is set, else `memory`. |
| Outbound mail | `sender.Sender` | `SENDER` | `aws-ses`, `log` | Defaults to `aws-ses` when `SES_FROM` is set, else `log`. |
| Message state | `metadata.MetadataStore` | `METADATA` | `memory` (default), `file`, `ddb` | `ddb` needs `DDB_TABLE`. |
| Users | `usermanager.Manager` | `USER_MANAGER` | `none` (default), `any`, `ddb`, `ldap` | `any` accepts any address in `DOMAIN` with any password. `ddb`/`ldap` are unimplemented. |

## Inbound mail flow

1. A sender's MTA resolves the MX record for `DOMAIN` and delivers to
   the SES inbound endpoint for the region.
2. SES evaluates the active receipt rule set (created and activated by
   the CDK "in" stack; activation is account-wide per region).
3. The rule's S3 action writes the raw message to the bucket at the
   **bucket root**, keyed by the SES message id. The bucket policy
   grants SES `PutObject`; no consumer Lambda is involved.
4. When a user lists or opens mail, `storage.S3Storage` paginates
   `ListObjectsV2` over the bucket, `GetObject`s each message, and
   parses it with `email.Parse`. Unparseable objects are logged and
   skipped, not fatal.
5. `handler` merges read/flag state from the metadata store into each
   response and filters the result by the caller's visibility.

Objects written at the root matter: the email id *is* the full object
key, so a key prefix would change every id.

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
- Listing/opening applies `canAccessEmail`: admins see everything
  (optionally filtered by `?user=`), users see only their own
  conversations, and anything else is a `404`.

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
