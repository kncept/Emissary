# Design

## What Emissary is

Emissary is a minimal, self-hosted email service for a domain you
control. It turns AWS SES into a small webmail application:

- **Receiving**: mail for `DOMAIN` is accepted by SES inbound, which
  stores each raw message (RFC 5322 EML) in an S3 bucket.
- **Reading**: a single Go binary serves a React webmail client and a
  JMAP API over HTTPS that reads those S3 objects as per-user mailboxes.
- **Sending**: outbound mail is sent through the SES `SendEmail` API.

There is no mail transfer agent, maildir, IMAP server or relational
database anywhere in the system. AWS does SMTP; Emissary does the web
application.

## Goals

- **Minimal operations.** The whole application is one statically
  linked Go binary. Deployed, it is a single Lambda function behind an
  API Gateway; the only stateful AWS services used are S3 (message
  storage) and DynamoDB (a few bytes of per-message state).
- **Cheap for small domains.** Pay-per-request DynamoDB, an S3 bucket,
  and a Lambda add up to approximately zero at hobby volumes.
- **Run anywhere.** The same binary runs as a Lambda (custom runtime),
  as a native process, or inside a container. Deployment mode is
  detected at startup, not chosen at build time.
- **Pluggable edges.** The pieces that talk to the outside world —
  inbound storage, outbound sender, per-message metadata, and the user
  directory used for login — are small interfaces selected with
  environment variables. The in-memory implementations make local
  development and tests independent of AWS.

## Non-goals / status

- Full IMAP/SMTP interoperability. SES is the only mail transport.
- The `ldap` user manager is registered but not implemented (it exists
  so deployments can select it later); it currently panics for ordinary
  users. The `ddb` user manager is implemented against DynamoDB.
- Bounce handling and multi-instance deployments are open ideas; see
  `TODO.md`.

## Core concepts

| Concept | Meaning |
| --- | --- |
| Domain | The receiving domain (`DOMAIN`). SES is configured to accept mail for it (and, optionally, its subdomains). |
| MAIL FROM subdomain | `mail.<DOMAIN>`. Used both for SES bounce handling and as the hostname the deployed application is served on. |
| Email id | The full S3 object key of a delivered message: `users/<accountId>/<mailboxId>/<timestamp>_<ses-message-id>.eml`. The id encodes the account, JMAP mailbox, received time and SES message id; the timestamp is part of the id. |
| Account | A JMAP account, identified by the user's email address. Storage reads are scoped to this account's prefix; `accountId` on a JMAP call selects it. |
| Mailbox | A JMAP mailbox inside an account (`inbox`, `sent`, `drafts`, ...). `Email/query` can restrict the read with `filter.inMailbox`. |
| Administrator | The single built-in user defined by `USERNAME`/`PASSWORD`. With `EmissaryUserManagement` it can read another account's mailbox by passing that account's `accountId` (masquerade); the built-in administrator account can send from any address. |
| Ordinary user | Any user accepted by the configured user manager. Reads only their own account/mailboxes and always sends as themselves. |
| Metadata | Small per-message state that is not part of the RFC 5322 message: read and flagged flags. Stored separately from S3. |

## Visibility and authorization model

