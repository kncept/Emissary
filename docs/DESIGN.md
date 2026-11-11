# Design

## What Emissary is

Emissary is a minimal, self-hosted email service for a domain you
control. It turns AWS SES into a small webmail application:

- **Receiving**: mail for `DOMAIN` is accepted by SES inbound, which
  stores each raw message (RFC 5322 EML) in an S3 bucket.
- **Reading**: a single Go binary serves a React webmail client and a
  JSON API that read those S3 objects directly.
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
- `mailer/cmd/sns-receiver` is a placeholder. The plan is a convenience
  Lambda that reads raw messages from SNS and writes them (RFC 822,
  keyed by SMTP message id) into one or more S3 buckets; it currently
  panics and is not wired into the CDK app.
- The `ddb` and `ldap` user managers are registered but not implemented
  (they exist so deployments can select them later); they currently
  panic for ordinary users.
- Bounce handling and multi-instance deployments are open ideas; see
  `TODO.md`.

## Core concepts

| Concept | Meaning |
| --- | --- |
| Domain | The receiving domain (`DOMAIN`). SES is configured to accept mail for it (and, optionally, its subdomains). |
| MAIL FROM subdomain | `mail.<DOMAIN>`. Used both for SES bounce handling and as the hostname the deployed application is served on. |
| Email id | The S3 object key of a message. SES writes inbound mail at the bucket root keyed by its message id, so the id is the SES message id. |
| Administrator | The single built-in user defined by `USERNAME`/`PASSWORD`. Sees every message and can send from any address. |
| Ordinary user | Any user accepted by the configured user manager. Sees only mail sent by or addressed to them, and always sends as themselves. |
| Metadata | Small per-message state that is not part of the RFC 5322 message: read and flagged flags. Stored separately from S3. |

## Visibility and authorization model

Authentication produces a JWT (HS256, Bearer token) whose `sub` claim is
the user id and which carries `admin: true` only for the built-in
administrator. Authorization is enforced in the API handler:

- Ordinary users only see messages where their address appears in
  `From`, `To`, `Cc` or `Bcc`.
- Administrators see everything, can filter the listing with
  `GET /api/emails?user=<address>`, and can send as any address.
- Messages outside a caller's visibility are reported as `404` (not
  `403`) so the existence of other users' mail is not revealed.
- Blank `PASSWORD` disables administrator login without affecting the
  other user managers.

## Technology choices

| Layer | Choice | Why |
| --- | --- | --- |
| Backend | Go, single module (`mailer/`) | One static binary, strong stdlib HTTP + MIME/mail parsing, easy Lambda custom runtime. |
| Frontend | React 19 + Vite + TypeScript SPA | Small client; built ahead of time and embedded into the Go binary with `//go:embed` so the app is one artifact with no separate static hosting. |
| Inbound transport | SES receipt rules → S3 | No server to run; raw messages are durable and cheap; the receipt rule's S3 action needs no consumer Lambda. |
| Outbound transport | SES `SendEmail` (v2 API) | Same verified identity as inbound; no SMTP client to maintain. |
| Message state | DynamoDB | Read/flag state must survive Lambda cold starts; a one-column table is the cheapest AWS answer. Local alternatives: memory or a JSON file. |
| Auth | Hand-rolled HS256 JWT | No external dependency; the claim set is small (`sub`, `admin`, `iat`, `exp`, optional `aud`/`iss`). |
| Infrastructure | AWS CDK (TypeScript) in `deploy/` | The stack is mostly DNS records + SES configuration, which is awkward to hand-manage; CDK also activates the receipt rule set via a custom resource. |

## Repository layout

```
Emissary/
├── docs/            documentation (this directory)
├── frontend/        React SPA (Vite + TypeScript)
│   └── src/         application code and vitest tests
├── mailer/          Go module: github.com/kncept/Emissary/mailer
│   ├── cmd/frontend/    webmail application binary + Lambda bootstrap
│   ├── cmd/sns-receiver/ placeholder SNS-to-S3 Lambda (not implemented)
│   ├── auth/            JWT issue/validate
│   ├── awsutil/         Lambda detection, Secrets Manager
│   ├── config/          environment-variable configuration
│   ├── email/           RFC 5322 parsing (wire format + simplified model)
│   ├── envutil/         cached env lookup with ARN secret resolution
│   ├── gui/             //go:embed of the built frontend
│   ├── handler/         HTTP API + embedded frontend server
│   ├── metadata/        read/flag state: memory, file, DynamoDB
│   ├── sender/          outbound: AWS SES, log
│   ├── server/          older HTTP server package, currently unused
│   ├── storage/         inbound messages: S3, memory
│   └── usermanager/     login backends: none, any, ddb*, ldap*
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
- **User manager** — `USER_MANAGER`: `none`, `any`, `ddb` or `ldap`.

The sender and the storage are intentionally independent: a deployment
can read mail from S3 while logging outbound mail (or vice versa).
When `METADATA` is unset the memory implementation is used, so a
bare process starts with no AWS dependencies at all.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how the components
interact, [CONFIGURATION.md](CONFIGURATION.md) for every variable,
and [DEPLOYMENT.md](DEPLOYMENT.md) for the AWS wiring.
