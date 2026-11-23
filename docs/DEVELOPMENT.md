# Development

How to build, run and extend Emissary locally. The wire contract is in [API.md](API.md); component selection rules are in
[CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components); the testing strategy is in [TESTING.md](TESTING.md).

## Prerequisites

- Go 1.27+ (module root: `mailer/`)
- Node.js 24 + npm (frontend and CDK)
- An AWS account and credentials only when deploying or running against S3, SES, DynamoDB or Secrets Manager (see
  [DEPLOYMENT.md](DEPLOYMENT.md#prerequisites)); the local run needs none.
- `git`, `curl`, Go, `npm` and `node` for the external conformance gate run by CI and the release workflow
  ([TESTING.md](TESTING.md#external-jmap-conformance-suite)).

## No `npx`

We never use `npx` — not in [`run.sh`](../run.sh), npm scripts, [`deploy/cdk.json`](../deploy/cdk.json), CI or documentation. `npx` fetches
whatever version happens to be current at the time of the call, so the same command can behave differently across machines and can break after an
upstream release with no change in this repo. It also keeps the tool out of the manifest, hiding what the build actually depends on.

Every CLI tool is instead a declared dependency in the `package.json` of the directory that uses it, exposed as an npm script and invoked with
`npm run`.

### Workaround when you would reach for `npx`

1. Install the tool into the relevant `package.json`, normally as a dev dependency: `npm install --save-dev <tool>` from that directory. Commit
   both `package.json` and `package-lock.json` so `npm ci` restores it in clean checkouts and CI.
2. Add a script that calls the local binary by name, e.g. `"orval": "orval"`. npm puts the local `node_modules/.bin` on the PATH for scripts, so
   nothing is downloaded or installed globally.
3. Replace `npx <tool>` with `npm run <script>`, or `npm run <script> -- [args]` when forwarding arguments.
4. Ensure the calling flow runs `npm ci` first so the local binary is present.

Concretely: `npx orval` became the `orval` dev dependency plus the `"orval": "orval"` script in
[`frontend/package.json`](../frontend/package.json), and [`run.sh`](../run.sh) runs `cd frontend && npm ci && npm run orval`. `npx ts-node` in
[`deploy/cdk.json`](../deploy/cdk.json) became the `cdkapp` script in [`deploy/package.json`](../deploy/package.json), invoked as `npm run
--silent cdkapp` (silent so only the cloud assembly goes to stdout); `npx cdk ...` is `npm run cdk -- ...`. The same rule applies to linting and
testing: `npm run lint` and `npm run test` in [`frontend/package.json`](../frontend/package.json).

## Build

`./run.sh build` runs four steps:

1. **Generate API interfaces**: `generate_api` produces Go server/client stubs from the OpenAPI specs for the backend and TypeScript types plus a
   typed API client for the frontend. This runs first so both sides compile against the same contract ([API.md](API.md#generated-api-artifacts)).
2. **Frontend**: `npm ci && npm run build` in [`frontend/`](../frontend/). Vite compiles the SPA into `mailer/gui/frontend/dist` (the output
   directory is set in [`vite.config.ts`](../frontend/vite.config.ts)). The frontend build consumes the generated TypeScript API client.
3. **Backends**: `go build` of [`cmd/frontend`](../mailer/cmd/frontend/) and [`cmd/s3-receiver`](../mailer/cmd/s3-receiver/) into `.build/`. The
   Go code uses the generated server interfaces from step 1.
4. **Lambda binary**: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bootstrap` inside [`mailer/cmd/frontend`](../mailer/cmd/frontend/). The
   CDK app deploys exactly this file as the custom-runtime handler, so it must stay next to the package. `./run.sh build:lambda` runs just this
   step (see [DEPLOYMENT.md](DEPLOYMENT.md#the-three-stacks)).

The frontend must be built **before** any `go build`/`go test` of the `mailer` module: `//go:embed all:frontend/dist` in
[`mailer/gui`](../mailer/gui/gui.go) fails when `dist/` is missing. This is why `./run.sh test` builds the frontend before invoking Go (see
[TESTING.md](TESTING.md#running)).

### API interface generation

`./run.sh generate:api` (the `generate_api` helper, invoked automatically by `build` and `test`) runs two generators against the OpenAPI
documents in [`api/`](../api/), whose path ownership is described in [API.md](API.md#specs-and-path-ownership):

- **Backend (Go)**: `go tool ogen` targeting [`api/auth/generated`](../api/auth/generated/), [`api/users/generated`](../api/users/generated/) and
  [`api/jmap/generated`](../api/jmap/generated/). Outputs include:
  - Go types for all request/response schemas
  - Server interfaces (`Handler` per operation group) that the application implements
  - Client interfaces for calling other Emissary instances
  - Request decoders, response encoders, validators, router, middleware
- **Frontend (TypeScript)**: `orval` (a declared dev dependency in [`frontend/package.json`](../frontend/package.json), invoked via `npm run
  orval`) targeting [`frontend/src/api/generated`](../frontend/src/api/generated/). Outputs include:
  - TypeScript interfaces for all request/response bodies and params
  - A fully typed fetch client with method signatures matching the OpenAPI `operationId`s
  - Zod schemas or equivalent for runtime validation if desired

The generated frontend client is imported by [`frontend/src/api.ts`](../frontend/src/api.ts), which becomes a thin wrapper for auth token
handling, and the generated types replace the manual ones in `frontend/src/types.ts`. JMAP dispatch in [`mailer/jmap/`](../mailer/jmap/) uses
generated wire types without a generated server stub ([API.md](API.md#generated-api-artifacts)).

The generated trees (`api/*/generated/` and `frontend/src/api/generated/`) are committed. After changing a spec or a generator version, run
`./run.sh generate:api` and commit the regenerated files along with the change. `./run.sh check:generated` regenerates and fails if those trees
would change; CI runs it as a separate job on every push and pull request so stale generated code cannot be merged.

## Test

The full testing strategy — layers, test doubles, conventions and the invariants that must stay covered — is documented in
[TESTING.md](TESTING.md).

`./run.sh test` builds the frontend (see [Build](#build)), then runs:

- `go test ./...` in [`mailer/`](../mailer/) — unit tests are colocated with the packages they test (`handler`, `auth`, `usermanager`, `storage`,
  `sender`, `metadata`, `email`, `config`, `cmd/frontend`, ...). Backend tests use small fakes or in-memory implementations, so they do not call
  AWS ([TESTING.md](TESTING.md#layers)).
- `npm run lint` in [`frontend/`](../frontend/) — oxlint. `no-alert` is an error, so a new `alert()`, `confirm()` or `prompt()` call fails CI
  (see [DESIGN.md](DESIGN.md#frontend-ui-conventions)).
- `npm run test` in [`frontend/`](../frontend/) — vitest (`*.test.{ts,tsx}`). The shared setup makes any surviving native browser dialog throw.

CI (`.github/workflows/ci.yml`) runs `./run.sh test` and `./run.sh spectest` on every push and pull request, plus the separate generated-code-freshness
check (`./run.sh check:generated`). Tagging `v*` triggers the release workflow, which runs the same tests before `./run.sh build` and uploads the
binaries and checksums ([TESTING.md](TESTING.md#running)).

## Run locally

```sh
STORAGE=memory ./run.sh run
```

`run.sh run` builds first, then starts `.build/frontend`. It defaults `USERNAME=Emissary`, generates `PASSWORD` and `JWT_SECRET` when unset
(printed at startup), defaults `LOG_LEVEL=DEBUG`, and sets every selector to a local/no-op value (`memory`, `log`, `none`) when neither selector
nor backend configuration is present. The binary itself never supplies those defaults: starting it directly requires explicit selectors or
exactly one configured backend per component. `S3_BUCKET` must be set explicitly for `STORAGE=s3`. Outbound mail uses the log sender in that
local runner setup (messages appear in stdout only); the From address always comes from the sending user rather than from configuration. For
direct SMTP delivery set `SENDER=mta`; `MTA_PORT=1025` is handy with a local mail sink. See
[CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components) for the selection rules and the full variable list.

For pure UI work, `cd frontend && npm run dev` starts the Vite dev server, but it does not proxy `/api` — end-to-end work happens against the
built binary (see [TESTING.md](TESTING.md#end-to-end-manual)). For a persistent AWS-free run use `STORAGE=filesystem ./run.sh run`
([CONFIGURATION.md](CONFIGURATION.md#aws-and-local-runs)).

## Extending

Each pluggable seam is a small interface plus a selector switch. To add a new implementation, three places change:

1. Implement the interface in its package, next to the existing ones: [`storage.Storage`](../mailer/components/storage/storage.go),
   [`sender.Sender`](../mailer/components/sender/sender.go), [`metadata.MetadataStore`](../mailer/components/metadata/interface.go),
   [`session.SessionStore`](../mailer/components/session/session.go), [`receiver.ConfigStore`](../mailer/receiver/receiver_config_store.go) or
   [`usermanager.UserManager`](../mailer/components/usermanager/usermanager.go). Metadata `Get` returns an empty record for an unknown id (never
   a fabricated receive time), keywords are a JMAP `String[Boolean]` map, and `receivedAt` is stamped by the server at creation and never
   accepted from JMAP clients ([API.md](API.md#received-time)).
2. Register it in [`mailer/config-vendor`](../mailer/config-vendor/vendor.go), the single place that vends each component from its config subtype
   (`configvendor.Storage`, `.Sender`, `.Metadata`, `.Session`, `.UserManager`, `.ReceiverConfigStore`). Both
   [`cmd/frontend`](../mailer/cmd/frontend/) and [`cmd/s3-receiver`](../mailer/cmd/s3-receiver/) use `configvendor.FromConfig`, while
   `handler.New` calls the same per-subtype functions for callers that supply only a selector. `metadata.NewStore`, `usermanager.New` and
   `receiver.NewConfigStore` remain the low-level selectors those functions delegate to.
3. Teach [`mailer/config`](../mailer/config/config.go) to validate the new selector value and any required companion variables (see
   [CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components)).

Add a colocated unit test exercising the new implementation through the interface; the existing `*_test.go` files show the patterns
([TESTING.md](TESTING.md#conventions-for-new-tests)).

Currently open implementation work:

- the `usermanager` `ldap` backend (registered, panics when reached);
- bounce handling, e.g. reacting to SES receipt verdicts.
