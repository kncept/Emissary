# Development

## Prerequisites

- Go 1.27+ (module root: `mailer/`)
- Node.js 24 + npm (frontend and CDK)
- An AWS account and credentials when deploying or running against S3,
  SES, DynamoDB or Secrets Manager

## Build

`./run.sh build` runs four steps:

1. **Generate API interfaces**: `generate_api` (see below) produces
   Go server/client stubs from the OpenAPI specs for the backend and
   TypeScript types + a typed API client for the frontend. This step
   runs first so both sides compile against the same contract.
2. **Frontend**: `npm ci && npm run build` in `frontend/`. Vite
   compiles the SPA into `mailer/gui/frontend/dist` (the output
   directory is set in `frontend/vite.config.ts`). The frontend build
   consumes the generated TypeScript API client.
3. **Backends**: `go build` of `cmd/frontend` and `cmd/sns-receiver`
   into `.build/`. The Go code uses the generated server interfaces
   from step 1.
4. **Lambda binary**: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
   -o bootstrap` inside `mailer/cmd/frontend`. The CDK app deploys
   exactly this file as the custom-runtime handler, so it must stay
   next to the package. `./run.sh build:lambda` runs just this step.

The frontend must be built **before** any `go build`/`go test` of the
`mailer` module: `//go:embed all:frontend/dist` in `mailer/gui` fails
when `dist/` is missing.

### API interface generation

`run.sh generate_api` (invoked automatically by `build` and `test`)
runs two generators against the three OpenAPI documents in `api/`:

- **Backend (Go)**: `go tool ogen` targeting `api/auth/generated`,
  `api/users/generated`, `api/jmap/generated`. Outputs include:
  - Go types for all request/response schemas
  - Server interfaces (`Handler` per operation group) that the
    application implements
  - Client interfaces for calling other Emissary instances
  - Request decoders, response encoders, validators, router, middleware
- **Frontend (TypeScript)**: An OpenAPI-to-TypeScript generator
  (e.g., `openapi-typescript` + `openapi-fetch` or `orval`) targeting
  `frontend/src/api/generated`. Outputs include:
  - TypeScript interfaces for all request/response bodies and params
  - A fully typed fetch client (`createClient`) with method signatures
    matching the OpenAPI `operationId`s
  - Zod schemas or equivalent for runtime validation if desired

The generated frontend client is imported by `frontend/src/api.ts`
(which becomes a thin re-export or wrapper for auth token handling),
and the generated types replace the manual `frontend/src/types.ts`.

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
(printed at startup). `S3_BUCKET` must be set explicitly when using
`STORAGE=s3` — set `STORAGE=memory` explicitly for an AWS-free run. With
no `SES_FROM`, outbound mail uses the log sender (messages appear in
stdout only). See [CONFIGURATION.md](CONFIGURATION.md) for the full
variable list and defaults.

For pure UI work, `cd frontend && npm run dev` starts the Vite dev
server, but it does not proxy `/api` — end-to-end work happens against
the built binary. The README notes the binary can also be embedded in
a Dockerfile.

## Extending

Each pluggable seam is a small interface plus a selector switch. To
add a new implementation, three places change:

1. Implement the interface (in its package, next to the others):
   - `storage.Storage` — `Name`, `ListAccountEmails`,
     `ListAllEmails` (legacy/admin only), `GetEmail`, `DeleteEmail`
     (`mailer/storage`). Listings must be scoped by JMAP `accountId`
     and optional `mailboxId`.
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
- `usermanager` `ldap` backend (registered, panics when reached).
- Bounce handling, e.g. reacting to SES receipt verdicts.