Authentication produces a JWT (HS256, Bearer token) whose `sub` claim is
the user id (the user's email address). It carries `systemManager: true` for
the built-in administrator and users with the System Manager role (support
tools), and `EmissaryUserManagement: true` for users who may manage accounts
and masquerade. Authorization is enforced at both the JMAP dispatcher and the
storage boundary:

- The default `accountId` for a JMAP call is the authenticated `sub`.
  Users without `EmissaryUserManagement` may not supply a different
  `accountId`; the dispatcher rejects the call.
- Users with `EmissaryUserManagement` may masquerade by supplying any
  `accountId`. Listing, getting, deleting and submitting are all scoped to
  that account.
- `Email/query` reads only the selected account's mailbox prefix. An
  `inMailbox` filter narrows it to one JMAP mailbox; without the filter
  all of that account's mailboxes are searched.
- A message in another account is not discoverable: it is absent from
  query results and appears in `Email/get`'s `notFound` list, just like
  a message that does not exist.
- Blank `PASSWORD` disables administrator login without affecting the
  other user managers.

## Technology choices

| Layer | Choice | Why |
| --- | --- | --- |
| Backend | Go, single module (`mailer/`) | One static binary, strong stdlib HTTP + MIME/mail parsing, easy Lambda custom runtime. |
| Frontend | React 19 + Vite + TypeScript SPA | Small client; built ahead of time and embedded into the Go binary with `//go:embed` so the app is one artifact with no separate static hosting. |
| API contract | OpenAPI 3.1 specs (`api/*.yaml`) + generated stubs | Single source of truth; `ogen` generates Go server/client interfaces, OpenAPI-to-TypeScript generates typed frontend client. Eliminates drift between server and client. |
| Inbound transport | SES receipt rules → S3 | No server to run; raw messages are durable and cheap; the receipt rule's S3 action needs no consumer Lambda. |
| Outbound transport | SES `SendEmail` (v2 API) | Same verified identity as inbound; no SMTP client to maintain. |
| Message state | DynamoDB | Read/flag state must survive Lambda cold starts; a one-column table is the cheapest AWS answer. Local alternatives: memory or a JSON file. |
| Auth | Hand-rolled HS256 JWT | No external dependency; the claim set is small (`sub`, `admin`, `iat`, `exp`, optional `aud`/`iss`). |
| Infrastructure | AWS CDK (TypeScript) in `deploy/` | The stack is mostly DNS records + SES configuration, which is awkward to hand-manage; CDK also activates the receipt rule set via a custom resource. |

## Frontend UI conventions

**There must be no native browser dialogs anywhere in the app:**
`alert()`, `confirm()` and `prompt()` are never called (including as
`window.alert`, `window.confirm` or `window.prompt`). Errors and status
messages render as in-app elements; asking the user to approve an action
renders an in-app modal instead.

Destructive actions — deleting an email from the inbox, deleting an email
from the message view, and deleting a user — use the shared
`ConfirmDialog` component in
`frontend/src/components/ConfirmDialog.tsx`. It renders the existing
`.modal-*` styles and has these rules:

- Nothing is deleted until the **Delete** button is clicked.
- **Cancel**, clicking the backdrop (outside the modal card), or pressing
  **Escape** cancels the action.
- While a delete request is in flight, both buttons and all dismissal
  paths are disabled.

This is enforced in three places: oxlint's `no-alert` rule is an error in
`frontend/.oxlintrc.json`, `npm run lint` runs in CI via `./run.sh test`,
and vitest replaces the native dialog methods with throwing stubs in
`frontend/src/test-setup.ts`.

## Repository layout

```
Emissary/
├── docs/            documentation (this directory)
├── frontend/        React SPA (Vite + TypeScript)
│   └── src/         application code and vitest tests
├── mailer/          Go module: github.com/kncept/Emissary/mailer
│   ├── cmd/frontend/    webmail application binary + Lambda bootstrap
│   ├── auth/            JWT issue/validate
│   ├── awsutil/         Lambda detection, Secrets Manager
│   ├── config/          environment-variable configuration
│   ├── email/           RFC 5322 parsing (wire format + simplified model)
│   ├── envutil/         cached env lookup with ARN secret resolution
│   ├── gui/             //go:embed of the built frontend
│   ├── handler/         HTTP API + embedded frontend server
│   ├── metadata/        read/flag state: memory, file, DynamoDB
│   ├── sender/          outbound: AWS SES, log
│   ├── storage/         inbound messages: S3, memory
│   └── usermanager/     login backends: none, ddb*, s3*, ldap*
├── deploy/          AWS CDK app (three stacks)
├── run.sh           build / test / run / deploy entry point
└── .github/         CI (test) and release (build artifacts) workflows
```

## How the pieces are selected

`cmd/frontend` loads configuration from environment variables
(`mailer/config`), builds concrete implementations of each pluggable
interface, and wires them into the HTTP handler (`mailer/handler`):

- **Storage** (read side of mail) — `STORAGE`: `s3` or `memory`.
- **Sender** (write side of mail) — `SENDER`: `aws-ses` or `log`.
- **Metadata** — `METADATA`: `memory`, `file` or `ddb`.
- **User manager** — `USER_MANAGER`: `none`, `ddb`, `s3` or `ldap`.
  When unset, the `s3` backend is selected automatically if storage is S3.

The sender and the storage are intentionally independent: a deployment
can read mail from S3 while logging outbound mail (or vice versa).
When `METADATA` is unset the memory implementation is used, so a
bare process starts with no AWS dependencies at all.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how the components
interact, [CONFIGURATION.md](CONFIGURATION.md) for every variable,
and [DEPLOYMENT.md](DEPLOYMENT.md) for the AWS wiring.
