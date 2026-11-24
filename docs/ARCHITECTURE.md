# Architecture

One Go binary serves both the API and the webmail client. This page explains how the pieces connect;
[DESIGN.md](DESIGN.md) covers rationale and [CONFIGURATION.md](CONFIGURATION.md) covers every setting.

## System overview

Deployed on AWS:

```
                  Route 53: MX <DOMAIN> → SES inbound; MX mail.<DOMAIN> → SES feedback
                  DKIM CNAMEs, SPF TXT, DMARC TXT
                                 │
   inbound mail ────────────────► SES receipt rule → S3 staging (raw EML)
                                 │
  https://mail.<DOMAIN> ────────► API Gateway → Lambda (Go, custom runtime)
         ▲                            │  DynamoDB metadata / sessions / users
         │ A alias + ACM cert          │  Secrets Manager JWT secret
    browser ── React SPA ────────────► one http.Handler: /api/* JSON, /* SPA
         outbound ◄──────────── SES SendRawEmail ◄──── EmailSubmission/set
```

SES receives mail for `DOMAIN` and writes the untouched RFC 5322 message to an S3 staging area under its message id.
A consumer process validates recipients and delivers a copy to each user's mailbox; nothing writes mail directly
into another account's tree. API Gateway proxies HTTPS requests to the Lambda, which hands API Gateway events to the
same `http.Handler` used for native runs. Outbound submissions go through the configured sender (SES or direct
SMTP). See [S3.md](S3.md#bucket-layout) for keys and [DEPLOYMENT.md](DEPLOYMENT.md#the-three-stacks) for the stacks.

The same handler runs natively with `net/http`. For an AWS-free run the local or no-op backends (`memory`, `log`,
`none`) must be selected explicitly; `./run.sh run` does that when no backend configuration is present.
`config.Load` never chooses them as a fallback ([CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components)).

## Application startup

[`mailer/cmd/frontend/main.go`](../mailer/cmd/frontend/main.go) is the entry point for both the web application and the `bootstrap` Lambda:

1. [`config.Load`](../mailer/config/config.go) reads and validates the
   environment, applies non-backend defaults, and resolves any value beginning with `arn:aws:` through
   [`envutil`](../mailer/envutil/envutil.go) and [`awsutil`](../mailer/awsutil/awsutil.go). This is how the deployed
   Lambda receives its JWT secret from Secrets Manager as an ARN.
2. [`configvendor.FromConfig`](../mailer/config-vendor/vendor.go) selects and
   constructs every pluggable component, returning a `Bundle` with the user manager (built-in administrator plus
   selected backend), storage, metadata store, sender, session store, receiver config store and loaded routing rules.
3. [`handler.New`](../mailer/handler/handler.go) takes that `Bundle` plus a few
   scalar settings, builds the [`auth.Authenticator`](../mailer/auth/auth.go), and wires routes, auth middleware,
   panic recovery and the embedded frontend into one `http.Handler`.
4. [`awsutil.IsAwsLambda`](../mailer/awsutil/awsutil.go) picks the runtime: in
   Lambda the handler is passed to `aws-lambda-go-api-proxy` so API Gateway events become `net/http` requests (custom
   runtime `provided.al2023`); otherwise the process listens on `LISTEN_ADDR`/`PORT`, with bounded timeouts and
   graceful SIGINT/SIGTERM shutdown.

The standalone [`s3-receiver`](../mailer/cmd/s3-receiver) binary reuses the same config, storage and receiver
packages to process staged objects outside the web process.

## Package map

| Package | Responsibility |
| --- | --- |
| [`cmd/frontend`, `cmd/s3-receiver`](../mailer/cmd/) | Web application and inbound-consumer binaries. |
| [`config`](../mailer/config/) | Environment loading and validation; [`config-vendor`](../mailer/config-vendor/) constructs the selected components. |
| [`envutil`, `awsutil`](../mailer/envutil/) | Cached lookups, ARN secret resolution, Lambda detection. |
| [`auth`](../mailer/auth/auth.go) | JWT issue/validate; [`Claims`](../mailer/auth/auth.go) carries `sub`, `sid`, `systemManager`, `EmissaryUserManagement`, `iat`, `exp` and optional `aud`/`iss`. |
| [`components/usermanager`](../mailer/components/usermanager/) | Login backends; every backend also knows the built-in administrator. |
| [`components/storage`](../mailer/components/storage/) | Mail source: `ListEmails` is scoped by internal user id and mailbox; `RootObject`/`ListRoot` exist only for support tools. |
| [`components/sender`](../mailer/components/sender/) | Outbound `Send`. |
| [`components/metadata`](../mailer/components/metadata/) | JMAP keywords and server-owned `receivedAt`, keyed by email id and user id; deleted with the message. |
| [`components/session`](../mailer/components/session/) | Refresh-token store for JWT sessions. |
| [`email`](../mailer/email/) | RFC 5322 parsing and rendering: `WireFormatEmail` (lossless) and `UserEmail` (simplified). |
| [`handler`](../mailer/handler/) | HTTP API, auth middleware, panic recovery and the embedded frontend. |
| [`jmap`](../mailer/jmap/) | JMAP method dispatch and wire types generated from `api/jmap.yaml`. |
| [`receiver`](../mailer/receiver/) | Inbound routing rules, recipient validation and delivery. |
| [`gui`](../mailer/gui/gui.go) | `//go:embed all:frontend/dist` serving the built SPA. |

Each seam is a small interface selected by environment variable; local/no-op implementations are explicit opt-ins, never fallbacks.

## Pluggable components

A selector must be set explicitly, or exactly one backend must have configuration; otherwise startup fails. See
[CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components) for the full rules and settings, and
[DESIGN.md](DESIGN.md#how-the-pieces-are-selected) for the interfaces.

| Seam | Interface | Selector | Implementations | Notes |
| --- | --- | --- | --- | --- |
| Inbound messages | [`storage.Storage`](../mailer/components/storage/storage.go) | `STORAGE` | `s3`, `filesystem`, `memory` | `s3` needs `S3_BUCKET`; `filesystem` needs `FILESYSTEM_DIR`; `memory` is an opt-in. |
| Outbound mail | [`sender.Sender`](../mailer/components/sender/sender.go) | `SENDER` | `aws-ses`, `mta`, `log` | `aws-ses` uses the SDK credential chain; `mta` needs `MTA_PORT`; `log` is an opt-in. From comes from the caller. |
| Message state | [`metadata.MetadataStore`](../mailer/components/metadata/interface.go) | `METADATA` | `memory`, `file`, `ddb` | Keywords plus `receivedAt` by email id and user id; `ddb` requires `TABLE_PREFIX`. |
| Sessions | [`session.SessionStore`](../mailer/components/session/session.go) | `SESSION` | `memory`, `ddb` | Refresh tokens; `ddb` requires `TABLE_PREFIX`; `memory` is an opt-in. |
| Receiver rules | [`receiver.ConfigStore`](../mailer/receiver/receiver_config_store.go) | `RECEIVER` | `s3`, `filesystem`, `ddb`, `memory` | One ordered rule set per mail identity; overrides use `RECEIVER_BUCKET`/`RECEIVER_FILESYSTEM_DIR`. |
| Users | [`usermanager.UserManager`](../mailer/components/usermanager/usermanager.go) | `USER_MANAGER` | `none`, `ddb`, `filesystem`, `s3`, `ldap` | `ddb`, `s3`, `filesystem` are Emissary-mastered; `ldap` unimplemented; `none` is an opt-in. |

Storage and sending are independent: a deployment can read mail from S3 while logging outbound mail, or use the
memory store with a real sender.

## Inbound mail flow

1. The sender's MTA resolves the MX record for `DOMAIN` and delivers to SES; the
   active receipt rule set (created by the `in` stack) writes the raw message to S3 staging under the SES message id.
   The bucket policy grants SES `PutObject`; no consumer Lambda is involved in this first write.
2. A consumer (Lambda or scheduled job) parses the staged object with
   [`email.ParseWireFormat`](../mailer/email/wireformat_email.go) and collects SMTP envelope recipients from the
   topmost `Received` header's `for <address>` clauses. `To`, `Cc` and `Bcc` are sender-controlled and never used for
   routing; a message without an envelope recipient is skipped.
3. Recipients are validated and routed as described in
   [Recipient validation and delivery](#recipient-validation-and-delivery). Rules come from
   [`HandlerRules`](../mailer/receiver/receiver_config.go) loaded from the configured store; see
   [CONFIGURATION.md](CONFIGURATION.md#receiver-inbound-routing-rules).
4. A delivery target gets
   `Storage.AddEmail(ctx, recipient, mailboxID, parsedEmail, raw)` on the configured store. The store assigns the
   stable id and writes the original bytes verbatim to that recipient's `inbox` (see [S3.md](S3.md#inbound-flow)), so
   every header, MIME part and attachment is preserved; the receiver never constructs mailbox keys. Metadata is created
   from the returned id with an empty keyword set and a server-stamped `receivedAt` (RFC 3339 UTC); `receivedAt` is
   never client-assignable.
5. A marker derived from the staging key and recipient is written under
   `_processed/` after each recipient is handled, so a retry skips completed recipients. Staging is deleted only once
   every recipient has been handled successfully; a failure leaves it in place and returns an error for retry. Markers
   are cleared after staging deletion; stale markers are harmless because SES message ids are unique.
6. For JMAP reads, the dispatcher resolves the public opaque `accountId` to the
   internal user id and [`storage`](../mailer/components/storage/) paginates that account's mailbox prefix; an
   `inMailbox` filter narrows the read to one mailbox. `SortListedEmails` gives `Email.Date` descending with id
   descending as tie-breaker across all backends; unparseable objects are logged and skipped.
   [`handler`](../mailer/handler/) merges keywords and `receivedAt` into `Email/get`, sorts queries by receive time,
   and reports a missing metadata record as empty keywords with a `Date` fallback. There is no cross-account listing
   API.

The JMAP `Id` is a URL-safe token derived from the delivered filename (`<timestamp>_<generated-id>.eml`), with the
account and mailbox supplied by the JMAP context; a move changes the physical key but not the id. Older
SES-message-id suffixes and legacy full-key inputs remain readable. See [S3.md](S3.md#bucket-layout) and [API.md](API.md#jmap-mail-operations).

Durable per-session scratch state lives at `_storage/<sid>/` (JSON drafts and `created-ids.json`), where `<sid>` is
the JWT session id; drafts never appear in mailbox listings. JMAP counters, bounded change logs and query snapshots
live at `_jmap-state/<base64url(accountId)>/state.json`; writes use an S3 ETag condition or filesystem content hash
so a stale Lambda cannot regress state. The same store drives `/eventsource` polling and verified `PushSubscription`
webhooks, and inbound delivery advances `EmailDelivery` so other processes become visible to push clients ([API.md](API.md#state-changes)).

### Recipient validation and delivery

When a consumer processes a staged object, each recipient is handled independently:

1. **Extract recipients** — [`email.ParseWireFormat`](../mailer/email/wireformat_email.go)
   gives the lossless wire type (unlike the simplified `UserEmail` used for delivered `.eml` files). SES writes one
   `for <address>` clause per `RCPT TO` in the topmost `Received` header; header recipients are ignored.
2. **Resolve routing rules** — Load the ordered
   [`HandlerRules`](../mailer/receiver/receiver_config.go) for the mail identity (the hostname after `@`), falling
   back to the `_` entry. First applicable rule wins:
   - `DeliverToUser` — only when `UserManager.Lookup` returns a known user,
     including the built-in administrator.
   - `DeliverTo:<address>` — only when the saved target address resolves to a
     known user; the message is delivered to that account rather than the
     envelope recipient. An unknown target falls through to the next rule.
   - `DeliverToMailIdentity` — delivers to the identity's own mailbox without
     requiring a user account.
   - `Bounce` — a standard `UserUnknown` bounce through the configured sender,
     with the original message id and a `BounceSender` address.
   - `Drop` — silent discard for that recipient; the only path that discards
     without notice.
   With no applicable rule, or no loadable rules, the receiver bounces rather than discards; place terminal rules last.
   The support API rejects a new `DeliverTo:` target whose saved address does not resolve to a user at save time, so the GUI cannot persist a rule that can never apply. A target deleted later still falls through as described above.
3. **Deliver to selected recipients** — Call `Storage.AddEmail` with the
   original staging bytes, initialize metadata from the returned id, and give each target their own copy in their
   `inbox`. Delivery, bouncing and dropping are decided per recipient, so one message can have mixed outcomes.
4. **Mixed outcomes and retries** — Each delivery gets its own physical copy,
   enabling independent move/delete/flag per user; each bounce is a separate message. Staging is deleted only after
   every recipient is handled. A failed delivery, bounce or delete leaves the object for retry, and the `_processed/`
   markers ensure a retry repeats only failed work.

See [SUPPORT.md](SUPPORT.md#receiver-rules-editor) for the rules editor and [S3.md](S3.md#bucket-layout) for the staging layout.

## Outbound mail flow

1. The client creates a draft with `Email/set` and submits it with
   `EmailSubmission/set` on the JMAP batch endpoint. With a JWT `sid`, [`handler`](../mailer/handler/) writes drafts
   to `_storage/<sid>/drafts/` rather than the delivered-mail tree; submission reads that draft first and falls back to
   delivered mail.
2. The handler verifies the email belongs to the request's `accountId` and locks
   From to that account (or the masquerade target). A client-supplied From is ignored; a masqueraded Reply-To must
   match From, defaulting to From when omitted. See [API.md](API.md#send-email).
3. [`sender.Sender`](../mailer/components/sender/) serializes the message once
   through [`email.RenderHeaders`](../mailer/email/headers.go), including `In-Reply-To`, `References` and the stored
   `Message-ID`; the same renderer backs `email.RawFromUserEmail`, so address formatting and CRLF sanitization live
   in one place. `aws-ses` calls `SendRawEmail` with envelope recipients passed separately; `mta` resolves MX records
   and speaks SMTP on `MTA_PORT` after a DMARC/SPF alignment warning check; `log` writes a process-log line. The exact
   SMTP envelope is modeled in [`email.SubmissionEnvelope`](../mailer/email/envelope.go) so Bcc and SMTP-only
   recipients never enter the headers.
4. On success the session draft and its metadata are deleted; on failure the
   draft is kept so the client can retry without re-entering the message.

## Authentication and visibility

[`POST /api/auth/login`](../mailer/handler/handler.go) checks the built-in administrator first (constant-time
compare), then the configured user manager. A successful login returns an HS256 access/refresh pair over a new
session; [`requireAuth`](../mailer/handler/handler.go) validates signature, expiry, audience and issuer, then
stashes claims in the request context. The JMAP dispatcher defaults to the `acct_` id derived from the JWT `sub` and
rejects another account unless the caller has `EmissaryUserManagement`, which permits masquerade. `Email/query`,
`Email/get`, `Email/set` and `EmailSubmission/set` are scoped to that account by storage prefix: another account's
mail is absent from results and reported `notFound`. Each recipient has their own object, so deleting a message
affects only that account's copy. Token lifetimes, refresh stepping and login limits are in [AUTH.md](AUTH.md);
account management and masquerade are in [USERS.md](USERS.md#admin-masquerade) and [API.md](API.md#account-selection-and-masquerade).

## Request handling details

- All API routes live under `/api`; unknown `/api` paths return JSON `404`,
   every other path serves the SPA with `index.html` fallback. Long-lived immutable caching applies only to `/assets/`.
- REST bodies are capped at 1 MiB; JMAP batches by `MAX_FILESIZE` (10 MiB by
   default). Oversized batches return 413 with `urn:ietf:params:jmap:error:limit`.
- A top-level `recover` converts panics to JSON `500`; client errors are
   generic and details are logged server-side only. See [API.md](API.md#conventions) and
   [CONFIGURATION.md](CONFIGURATION.md#server-and-authentication).

## Email data model

The types live in [`mailer/email/`](../mailer/email/):

- [`WireFormatEmail`](../mailer/email/wireformat_email.go) — lossless, lenient
   RFC 5322 parsing (headers, MIME tree, transfer encodings, charsets, folded headers) that can serialize back to wire
   format. Parse errors are collected rather than fatal; the whole message and each decoded part are capped by
   `MAX_FILESIZE`. Used for inbound routing.
- [`UserEmail`](../mailer/email/email.go) — simplified content used by storage,
   sender and HTTP layers: addresses, subject, date, text and HTML bodies. [`EmailRef`](../mailer/email/email.go) holds
   its storage id plus derived `AccountID`/`MailboxID`, and [`ListedEmail`](../mailer/email/email.go) pairs the two.
   [`Parse`](../mailer/email/email.go) is the convenience path used by storage; attachments are ignored there, while
   `Email/set` creates can supply an [`Attachment`](../mailer/email/email.go).
- [`RenderHeaders`](../mailer/email/headers.go) — shared RFC 5322 header writer
   used by both senders and by `email.RawFromUserEmail`.
- [`SubmissionEnvelope`](../mailer/email/envelope.go) — send-time MAIL FROM and
   RCPT TO metadata, kept separate from the stored message.

## Frontend

[`frontend/`](../frontend/) is a React 19 + Vite + TypeScript SPA (React Router) with routes `/login`, `/` (current
account inbox), `/email/:id`, `/compose` and `/compose/:id` (reply). [`api.ts`](../frontend/src/api.ts) keeps the
JWT in `localStorage`, sends the Bearer header and redirects to `/login` on `401`;
[`masquerade.ts`](../frontend/src/masquerade.ts) converts a selected masquerade email address to the opaque `acct_`
id before use. Vite builds into `mailer/gui/frontend/dist`, which [`mailer/gui`](../mailer/gui/gui.go) embeds; the
Go binary serves the SPA from the same origin as the API, so CORS is off. Long-lived streams use the
`eventSourceUrl` advertised by the JMAP Session. Dialog conventions are in [DESIGN.md](DESIGN.md#frontend-ui-conventions).

## Generated API artifacts

The OpenAPI specs in [`api/`](../api/) are the source of truth for paths and schemas. `./run.sh generate:api`
regenerates Go types, decoders, validators and router middleware under `api/*/generated/` and TypeScript fetch
helpers under [`frontend/src/api/generated/`](../frontend/src/api/generated/); `./run.sh check:generated` detects
staleness. The handler implements the generated auth, user and support server interfaces; JMAP is dispatched by
[`mailer/jmap/`](../mailer/jmap/) from generated wire types, with no generated server stub.
[`api/spec_test.go`](../api/spec_test.go) checks spec↔runtime agreement. See [API.md](API.md#generated-api-artifacts).

## Deployment shape

Three CDK stacks ([DEPLOYMENT.md](DEPLOYMENT.md#the-three-stacks)):

1. **id** — SES identity for `DOMAIN` with MAIL FROM `mail.<DOMAIN>`, plus DKIM,
   MX, SPF and DMARC records.
2. **in** — inbound MX, raw-message bucket, receipt rule set and activation,
   and an SSM parameter publishing the bucket name.
3. **client** — the frontend and `s3-receiver` Lambdas, API Gateway with TLS and
   a `mail.<DOMAIN>` alias, DynamoDB tables and the JWT secret. This stack is skipped when `bootstrap` has not been
   built.
