# Development

## Prerequisites

- Go 1.27+ (module root: `mailer/`)
- Node.js 24 + npm (frontend and CDK)
- An AWS account and credentials when deploying or running against S3,
  SES, DynamoDB or Secrets Manager

## Build

`./run.sh build` runs three steps:

1. **Frontend**: `npm ci && npm run build` in `frontend/`. Vite
   compiles the SPA into `mailer/gui/frontend/dist` (the output
   directory is set in `frontend/vite.config.ts`).
2. **Backends**: `go build` of `cmd/frontend` and `cmd/sns-receiver`
   into `.build/`.
3. **Lambda binary**: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
   -o bootstrap` inside `mailer/cmd/frontend`. The CDK app deploys
   exactly this file as the custom-runtime handler, so it must stay
   next to the package. `./run.sh build:lambda` runs just this step.

The frontend must be built **before** any `go build`/`go test` of the
`mailer` module: `//go:embed all:frontend/dist` in `mailer/gui` fails
when `dist/` is missing.

## Test

The full testing strategy — layers, test doubles, conventions and
the invariants that must stay covered — is documented in
[TESTING.md](TESTING.md).

`./run.sh test` builds the frontend (see above), then runs:

- `go test ./...` in `mailer/` — unit tests are colocated with the
  packages they test (`handler`, `auth`, `usermanager`, `storage`,
  `sender`, `metadata`, `email`, `config`, `cmd/frontend`, ...).
  Storage/sender tests use small fakes or the in-memory
  implementations, so they do not call AWS.
- `npm run test` in `frontend/` — vitest (`*.test.ts(x)`).

CI (`.github/workflows/ci.yml`) runs `./run.sh test` on every push and
pull request. Tagging `v*` triggers the release workflow, which runs
`./run.sh build` and uploads the `.build/frontend` and
`.build/sns-receiver` binaries as artifacts.

## Run locally

```sh
STORAGE=memory ./run.sh run
```

`run.sh run` builds first, then starts `.build/frontend`. It defaults
`USERNAME=Emissary`, generates `PASSWORD` and `JWT_SECRET` when unset
(printed at startup), and defaults `S3_BUCKET=emissary-emails` — so set
`STORAGE=memory` explicitly for an AWS-free run. With no `SES_FROM`,
outbound mail uses the log sender (messages appear in stdout only).
See [CONFIGURATION.md](CONFIGURATION.md) for the full variable list
and defaults.

For pure UI work, `cd frontend && npm run dev` starts the Vite dev
server, but it does not proxy `/api` — end-to-end work happens against
the built binary. The README notes the binary can also be embedded in
a Dockerfile.

## Extending

Each pluggable seam is a small interface plus a selector switch. To
add a new implementation, three places change:

1. Implement the interface (in its package, next to the others):
   - `storage.Storage` — `Name`, `ListAllEmails`, `GetEmail`,
     `DeleteEmail` (`mailer/storage`)
   - `sender.Sender` — `Name`, `From`, `Send` (`mailer/sender`)
   - `metadata.MetadataStore` — `Get`, `SetRead`, `SetFlagged`, `Delete`,
     `List` (`mailer/metadata`)
   - `usermanager.Manager` — `Name`, `Authenticate`, `IsAdmin`
     (`mailer/usermanager`)
2. Register it in the `switch` that builds that component
   (`handler.New` for storage/metadata/sender; `usermanager.New` for
   user managers).
3. Teach `mailer/config` to validate the new selector value and any
   required companion variables.

Add a unit test alongside the implementation; the existing
`*_test.go` files show the patterns (e.g. `storage` tests exercise
both implementations through the interface).

Currently open implementation work (see also `TODO.md`):

- `cmd/sns-receiver` — the SNS-to-S3 relay Lambda (planned to re-write
  SES notifications as RFC 822 objects keyed by SMTP message id).
- `usermanager` `ddb` and `ldap` backends (registered, panic when
  reached).
- Bounce handling, e.g. reacting to SES receipt verdicts.
