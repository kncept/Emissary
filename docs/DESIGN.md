# Design

## What Emissary is

Emissary is a minimal, self-hosted email service for a domain you control. It turns AWS SES into a small webmail
application:

- **Receiving**: SES inbound stores each raw RFC 5322 message in an S3 bucket
  ([S3.md](S3.md#bucket-layout)); a consumer validates recipients and fills per-user mailboxes.
- **Reading**: one Go binary serves a React client and a JMAP API that reads those objects as mailboxes
  ([API.md](API.md#jmap-mail-operations)).
- **Sending**: outbound mail goes through SES (`SENDER=aws-ses`) or directly to recipient MX hosts
  (`SENDER=mta`), as described in [ARCHITECTURE.md](ARCHITECTURE.md#outbound-mail-flow).

There is no persistent mail queue, maildir, IMAP server or relational database. The `mta` sender speaks SMTP itself;
Emissary is the web application. See [ARCHITECTURE.md](ARCHITECTURE.md#system-overview) for the deployed shape.

## Goals

- **Minimal operations.** One statically linked Go binary, deployed as a single Lambda behind API Gateway. The only
  stateful AWS services are S3 (message storage) and DynamoDB (a few bytes of per-message state).
- **Cheap for small domains.** Pay-per-request DynamoDB, one S3 bucket and a Lambda cost approximately nothing at
  hobby volumes ([DEPLOYMENT.md](DEPLOYMENT.md#the-three-stacks)).
- **Run anywhere.** The same binary runs as a Lambda custom runtime, a native process or a container; the mode is
  detected at startup by [`awsutil.IsAwsLambda`](../mailer/awsutil/awsutil.go), not chosen at build time
  ([DEVELOPMENT.md](DEVELOPMENT.md#run-locally)).
- **Pluggable edges.** Inbound storage, outbound sender, message metadata, sessions, receiver rules and the user
  directory are small interfaces selected with environment variables — see
  [how the pieces are selected](#how-the-pieces-are-selected). A backend is explicit, or inferred when exactly one
  has configuration; local/no-op implementations are opt-ins only.

## Non-goals / status

- Full IMAP/SMTP interoperability. Outbound SMTP is direct and unqueued; there is no local queue or DKIM signing.
- The `ldap` user manager is registered but unimplemented; `ddb`, `s3` and `filesystem` are implemented
  ([USERS.md](USERS.md)).
- Bounce handling and multi-instance deployments remain open ideas.

## Core concepts

| Concept | Meaning |
| --- | --- |
| Domain | Receiving domain (`DOMAIN`); SES accepts mail for it and optionally its subdomains. |
| MAIL FROM subdomain | `mail.<DOMAIN>`, used for SES bounce handling and as the application's hostname. |
| Email id | RFC 8620 `Id` containing the base64url-encoded delivered token, `users/<internal-user-id>/<mailboxId>/<timestamp>_<message-id>.eml`. Timestamps use ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`); older SES-message-id suffixes remain readable. See [S3.md](S3.md#bucket-layout). |
| Account | JMAP account with an opaque `acct_` id derived from the user's email; `accountId` selects it on a call. |
| Mailbox | JMAP mailbox inside an account (`inbox`, `sent`, `drafts`, ...); `filter.inMailbox` narrows a query. |
| Administrator | The built-in `USERNAME`/`PASSWORD` user; with `EmissaryUserManagement` it can masquerade as another account and send as that account. |
| Ordinary user | A user accepted by the configured user manager; reads only their own account and sends as themselves. |
| Metadata | JMAP keywords, the server-owned `receivedAt` timestamp, the current mailbox id and the message subject, stored separately from the message ([`mailer/components/metadata/`](../mailer/components/metadata/)). |

Authorization is enforced in the JMAP dispatcher and at the storage boundary; users cannot discover other accounts'
mail. See [ARCHITECTURE.md](ARCHITECTURE.md#authentication-and-visibility) for enforcement and
[AUTH.md](AUTH.md) for token details.

## Visibility model (short)

Authentication issues an HS256 Bearer token whose `sub` is the user's email address. `systemManager` enables support
tools; `EmissaryUserManagement` permits user management and masquerade. The default JMAP `accountId` is derived from
`sub`; another account may be selected only with that claim. Queries and gets are scoped to the selected account's
storage prefix, so another user's message is absent from results and appears in `notFound`. Each recipient has their
own copy, so deleting affects only one account. Blank `PASSWORD` disables administrator login without affecting other
user managers. Full rules are in [ARCHITECTURE.md](ARCHITECTURE.md#authentication-and-visibility),
[AUTH.md](AUTH.md) and [USERS.md](USERS.md#admin-masquerade).

## Technology choices

| Layer | Choice | Why |
| --- | --- | --- |
| Backend | Go, one module ([`mailer/`](../mailer/)) | Static binary, strong stdlib HTTP and MIME parsing, easy Lambda custom runtime. |
| Frontend | React 19 + Vite + TypeScript ([`frontend/`](../frontend/)) | Small SPA built ahead of time and embedded with `//go:embed`, so there is one artifact and no separate static hosting. |
| API contract | OpenAPI 3.1 ([`api/*.yaml`](../api/)) + generated stubs | One source of truth for Go and TypeScript clients; see [API.md](API.md#generated-api-artifacts). |
| Inbound | SES receipt rules → S3 | No server to run; raw messages are durable and cheap; the initial write needs no consumer Lambda. |
| Outbound | SES raw send or direct SMTP | Reuses the verified identity, or delivers without SES when `SENDER=mta`. |
| Message state | DynamoDB, file or memory | Read/flag state must survive Lambda cold starts; local alternatives exist for development. |
| Auth | Hand-rolled HS256 JWT | Small claim set; no external dependency. See [`auth.Claims`](../mailer/auth/auth.go). |
| Infrastructure | AWS CDK v2 ([`deploy/`](../deploy/)) | Mostly DNS and SES configuration, with rule-set activation via a custom resource. |

## Frontend UI conventions

There must be no native browser dialogs anywhere in the app: `alert()`, `confirm()` and `prompt()` are never called,
not even as `window.alert`, `window.confirm` or `window.prompt`. Errors and status messages render as in-app elements,
and actions needing approval render an in-app modal instead. See
[TESTING.md](TESTING.md#protected-invariants).

Destructive actions — deleting an email from the list or message view, and deleting a user — use the shared
[`ConfirmDialog`](../frontend/src/components/ConfirmDialog.tsx) component, which reuses the existing `.modal-*`
styles:

- Nothing is deleted until the **Delete** button is clicked.
- **Cancel**, clicking the backdrop outside the modal card, or pressing **Escape** cancels the action.
- While a delete request is in flight, both buttons and every dismissal path are disabled.

This is enforced in three places: oxlint's `no-alert` rule is an error in
[`frontend/.oxlintrc.json`](../frontend/.oxlintrc.json), `npm run lint` runs in CI via `./run.sh test`, and vitest
replaces the native dialog methods with throwing stubs in
[`frontend/src/test-setup.ts`](../frontend/src/test-setup.ts). The same component is used by
[`Users.tsx`](../frontend/src/components/Users.tsx) and
[`EmailView.tsx`](../frontend/src/components/EmailView.tsx). See
[TESTING.md](TESTING.md#protected-invariants) and [USERS.md](USERS.md).

## Repository layout

- [`docs/`](.) — this documentation; start at [README.md](../README.md).
- [`frontend/`](../frontend/) — React SPA; `src/` holds application code, components and vitest tests.
- [`mailer/`](../mailer/) — Go module `github.com/kncept/Emissary/mailer`:
  [`cmd/`](../mailer/cmd/) binaries, [`components/`](../mailer/components/) pluggable backends, and
  [`handler/`](../mailer/handler/), [`jmap/`](../mailer/jmap/), [`receiver/`](../mailer/receiver/),
  [`config/`](../mailer/config/) and [`config-vendor/`](../mailer/config-vendor/) application wiring.
- [`deploy/`](../deploy/) — AWS CDK app (three stacks).
- [`api/`](../api/) — OpenAPI specs and generated Go/TypeScript artifacts.
- [`run.sh`](../run.sh) — build / test / run / deploy entry point; [`.github/`](../.github/) holds CI and release
  workflows.

## How the pieces are selected

[`cmd/frontend`](../mailer/cmd/frontend/main.go) loads environment configuration, constructs concrete
implementations of each interface via [`configvendor.FromConfig`](../mailer/config-vendor/vendor.go), and wires them
into [`handler.New`](../mailer/handler/handler.go):

| Seam | Interface | Selector |
| --- | --- | --- |
| Inbound messages | [`storage.Storage`](../mailer/components/storage/storage.go) | `STORAGE` |
| Outbound mail | [`sender.Sender`](../mailer/components/sender/sender.go) | `SENDER` |
| Message state | [`metadata.MetadataStore`](../mailer/components/metadata/interface.go) | `METADATA` |
| Sessions | [`session.SessionStore`](../mailer/components/session/session.go) | `SESSION` |
| Receiver rules | [`receiver.ConfigStore`](../mailer/receiver/receiver_config_store.go) | `RECEIVER` |
| Users | [`usermanager.UserManager`](../mailer/components/usermanager/usermanager.go) | `USER_MANAGER` |

Storage and sending are intentionally independent: read from S3 while logging outbound mail, or use the memory store
with a real sender. A selector must be set explicitly or exactly one backend must have configuration; local/no-op
backends are opt-ins. See [CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components) for the
full rules and [ARCHITECTURE.md](ARCHITECTURE.md#application-startup) for what the wiring builds.
