# Testing

How Emissary is tested: the strategy, the layers, the test doubles, and the invariants every change is expected to keep green. Commands live in
[DEVELOPMENT.md](DEVELOPMENT.md#test); this document explains the reasoning behind them.

## Strategy

1. **Hermetic by default.** `go test` and vitest never talk to AWS or the network and depend on no wall clock beyond explicit fixtures. The whole
   suite runs in seconds on a laptop and identically in CI.
2. **Test through the seams.** The pluggable interfaces ([`storage.Storage`](../mailer/components/storage/storage.go),
   [`sender.Sender`](../mailer/components/sender/sender.go), [`metadata.MetadataStore`](../mailer/components/metadata/interface.go),
   [`session.SessionStore`](../mailer/components/session/session.go), [`receiver.ConfigStore`](../mailer/receiver/receiver_config_store.go),
   [`usermanager.UserManager`](../mailer/components/usermanager/usermanager.go)) exist as much for testing as for configuration. Tests wire the
   real composition root (`handler.New`) over in-memory implementations, so only the AWS client differs from a deployment
   ([ARCHITECTURE.md](ARCHITECTURE.md)).
3. **Test at the highest layer that can fail.** API behavior drives the finished `http.Handler` with `httptest` — routing, middleware, JSON and
   visibility included — rather than unit-testing handlers in isolation. Pure functions (MIME parsing, JWT, config) are tested directly.
4. **Assert behavior, not structure.** Tests assert status codes, JSON payloads, JWT claims and the exact S3 keys requested, so internal
   refactors do not churn the suite ([API.md](API.md)).
5. **One contract per seam.** `memory`/`file`/`s3` storage, `memory`/`file`/`ddb` metadata, `memory`/`ddb` sessions and
   `none`/`filesystem`/`s3`/`ddb` user managers are each tested against the same interface expectations, including not-found conventions and
   listing order; see the shared [storage](../mailer/components/storage/contract_test.go),
   [metadata](../mailer/components/metadata/contract_test.go) and [user-manager](../mailer/components/usermanager/authpolicy_test.go) contract
   suites.
6. **A bug fix lands with its failing test**; CI runs the suite on every push and pull request (see [Running](#running)).

## Layers

| Layer | Tool | Where | Scope |
| --- | --- | --- | --- |
| Pure units | `go test` | [`email`](../mailer/email/), [`auth`](../mailer/auth/), [`config`](../mailer/config/) | RFC 5322 parsing, JWT, env defaults and validation |
| Seam implementations | `go test` | [`components/`](../mailer/components/), [`receiver/`](../mailer/receiver/) | each backend behind its interface |
| HTTP handler | `go test` + `httptest` | [`handler`](../mailer/handler/), [`jmap`](../mailer/jmap/), [`cmd/frontend`](../mailer/cmd/frontend/) | whole API: routing, auth, visibility, sends |
| Frontend | vitest + jsdom | [`frontend/src`](../frontend/src/) | API client, rendering/routing, in-app modals |
| End-to-end | manual | `STORAGE=memory ./run.sh run` | real binary with embedded UI in a browser |

### Pure units

[`email`](../mailer/email/email_test.go) parses the wire format and simplified model — multipart, nested multipart, base64, quoted-printable,
ISO-8859-1, missing headers, round trips — and formats delivered-key timestamps. [`auth`](../mailer/auth/auth_test.go) covers token
issue/validate, expiry and future tokens, audience/issuer mismatch, constant-time password compare, refresh/logout and the injectable time
source. [`config`](../mailer/config/config_test.go) covers backend selection (explicit selectors, unique inferred configuration, missing and
ambiguous configuration) and validation of every selector value and its companion variables, table-driven with `t.Run` subtests.

### Seam implementations

[`storage`](../mailer/components/storage/) tests each implementation: memory directly (concurrency, clone isolation in
[`memory_test.go`](../mailer/components/storage/memory_test.go)), file (persistence, traversal rejection in
[`file_test.go`](../mailer/components/storage/file_test.go)), and S3 through a fake client
([`s3_test.go`](../mailer/components/storage/s3_test.go)). The shared [`contract_test.go`](../mailer/components/storage/contract_test.go) checks
listings, revisions, blobs and submissions through the interface. [`metadata`](../mailer/components/metadata/) covers memory, file (atomic
writes, corrupt JSON, directory creation) and ddb (composite keys, GSI pagination) in
[`metadata_test.go`](../mailer/components/metadata/metadata_test.go) and [`ddb_test.go`](../mailer/components/metadata/ddb_test.go), plus a
concurrency test; [`session`](../mailer/components/session/session_test.go) covers composite keys, expiry and per-user deletion.
[`usermanager`](../mailer/components/usermanager/) covers `none`, the management policy, `ddb`, `s3` and `file` managers through fakes or temp
directories, and that `ldap` is selectable but panics with `"unimplemented"`
([`usermanager_test.go`](../mailer/components/usermanager/usermanager_test.go)). [`sender`](../mailer/components/sender/) covers the log sender,
the in-memory recording sender, and the MTA sender end to end over a `net.Pipe` SMTP session — MX preference/failover, recipient grouping, Bcc
envelope handling, bounces — plus DMARC/SPF alignment with mox's mock DNS ([`mta_test.go`](../mailer/components/sender/mta_test.go)); SES
envelope rules are in [`aws-ses_test.go`](../mailer/components/sender/aws-ses_test.go). [`receiver`](../mailer/receiver/) covers delivery,
routing rules, dedup, vacation replies and retries.

### HTTP handler

[`handler`](../mailer/handler/handler_test.go) and [`cmd/frontend`](../mailer/cmd/frontend/main_test.go) build the real handler via
`New(HandlerConfig)` with memory storage and a capturing sender, then drive it with `httptest`: login, authenticated requests, listing, reading,
flagging, deleting and sending. JMAP method behavior is pinned by protocol-level suites in [`jmap`](../mailer/jmap/) —
[`handler_test.go`](../mailer/jmap/handler_test.go), [`dispatcher_test.go`](../mailer/jmap/dispatcher_test.go),
[`resultrefs_test.go`](../mailer/jmap/resultrefs_test.go) — and [conformance tests](../mailer/handler/jmap_conformance_rfc_test.go). This is
where the authorization model is pinned ([AUTH.md](AUTH.md), [API.md](API.md#jmap-mail-operations)).

### Frontend

Vitest with jsdom (see [`vitest.config.ts`](../frontend/vitest.config.ts); shared setup in [`test-setup.ts`](../frontend/src/test-setup.ts)).
[`jmap-client.test.ts`](../frontend/src/api/jmap-client.test.ts) stubs `fetch` with `vi.stubGlobal` and checks token storage, headers and error paths.
[`App.test.tsx`](../frontend/src/App.test.tsx) renders the app with `createRoot` inside `act()` and checks the stored-token → inbox flow (auth
state derives from the JWT in localStorage; there is no server-side auth check call).
[`ConfirmDialog.test.tsx`](../frontend/src/components/ConfirmDialog.test.tsx) covers cancel, backdrop click, Escape, confirm and busy state;
[`DeleteConfirmation.test.tsx`](../frontend/src/components/DeleteConfirmation.test.tsx) drives inbox, message-view and user-management Delete
buttons through the same modal. Other component suites live beside their sources in [`frontend/src/components/`](../frontend/src/components/).
The shared setup replaces `window.alert`, `window.confirm` and `window.prompt` with throwing stubs, so native dialogs fail immediately.

### End-to-end (manual)

`STORAGE=memory ./run.sh run` starts the real binary with embedded frontend and log sender — an AWS-free smoke of the whole product (see
[DEVELOPMENT.md](DEVELOPMENT.md#run-locally)). There is no automated browser test today.

## Test doubles

- **In-memory implementations are production code**, shipped for AWS-free runs and reused as test doubles; no parallel mocks to drift out of
  sync. The sole test-only exception is `sender.InMemoryRecording`, whose `Sent`/`Bounces` slices are asserted by handler, JMAP and receiver
  tests.
- **AWS SDK clients are hand-written fakes** implementing the same method set (`fakeS3` in
  [`s3_test.go`](../mailer/components/storage/s3_test.go), DDB fakes in [`ddb_test.go`](../mailer/components/metadata/ddb_test.go) and
  [`session_test.go`](../mailer/components/session/session_test.go)) and returning real SDK error types (`types.NoSuchKey`, ...). They record
  requests so tests can assert wire-level behavior such as fetching delivered messages by full `.eml` keys while legacy SES staging keys resolve
  without the suffix.
- **No mocking framework**: standard `testing`, plain structs and table-driven subtests throughout; frontend tests stub `fetch` with
  `vi.stubGlobal`.
- **Sentinel errors** (`storage.ErrNotFound`, `usermanager.ErrInvalidCredentials`) are checked with `errors.Is`, never by string.
- **Deterministic fixtures**: literal messages and fixed `Date` headers, so failures are reproducible.

## Protected invariants

Security- and contract-critical behavior must keep coverage; if you change one of these, change (or deliberately keep) the test with it, and keep
this table in sync when renaming tests. Links point to the suite where each invariant is pinned.

| Invariant | Pinned by (package / test) |
| --- | --- |
| Ordinary users read only their own account's mailbox | [`handler` `TestUserReadsOnlyOwnMailbox`](../mailer/handler/handler_test.go); [`jmap` `TestHandler_EmailQueryScopesToAccountAndMailbox`](../mailer/jmap/handler_test.go) |
| Cross-account mail is absent from queries and `notFound` in `Email/get` (no existence leak) | [`jmap` `TestHandler_EmailQueryScopesToAccountAndMailbox`](../mailer/jmap/handler_test.go) |
| A user without User Manager permission cannot access another `accountId` | [`handler` `TestNonAdminCannotReadAnotherAccount`](../mailer/handler/handler_test.go) |
| `EmissaryUserManagement` permits masquerade via the target `accountId`; `systemManager` alone is not enough | [`handler` `TestUserManagerMasqueradesViaAccountID`](../mailer/handler/handler_test.go), [`TestUserManagerClaimAllowsMasqueradeWithoutSystemManagerClaim`](../mailer/handler/handler_test.go) |
| JWT `sub` is the user id (email address); `systemManager: true` only for the built-in admin/System Managers; no `admin` claim | [`handler` `TestJWTIncludesUserIDAndSystemManagerOnlyForAdmin`](../mailer/handler/handler_test.go); [`auth` tests](../mailer/auth/auth_test.go) |
| Blank `PASSWORD` disables admin login without affecting other managers | [`handler` `TestBlankAdminPasswordDisablesAdminLogin`](../mailer/handler/handler_test.go); [`usermanager` tests](../mailer/components/usermanager/usermanager_test.go) |
| A user created with a blank password stores no salt and cannot log in | [`handler` `TestCreateUserWithoutPassword`](../mailer/handler/users_test.go), [`TestNoPasswordUserOverHTTP`](../mailer/handler/users_test.go); [`usermanager` `Test*NoPasswordUser`](../mailer/components/usermanager/nopassword_test.go); frontend [`UsersCreate.test.tsx`](../frontend/src/components/UsersCreate.test.tsx) |
| Ordinary users send only from their own address; masquerade permission allows another From; replies default to the original recipient; masqueraded Reply-To must match From | [`handler` `TestSendEmailFromRules`](../mailer/handler/handler_test.go), [`TestSendEmailFromVerification`](../mailer/handler/handler_test.go); [`jmap` `TestEmailSubmissionSetMasqueradeReplyTo`](../mailer/jmap/handler_test.go), [`TestEmailSetStoresReplyTo`](../mailer/jmap/handler_test.go) |
| JMAP creation references (`#id`) resolve across batches, including seeded `createdIds` echoed only when requested | [`handler` `TestSendEmailWithCreationReference`](../mailer/handler/handler_test.go), [`TestJMAPCreatedIDsOnlyWhenRequested`](../mailer/handler/handler_test.go); [`jmap` `TestResolveCreationIDs`](../mailer/jmap/dispatcher_test.go), [`TestDispatchRequestResolvesCreationReferences`](../mailer/jmap/dispatcher_test.go), [`TestDispatchRequestCreatedIDsOnlyWhenRequested`](../mailer/jmap/dispatcher_test.go) |
| RFC 8620 result references (`#ids`) resolve JSON pointers from earlier responses, apply before creation references, and reject bad references with `invalidResultReference` (or `invalidArguments` for `ids` + `#ids`) | [`jmap` `TestDispatchRequestResolvesResultReference`](../mailer/jmap/resultrefs_test.go), [`TestDispatchRequestResolvesResultReferenceBeforeCreationID`](../mailer/jmap/resultrefs_test.go), [`TestDispatchRequestInvalidResultReferences`](../mailer/jmap/resultrefs_test.go), [`TestDispatchRequestDuplicateReferencedArgumentReturnsInvalidArguments`](../mailer/jmap/resultrefs_test.go), [`TestResolveJSONPointer`](../mailer/jmap/resultrefs_test.go) |
| Drafts and creation ids use durable per-session storage, and the draft is deleted after a successful send | [`storage` `TestS3StorageSessionDraftsAndCreatedIDs`](../mailer/components/storage/s3_test.go), [`TestS3StorageSessionKeysRejectTraversal`](../mailer/components/storage/s3_test.go); [`jmap` `TestDispatchRequestPersistsCreatedIDsPerSession`](../mailer/jmap/dispatcher_test.go); [`handler` `TestSendEmailWithCreationReference`](../mailer/handler/handler_test.go) |
| Deleting an email also removes its metadata | [`handler` `TestDeleteEmailAlsoDeletesMetadata`](../mailer/handler/handler_test.go) |
| A `/set` advances state from post-mutation data even when an earlier call in the batch cached the type's records; `/changes` then reports the update | [`mailbox` `TestMailboxSet_AdvancesCachedState_RFC8621_2_5`](../mailer/jmap/mailbox/methods_test.go); [`handler` `TestMailboxSet_StateAdvancesAfterCachedRead_RFC8621_2_5`](../mailer/handler/jmap_conformance_rfc_test.go) |
| Metadata stamps a server-owned `receivedAt` at creation, persists it, and never fabricates one for missing records | [`metadata` `TestNewEmailMetadataStampsReceivedAt`](../mailer/components/metadata/metadata_test.go), [`TestEmptyEmailMetadataHasNoReceivedAt`](../mailer/components/metadata/metadata_test.go), [`TestFileMetadataPersistsReceivedAt`](../mailer/components/metadata/metadata_test.go); [`receiver` `TestProcessMessageCreatesMetadataWithStorageID`](../mailer/receiver/receiver_test.go) |
| JMAP `receivedAt` comes from metadata, falls back to `Date` for legacy records, and is not client-assignable or editable | [`jmap` `TestEmailGetUsesStoredReceivedAt`](../mailer/jmap/handler_test.go), [`TestEmailGetMissingMetadataDoesNotFail`](../mailer/jmap/handler_test.go), [`TestEmailSetCreateIgnoresClientReceivedAt`](../mailer/jmap/handler_test.go), [`TestApplyKeywordPatch`](../mailer/jmap/handler_test.go), [`TestEmailQuerySortsByStoredReceivedAt`](../mailer/jmap/handler_test.go) |
| Expired, future and wrong-audience/issuer tokens are rejected | [`auth` `TestAuthenticator_Validate_*`](../mailer/auth/auth_test.go) |
| Delivered keys (timestamp + `.eml`) encode as URL-safe JMAP ids; account/mailbox derive into an `email.EmailRef` | [`email` `TestMailboxFromID`](../mailer/email/email_test.go), [`TestEmailRefFromID_DerivesAccountAndMailboxFromDeliveredKey`](../mailer/email/email_test.go); [`storage` `TestS3StorageListEmailsUsesMailboxPrefixAndFullKey`](../mailer/components/storage/s3_test.go) |
| Delivered keys use ISO 8601 basic UTC timestamps (`YYYYMMDDTHHMMSS`), including when `Date` is missing | [`email` `TestFormatDeliveredTimestamp`](../mailer/email/email_test.go); [`storage` `TestS3StorageAddEmailStoresInInboxAndReturnsToken`](../mailer/components/storage/s3_test.go), [`TestS3StorageAddEmailHonorsConfiguredPrefix`](../mailer/components/storage/s3_test.go) |
| S3 `ListEmails` is scoped to the account/mailbox prefix, skips non-`.eml` system files, and legacy SES keys still resolve | [`storage` `TestS3StorageListEmailsSkipsSystemFilesAndNonEmlObjects`](../mailer/components/storage/s3_test.go), [`TestS3StorageGetEmailReadsDeliveredKey`](../mailer/components/storage/s3_test.go) |
| `AddEmail` generates the id and stores mail in the inbox; the receiver keys metadata from that id | [`storage` `TestMemoryStorage_AddEmailGeneratesIDAndUsesInbox`](../mailer/components/storage/memory_test.go), [`TestS3StorageAddEmailStoresInInboxAndReturnsToken`](../mailer/components/storage/s3_test.go); [`receiver` `TestProcessMessageCreatesMetadataWithStorageID`](../mailer/receiver/receiver_test.go) |
| The receiver delivers through configured storage and requires it; staging deletion remains S3-only | [`receiver` `TestProcessMessageDeliversThroughConfiguredStorage`](../mailer/receiver/receiver_test.go), [`TestNewReceiverRequiresStorage`](../mailer/receiver/receiver_test.go) |
| Unknown selector values and missing companion variables fail at startup | [`config` `TestLoad_UnknownStorage`](../mailer/config/config_test.go), [`TestLoad_S3StorageRequiresBucket`](../mailer/config/config_test.go), [`TestLoad_ExplicitDDBSelectorRequiresTablePrefix`](../mailer/config/config_test.go) |
| Frontend uses no native browser dialogs; destructive actions require in-app confirmation | [`.oxlintrc.json`](../frontend/.oxlintrc.json) `no-alert` (CI via `run.sh test`), throwing stubs in [`src/test-setup.ts`](../frontend/src/test-setup.ts), [`ConfirmDialog`](../frontend/src/components/ConfirmDialog.test.tsx) and [`DeleteConfirmation`](../frontend/src/components/DeleteConfirmation.test.tsx) tests |

## Running

- `./run.sh test` — builds the frontend (the `//go:embed all:frontend/dist` directive in [`mailer/gui`](../mailer/gui/gui.go) makes `dist/` a
  prerequisite of any `go build`/`go test`), runs `npm run lint` (oxlint), then `go test ./...` in `mailer/` and `npm run test` (vitest) in
  `frontend/`.
- Inner loop, with `dist/` already built: `cd mailer && go test ./...` for everything; `go test ./handler/...` or `-run TestName` for one package
  or test; `cd frontend && npm run test`, `npm run test -- <file>` for one file, or `npm run test:watch` while iterating (no `npx`; see
  [DEVELOPMENT.md](DEVELOPMENT.md#no-npx)).
- `./run.sh check:generated` — regenerates API code and fails if the committed trees (`api/*/generated/`, `frontend/src/api/generated/`) would
  change; run it after editing a spec or bumping a generator ([API.md](API.md#generated-api-artifacts)).
- CI (`.github/workflows/ci.yml`) runs `./run.sh test` and `./run.sh spectest` in its test job, plus `./run.sh check:generated` in a separate
  job, on every push to any branch and on every pull request. The release workflow (`.github/workflows/release.yml`) runs `./run.sh test` and
  `./run.sh spectest` on `v*` tags before building binaries, so both workflows validate a commit. Both upload the conformance report as a
  workflow artifact.

### External JMAP conformance suite

`./run.sh spectest` is the conformance gate, linked from the [conformance claim in API.md](API.md#jmap-conformance-claim). It runs locally and in
both CI and the release workflow. It clones `jmapio/jmap-test-suite` pinned to a specific commit, applies the advertised-profile adaptations in
[`jmap-test-suite.patch`](../scripts/spectest/jmap-test-suite.patch), builds a throwaway local server with bearer auth and two real users, runs
the suite's `core`, `binary`, `push`, `email`, `mailbox`, `thread`, `identity`, `submission`, `vacation` and `search-snippet` categories, and
writes a report to `${SPECTEST_REPORT}` (default `${TMPDIR}/emissary-spectest/report.json`). Its JSON, config and report helpers live in
[`mailer/cmd/tools`](../mailer/cmd/tools) and are invoked with `go run`. It needs `git`, `curl`, Go, `npm` and `node` on the PATH and network access
for the initial clone. Credentials are generated per run unless supplied via `SPECTEST_USERNAME`,
`SPECTEST_PASSWORD`, `SPECTEST_JWT_SECRET`, `SPECTEST_SECONDARY_USERNAME` or `SPECTEST_SECONDARY_PASSWORD`; the script embeds none.

The gate passes only when every **required** test passes. Any skip is a consequence of the limits advertised in Session (for example
`maxMailboxDepth=1`, `maxMailboxesPerEmail=1`, `emailQuerySortOptions=["receivedAt"]`, `maxDelayedSend=0`) or the single-account-per-user model;
the patch records the reason beside each adapted test. Genuine defects found by the suite are fixed and covered by protocol-level HTTP tests
named `Test<Type>_<Behavior>_RFC<section>` in [`jmap_conformance_rfc_test.go`](../mailer/handler/jmap_conformance_rfc_test.go).

## Conventions for new tests

**Go**: tests live next to the code, in the same package (`handler_test.go`), one `*_test.go` per area; helpers call `t.Helper()`, use `t.Fatalf`
when the test cannot continue and `t.Errorf` for assertion failures. Prefer `httptest` through `http.Handler` for anything reachable over HTTP
([API.md](API.md#endpoints)). A new seam implementation gets tested through the interface next to existing ones, covering `ErrNotFound` (or the
package's not-found convention) at minimum; if backed by AWS, write a fake of the SDK client rather than skipping.

**Frontend**: one `*.test.{ts,tsx}` per source file, colocated under [`frontend/src`](../frontend/src/); stub `fetch` per test with
`vi.stubGlobal('fetch', ...)` and `vi.unstubAllGlobals()` in `afterEach`; clear `localStorage` in `beforeEach`; wrap renders and state updates in
`act()` (configured by [`test-setup.ts`](../frontend/src/test-setup.ts)). Never call `alert()`, `confirm()` or `prompt()` — the shared setup
makes them throw and oxlint reports them as errors; use [`ConfirmDialog`](../frontend/src/components/ConfirmDialog.tsx)
([DESIGN.md](DESIGN.md#frontend-ui-conventions)).

## Gaps and direction

- [`deploy/`](../deploy/) (CDK) has no tests; Jest and ts-jest are already in `devDependencies`, so synthesizing stacks and asserting on
  resources (receipt rule recipients, bucket policy, least-privilege IAM) is the natural first step. `aws-cdk-local` is installed if
  LocalStack-style checks are wanted ([DEPLOYMENT.md](DEPLOYMENT.md#the-three-stacks)).
- Untouched packages: `envutil`, `awsutil`, `gui` have no tests.
- No integration tests against real AWS services — S3, DynamoDB and SES are covered only through fakes. Prefer an opt-in suite behind a build tag
  (e.g. `//go:build integration`) to keep the default suite hermetic.
- No automated browser/E2E tests for the SPA; the manual `run.sh run` smoke is the stopgap.
- Coverage is not measured in CI; if added, treat it as information, not a gate.
