# Testing

How Emissary is tested: the strategy, the layers, the test doubles,
and the invariants every change is expected to keep green. The
commands themselves live in [DEVELOPMENT.md](DEVELOPMENT.md#test);
this document explains the reasoning behind them.

## Strategy

1. **Hermetic by default.** `go test` and `vitest` never talk to AWS
   or the network and depend on no wall clock beyond explicit
   fixtures. The whole suite runs in seconds on a laptop and
   identically in CI.
2. **Test through the seams.** The pluggable interfaces
   (`storage.Storage`, `sender.Sender`, `metadata.MetadataStore`,
   `usermanager.Manager`) exist as much for testing as for
   configuration. Tests wire the real composition root (`handler.New`)
   over the in-memory implementations, so the only layer that can
   differ between the test bed and a deployment is the AWS client
   itself.
3. **Test at the highest layer that can fail.** API behavior is
   tested by driving the finished `http.Handler` with `httptest`
   requests — routing, auth middleware, JSON and visibility rules
   included — rather than unit-testing handlers in isolation. Genuinely
   pure functions (MIME parsing, JWT, config) are tested directly.
4. **Assert behavior, not structure.** Tests assert status codes,
   JSON payloads, JWT claims and the exact S3 keys a client requests,
   so internal refactors do not churn the suite.
5. **Every implementation of a seam passes the same contract.**
   `memory` and `s3` storage, `memory` and `file` metadata, `none` and
   `any` user managers are each tested against the expectations the
   rest of the system has of their interface (e.g. the shared
   `ErrNotFound` convention).
6. **A bug fix lands with the test that failed first.** The suite is
   the definition of done; CI runs it on every push and pull request.

## Layers

| Layer | Tool | Where | Scope |
| --- | --- | --- | --- |
| Pure units | `go test` | `mailer/email`, `mailer/auth`, `mailer/config` | RFC 5322 parsing, JWT issue/validate, env-var defaults and validation |
| Seam implementations | `go test` | `mailer/storage`, `mailer/metadata`, `mailer/usermanager`, `mailer/sender` | each backend behind its interface |
| HTTP handler | `go test` + `httptest` | `mailer/handler`, `mailer/cmd/frontend`, `mailer/server` | the whole API: routing, middleware, auth, visibility, sends |
| Frontend | vitest + jsdom | `frontend/src/*.test.{ts,tsx}` | API client, app rendering/routing, and in-app confirmation modals |
| End-to-end | manual | `STORAGE=memory ./run.sh run` | the real binary with the embedded UI, exercised in a browser |

### Pure units

`email` parses the wire format and the simplified model — multipart,
nested multipart, base64, quoted-printable, ISO-8859-1, missing
headers, round trips. `auth` covers token issue/validate, expiry and
future tokens, audience/issuer mismatch, constant-time password
compare and the injectable time source. `config` covers defaults and
the validation of every selector value and its companion variables,
table-driven with `t.Run` subtests.

### Seam implementations

`storage` tests both implementations: memory directly (including
concurrency and clone isolation), S3 through a fake client.
`metadata` likewise covers memory and file (atomic writes, corrupt
JSON, directory creation) plus a concurrency test. `usermanager`
covers `none`, `any` and `ddb` (through a fake DynamoDB client), and
that `ldap` constructs and selects but panics with `"unimplemented"`
for ordinary users.
`sender` covers the log sender; the SES sender is only constructed.

### HTTP handler

`mailer/handler` and `mailer/cmd/frontend` build the real handler via
`New(HandlerConfig)` with `MemoryStorage` and a capturing sender,
then drive it with `httptest`: login, authenticated requests, listing,
reading, flagging, deleting and sending. This is where the
authorization model is pinned. `mailer/server` is the older, unused
server package; its tests use hand-rolled mocks and keep the old
behavior pinned until the package is removed.

### Frontend

Vitest with jsdom (see `frontend/vitest.config.ts`; shared setup in
`src/test-setup.ts`). `api.test.ts` stubs `fetch` with
`vi.stubGlobal` and checks token storage, headers and error paths.
`App.test.tsx` renders the app with `createRoot` inside `act()` and
checks the stored-token → inbox flow (the auth state is derived from
the JWT in localStorage; there is no server-side auth check call).
`ConfirmDialog.test.tsx` covers cancel, backdrop click, Escape, confirm
and the busy state; `DeleteConfirmation.test.tsx` drives the inbox,
message-view and user-management Delete buttons through the same modal.
The shared setup in `src/test-setup.ts` replaces
`window.alert`, `window.confirm` and `window.prompt` with throwing
stubs, so any native dialog in a test fails immediately.

### End-to-end (manual)

`STORAGE=memory ./run.sh run` starts the real binary with the
embedded frontend and the log sender — an AWS-free smoke of the whole
product (see [DEVELOPMENT.md](DEVELOPMENT.md#run-locally)). There is
no automated browser test today.

## Test doubles

- **In-memory implementations are production code**, shipped for
  AWS-free runs, and reused as test doubles. There is no parallel mock
  of them to drift out of sync.
- **AWS SDK clients are replaced by small hand-written fakes** that
  implement the same method set (`fakeS3` in
  `mailer/storage/s3_test.go`) and return real SDK output/error types
  (`types.NoSuchKey`, ...). The fakes record what was requested so
  tests can assert wire-level behavior — e.g. that delivered messages
  are fetched by their full `.eml` object key, while legacy SES staging
  keys are fetched without an `.eml` suffix.
- **`captureSender`** (in `mailer/handler/handler_test.go`) records
  sent messages so from-address rules can be asserted without SES.
- **No mocking framework** — the standard `testing` package, plain
  structs and table-driven subtests throughout.
- **Sentinel errors** (`storage.ErrNotFound`,
  `usermanager.ErrInvalidCredentials`) are asserted with `errors.Is`,
  never by string.
- **Deterministic fixtures**: messages and dates are literal
  constants (`testMessage` in the storage tests, fixed `Date`
  headers), so failures are reproducible.

## Protected invariants

Security- and contract-critical behavior must keep test coverage; if
you change one of these, change (or deliberately keep) the test with
it. When renaming tests, keep this table in sync.

| Invariant | Pinned by |
| --- | --- |
| Ordinary users read only their own account's mailbox | `handler` `TestUserReadsOnlyOwnMailbox`, `jmap` `TestHandler_EmailQueryScopesToAccountAndMailbox` |
| Cross-account mail is absent from queries and `notFound` in `Email/get` (no existence leak) | `jmap` `TestHandler_EmailQueryScopesToAccountAndMailbox` |
| A non-admin cannot access another `accountId` | `handler` `TestNonAdminCannotReadAnotherAccount` |
| Admins masquerade by supplying the target `accountId` | `handler` `TestAdminMasqueradesViaAccountID` |
| JWT `sub` is the user id (the user's email address); `admin: true` only for the built-in admin | `handler` `TestJWTIncludesUserIDAndAdminOnlyForAdmin`, `auth` tests |
| Blank `PASSWORD` disables admin login without affecting other managers | `handler` `TestBlankAdminPasswordDisablesAdminLogin`, `usermanager` tests |
| Ordinary users always send as themselves; admins as anyone; replies default to the original recipient | `handler` `TestSendEmailFromRules` |
| Deleting an email also removes its metadata | `handler` `TestDeleteEmailAlsoDeletesMetadata` |
| Expired, future and wrong-audience/issuer tokens are rejected | `auth` `TestAuthenticator_Validate_*` |
| Delivered ids are full object keys (timestamp + `.eml`) and account/mailbox are derived from them | `email` `TestMailboxFromID`, `TestParse_DerivesAccountAndMailboxFromDeliveredKey`, `storage` `TestS3StorageListAccountEmails*` |
| S3 `ListAccountEmails` is limited to the account/mailbox prefix; legacy SES staging keys still resolve without an `.eml` suffix | `storage` `TestS3StorageListAccountEmails*`, `TestS3StorageListAllEmailsReadsSESMessageKeys` |
| Unknown selector values and missing companion variables fail at startup | `config` `TestLoad_Unknown*`, `TestLoad_*Requires*` |
| Frontend uses no native browser dialogs; destructive actions require an in-app confirmation | `.oxlintrc.json` `no-alert` (CI via `run.sh test`), throwing stubs in `src/test-setup.ts`, `ConfirmDialog` and `DeleteConfirmation` tests |

## Running

- `./run.sh test` — the full suite: builds the frontend (the
  `//go:embed all:frontend/dist` directive in `mailer/gui` makes
  `dist/` a prerequisite of any `go build`/`go test`), then
  `go test ./...` in `mailer/` and `npm run test` (vitest) in
  `frontend/`.
- Inner loop, with `dist/` already built:
  - `cd mailer && go test ./...` — everything;
  - `go test ./handler/...` or `-run TestName` — one package or test;
  - `cd frontend && npm run test` — vitest once,
    `npm run test -- <file>` for one file, or `npm run test:watch`
    while iterating (no `npx`; see
    [DEVELOPMENT.md](DEVELOPMENT.md#no-npx)).
- CI (`.github/workflows/ci.yml`) runs `./run.sh test` on every push
  to any branch and on every pull request. The release workflow
  (`release.yml`) only builds and uploads binaries on `v*` tags — it
  does not run tests — so treat branch/PR CI as the gate that
  actually validated a commit.

## Conventions for new tests

**Go**

- Tests live next to the code, in the same package
  (`handler_test.go`), one `*_test.go` per area.
- Helpers call `t.Helper()`; use `t.Fatalf` when the test cannot
  continue, `t.Errorf` for assertion failures.
- Prefer `httptest` end-to-end through `http.Handler` for anything
  reachable over HTTP.
- A new implementation of a seam gets tested through the interface,
  next to the existing implementations' tests, covering `ErrNotFound`
  (or the package's not-found convention) at minimum. If it is backed
  by AWS, write a fake of the SDK client rather than skipping.

**Frontend**

- One `*.test.{ts,tsx}` per source file, colocated.
- Stub `fetch` per test with `vi.stubGlobal('fetch', ...)` and call
  `vi.unstubAllGlobals()` in `afterEach`; clear `localStorage` in
  `beforeEach`.
- Wrap renders and state updates in `act()` (the shared setup in
  `src/test-setup.ts` configures the act environment).
- Never call `alert()`, `confirm()` or `prompt()` — the shared setup
  makes them throw and oxlint reports them as errors. Use
  `ConfirmDialog` for user confirmations.

## Gaps and direction

- **`deploy/` (CDK) has no tests.** Jest and ts-jest are already in
  `devDependencies`; the natural first step is synthesizing the stacks
  and asserting on the produced resources (receipt rule recipients,
  bucket policy, least-privilege IAM). `aws-cdk-local` is also
  installed if LocalStack-style checks are wanted.
- **Untouched packages:** `envutil`, `awsutil`, `gui` have no tests;
  `cmd/sns-receiver` is an unimplemented placeholder (see
  [DESIGN.md](DESIGN.md) status notes and `TODO.md`).
- **No integration tests against real AWS services** — S3, DynamoDB
  and SES are covered only through fakes. If real-service coverage
  becomes necessary, prefer an opt-in suite gated behind a build tag
  (e.g. `//go:build integration`) that runs on demand with real
  credentials, keeping the default suite hermetic.
- **No automated browser/E2E tests** for the SPA; the manual
  `run.sh run` smoke is the current stopgap.
- **Coverage is not measured** in CI. If it is added, treat it as
  information for reviewers, not a gate.
