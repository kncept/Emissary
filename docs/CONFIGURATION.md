# Configuration

Most settings are read from environment variables by
[`mailer/config`](../mailer/config/config.go) at startup. `LOG_LEVEL` is the
exception, read by [`mailer/logging`](../mailer/logging/logging.go) before
configuration loads. Values are cached, and any `arn:*:secretsmanager:...`
value is resolved through [`envutil`](../mailer/envutil/envutil.go) and
[`awsutil`](../mailer/awsutil/awsutil.go); other ARNs (for example
`AWS_ROLE_ARN`) are left alone. A failed resolution aborts startup, so an ARN
is never used as if it were the resolved value. Client creation is retried on
the next call, so a transient failure does not poison the process. This is how
the deployed Lambda receives its JWT secret as an ARN.

## Backend selection (all pluggable components)

`STORAGE`, `SENDER`, `RECEIVER`, `METADATA`, `SESSION` and `USER_MANAGER` share one rule, applied by `config.Load` at startup:

1. **An explicitly set selector wins**, whatever other configuration exists.
2. **If the selector is unset and exactly one backend has configuration, use that backend.**
3. **If none has configuration, error.** The local/no-op implementations (`memory`, `log`, `none`) are opt-ins: set `STORAGE=memory`, `SENDER=log`, `RECEIVER=memory`, `METADATA=memory`, `SESSION=memory` or `USER_MANAGER=none` explicitly.
4. **If several backends have configuration, error.** Set the selector to choose one rather than letting startup guess.

What counts as configuration when the selector is unset:

| Component | Backend | Configuration that selects it |
| --- | --- | --- |
| `STORAGE` | `s3` | AWS credentials plus `S3_BUCKET` (S3 settings are static environment values). |
| `STORAGE` | `filesystem` | `FILESYSTEM_DIR` set explicitly; the implicit `~/.emissary` default does not count. |
| `SENDER` | `aws-ses` | AWS credentials from the SDK chain, including IAM roles. |
| `SENDER` | `mta` | `MTA_PORT` set explicitly; its `25` default does not count. |
| `RECEIVER` | `s3` | AWS credentials plus `RECEIVER_BUCKET` or `S3_BUCKET`. |
| `RECEIVER` | `filesystem` | `RECEIVER_FILESYSTEM_DIR` or `FILESYSTEM_DIR` set explicitly. |
| `RECEIVER` | `ddb` | AWS credentials plus an explicit `TABLE_PREFIX`/`DDB_TABLE_PREFIX`. |
| `METADATA` | `ddb` | AWS credentials plus an explicit `TABLE_PREFIX`. |
| `METADATA` | `file` | `METADATA_FILE` or `FILESYSTEM_DIR` set explicitly. |
| `SESSION` | `ddb` | AWS credentials plus an explicit `TABLE_PREFIX`. |
| `USER_MANAGER` | `s3` | AWS credentials plus `S3_BUCKET`. |
| `USER_MANAGER` | `filesystem` | `FILESYSTEM_DIR` set explicitly. |
| `USER_MANAGER` | `ddb` | AWS credentials plus an explicit `TABLE_PREFIX`. |
| `USER_MANAGER` | `ldap` | `LDAP_URL` set explicitly. |

DynamoDB has one configuration input — an explicitly set table prefix — and
table names are fixed suffixes, so there are no per-table overrides and no
default prefix. S3 checks read static environment values only, and the
filesystem check is just the presence of the root variable.

For example, `S3_BUCKET` plus `TABLE_PREFIX` with `USER_MANAGER` unset is an
error, not a silent choice: set `USER_MANAGER=s3` or `ddb`. With no backend
configuration at all, set `USER_MANAGER=none` for administrator-only
operation. An explicit `METADATA=ddb` without `TABLE_PREFIX` is also an error.
See [DESIGN.md](DESIGN.md#how-the-pieces-are-selected) for the interfaces
rather than a repeated description of the seams.

## Server and authentication

| Variable | Default | Description |
| --- | --- | --- |
| `USERNAME` | — (required) | The single built-in administrator's username. |
| `PASSWORD` | unset | Administrator password. An explicitly blank value **disables** administrator login; other user managers keep working. `run.sh run` generates a random password when the variable is *unset*. |
| `JWT_SECRET` | generated | HS256 signing secret. When unset, an ephemeral 32-byte secret is generated and tokens die on restart. May be a Secrets Manager ARN. |
| `JWT_TTL` | `24h` | Legacy single-token lifetime parsed as a Go duration (`30m`, `12h`, ...); access/refresh lifetimes are fixed by [AUTH.md](AUTH.md#the-token-pair). |
| `JWT_AUDIENCE` | unset | Expected `aud` claim; tokens with another audience are rejected. The CDK app sets `mail.<DOMAIN>`. |
| `JWT_ISSUER` | unset | Expected `iss` claim. The CDK app sets `emissary-<domain>`. |
| `LISTEN_ADDR` | — | Listen address, e.g. `:9090` or `127.0.0.1:8080`; takes precedence over `PORT`. |
| `PORT` | `8080` | Listen port when `LISTEN_ADDR` is unset. |
| `MAX_FILESIZE` | `10485760` (10 MiB) | Maximum bytes in one JMAP request body and one MIME message (including decoded parts). A positive integer below 1 TiB; a bad value aborts startup. |

The standalone HTTP server applies bounded read-header, read, write and idle
timeouts and shuts down gracefully on SIGINT/SIGTERM, allowing a bounded
window for in-flight requests to finish.

`MAX_FILESIZE` is enforced by both the frontend and the inbound receiver.
Every HTTP request body is capped at this size; an oversized JMAP batch
returns HTTP 413 with an `application/problem+json` body of type
`urn:ietf:params:jmap:error:limit` and `limit: "maxSizeRequest"`
([API.md](API.md#post-apijmap-jmap-batch-request)). The value is advertised in
the JMAP Session as `maxSizeRequest`, `maxSizeUpload` and
`maxSizeAttachmentsPerEmail`, so conformant clients can avoid sending requests
that would be rejected. See [API.md](API.md#get-well-knownjmap) for the Session
object.

## Logging

All backend logs use Go's `log/slog` with one threshold from `LOG_LEVEL`:

| Variable | Default | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `WARN` when unset (`ERROR` in deployments, `DEBUG` for `run.sh run`) | Minimum level emitted: `DEBUG` (-4), `INFO` (0), `WARN` (4) or `ERROR` (8). Values are case-insensitive and numeric slog values are accepted; unknown values fall back to `WARN`. |

Records go to stderr, which the Lambda runtime forwards to CloudWatch Logs.
`DEBUG` carries per-request and per-message traces, `INFO` records lifecycle
events and successful operations, `WARN` records recoverable or degraded
conditions, and `ERROR` records failed operations that need operator attention.

## Storage (read side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE` | — (see above) | `s3`, `filesystem` or `memory`; `memory` must be set explicitly. |
| `S3_BUCKET` | — (required for `s3`) | Bucket holding raw inbound messages. Objects are read at their exact key (SES writes them at the bucket root); see [S3.md](S3.md#bucket-layout). |
| `FILESYSTEM_DIR` | `~/.emissary` once a filesystem backend is selected | Shared root for filesystem-backed stores; `STORAGE=filesystem` stores mail under `<root>/mail`. Setting it explicitly counts as backend configuration; the implicit default does not. |

## Sender (write side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `SENDER` | — (see above) | `aws-ses` (alias `ses`), `mta` or `log`; `log` must be set explicitly. |
| `MTA_PORT` | `25` | SMTP port for `mta` (e.g. `1025` for a local mail sink). An explicit value counts as `mta` configuration; the default does not. |

Credential detection uses the AWS SDK default chain, so IAM roles (EC2
instance profiles, ECS task roles, web identity/IRSA and the Lambda execution
role) select `aws-ses` even without `AWS_*` credential variables. If
credentials are found and `SENDER` is unset, `aws-ses` is selected. With no
credentials and no explicit `MTA_PORT`, startup errors rather than using the
log sender, which prints messages to stdout and does **not** deliver replies
or bounces. See [ARCHITECTURE.md](ARCHITECTURE.md#outbound-mail-flow) for the
delivery sequence.

There is no configured From address. From is locked to the submitting user's
own address (or the masquerade target's when a User Manager sends on behalf of
another account); a client-supplied From is ignored. With SES the locked
address must be one of the account's verified identities (`ses:ListIdentities`,
covering full addresses and domains), or submission fails with JMAP
`forbidden`. Identity lookups are cached for five minutes, and a failed lookup
fails the submission rather than allowing an unverified sender. Other senders
have no SES identity source and keep only the locked-From rule. Reply-To is
carried on the message and, when masquerading, must match From (it defaults to
From when omitted). `In-Reply-To` and `References` from the draft are written
into the delivered message, and the stored `Message-ID` is reused on the wire.

`SENDER=mta` delivers directly: for each recipient domain it resolves MX
records, connects to the preferred mail exchanger on `MTA_PORT` and speaks
SMTP. MX hosts are tried in preference order, then the domain itself as a last
resort; recipients are grouped by domain, and Bcc addresses appear only in the
SMTP envelope. The envelope sender and EHLO/HELO identity come from the
message's From address. Before delivery the sender checks the From domain
against the envelope (SPF) domain using `github.com/mjl-/mox/dmarc`; a
misaligned domain is logged as a warning but does not stop delivery. The sender
does not authenticate to the MX host and uses STARTTLS when the server advertises it.

## Receiver (inbound routing rules)

| Variable | Default | Description |
| --- | --- | --- |
| `RECEIVER` | — (see above) | Backend for recipient rules: `s3`, `filesystem`, `ddb`/`dynamodb` or `memory`; `memory` must be explicit. |
| `RECEIVER_BUCKET` | `S3_BUCKET` | Rules bucket for `RECEIVER=s3`; set it explicitly for a dedicated bucket. |
| `RECEIVER_FILESYSTEM_DIR` | `FILESYSTEM_DIR` (default `~/.emissary`) | Rules root for `RECEIVER=filesystem`; the store adds `receiver/`. An explicit value counts as filesystem configuration. |
| `RECEIVER_REGION` | `AWS_REGION` | Region for the `ddb` backend; the table is `<TABLE_PREFIX>receiver` and `TABLE_PREFIX` is required. |

Each mail identity is one JSON object under the `receiver/` prefix: `s3://<RECEIVER_BUCKET>/receiver/<identity>.json` or `<RECEIVER_FILESYSTEM_DIR>/receiver/<identity>.json`. The object is a single [`HandlerRules`](../mailer/receiver/receiver_config.go) entry — `{"mailIdentity": "<identity>", "rules": [...]}` — where `mailIdentity` repeats the config key and `rules` is the ordered list tried first-match-wins. With `RECEIVER=ddb`, each identity is one item in `<TABLE_PREFIX>receiver`, keyed by a string partition key `id` (no sort key), with rules in a `rules` list; the identity is not stored twice.

The `_` identity holds the base fallback rules (deliver to an existing user, otherwise bounce). It is always available in memory when the backend has no `_` entry, and identities without their own configuration use it. Loading rules never writes to the store, so `_` is persisted only when a save explicitly includes it. The frontend API and `s3-receiver` both load these rules at startup; the `s3-receiver` Lambda ignores object keys containing `/`, so writes under `receiver/` are never treated as inbound mail or deleted by the message consumer. DynamoDB tables are created at deploy time, without per-table CREATE permissions at runtime, so the table must exist before the app starts.

Allowed rules are `DeliverToUser` (known users only), `DeliverTo:<address>` (a saved user address, for example `DeliverTo:catchall@example.com`; must name an existing user when saved), `DeliverToMailIdentity` (the identity's own mailbox), `Bounce` (standard bounce) and `Drop` (silent discard). Rules are tried in order until one applies; `Bounce` and `Drop` are terminal, so place them after rules that should take precedence. A `DeliverTo` rule whose saved address is no longer a known user falls through to the next rule. No match bounces rather than discarding. See [ARCHITECTURE.md](ARCHITECTURE.md#inbound-mail-flow) for routing and [SUPPORT.md](SUPPORT.md#receiver-rules-editor) for the editor and save-time validation.

## Metadata (JMAP keywords, received time, mailbox and subject)

| Variable | Default | Description |
| --- | --- | --- |
| `METADATA` | — (see above) | `memory`, `file` or `ddb` (alias `dynamodb`); `memory` must be explicit. |
| `METADATA_FILE` | `<FILESYSTEM_DIR>/metadata` | For `file`: one `<email id>.json` record per message; an explicit path ending in `.json` keeps the older single-file layout. |
| `METADATA_REGION` | AWS region | Region for `<TABLE_PREFIX>metadata` (partition key `id`, sort key `userId`, `userId-index` GSI). |

These settings are shared by the frontend API and the `s3-receiver` delivery consumer: delivery creates a record for each delivered message with a server-stamped `receivedAt` (RFC 3339 UTC), the destination mailbox (`inbox`) and the decoded subject, with no keywords. JMAP reads and updates keywords and rewrites `mailbox` whenever an Email/set moves the message. `receivedAt` is never accepted from clients. Use `file` or `ddb` in production so both processes see the same state; a message with no record means empty keywords and `receivedAt` falling back to the message `Date` header. See [API.md](API.md#received-time) for the JMAP view.

## Sessions (refresh tokens)

| Variable | Default | Description |
| --- | --- | --- |
| `SESSION` | — (see above) | `memory`, `ddb` or `dynamodb`; `memory` must be explicit. |
| `SESSION_REGION` | AWS region | Region for `<TABLE_PREFIX>sessions` (partition key `id`, sort key `userEmail`). |

## User manager (login for ordinary users)

| Variable | Default | Description |
| --- | --- | --- |
| `USER_MANAGER` | — (see above) | `none`, `ddb`, `s3`, `filesystem` or `ldap`; `none` must be explicit. |
| `DOMAIN` | — | Receiving domain used for delivery, bounce addresses and CDK. |
| `USER_REGION` | AWS region | Region for the user table (and the `s3` backend's S3 client); `<TABLE_PREFIX>users` for `ddb`. |
| `LDAP_URL` | — | LDAP server URL; required by the reserved, unimplemented `ldap` backend. |
| `LDAP_BIND_DN` / `LDAP_BIND_PASSWORD` | — | Bind credentials for the `ldap` backend. |
| `LDAP_BASE_DN` | — | Search base for the `ldap` backend. |
| `LDAP_USER_FILTER` | — | User search filter for the `ldap` backend. |
| `LDAP_CACHE_TTL` | — | Cache TTL for the `ldap` backend. |

The `s3` backend stores each user as `users/<email>/user.json` in the mail bucket and requires `S3_BUCKET`; `filesystem` writes the same record to `<FILESYSTEM_DIR>/users/<email>/user.json`. See [USERS.md](USERS.md) for account behavior.

## Filesystem backend layout

With `FILESYSTEM_DIR=~/.emissary` (the default) the filesystem-backed stores share one root:

| Store | Directory | Contents |
| --- | --- | --- |
| `STORAGE=filesystem` | `~/.emissary/mail` | `users/<account>/<mailbox>/<timestamp>_<id>.eml`, `_storage/<session>/` drafts and creation ids, and `_jmap-state/<base64url(account)>/state.json` JMAP counters/change logs/query snapshots. |
| `USER_MANAGER=filesystem` | `~/.emissary/users` | `<email>/user.json` records, the same JSON shape as the S3 manager. |
| `METADATA=file` | `~/.emissary/metadata` | One `<email id>.json` record per message. |
| `RECEIVER=filesystem` | `~/.emissary/receiver` | One `<identity>.json` routing config per mail identity, including `_` for the base fallback; `RECEIVER_FILESYSTEM_DIR` overrides the root independently. |

All four expand a leading `~` in their configured root to the home directory and create their directory on first use. Ids are validated before being mapped to paths, so JMAP ids cannot escape these roots. See [ARCHITECTURE.md](ARCHITECTURE.md#request-handling-details) for the state and scratch-file conventions.

## DynamoDB table naming

| Variable | Default | Description |
| --- | --- | --- |
| `TABLE_PREFIX` | — (required for any DDB backend) | Prefix applied to the fixed table names. `DDB_TABLE_PREFIX` is accepted as an alias. No default is supplied by the application or the CDK deployment. |

The resolved name is `<prefix><name>` when the prefix already ends in `-` or `_`, otherwise `<prefix>-<name>`. The suffixes are `metadata` (JMAP keywords and `receivedAt`), `sessions` (refresh tokens), `users` (the `ddb` user manager) and `receiver` (inbound rules). There are no per-table overrides: `DDB_TABLE`, `SESSION_TABLE`, `USER_TABLE` and `RECEIVER_TABLE` are ignored. If `TABLE_PREFIX` is unset, any DDB backend is incomplete configuration and startup fails.

## AWS and local runs

| Variable | Description |
| --- | --- |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | Default region for DynamoDB and the SDK config chain. |
| AWS credentials | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, a web identity token (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`) or ECS container credentials. They count as configuration for AWS-backed components once their other required values are set; see [Backend selection](#backend-selection-all-pluggable-components). |
| `AWS_LAMBDA_FUNCTION_NAME` | Presence switches `cmd/frontend` into Lambda mode (set by the runtime). |

`./run.sh run` builds everything and starts `.build/frontend` with these behaviours on top of the tables above:

- `USERNAME` defaults to `Emissary`.
- A random `PASSWORD` is generated when the variable is **unset**; setting it explicitly blank disables admin login.
- A random `JWT_SECRET` is generated when unset.
- `S3_BUCKET` must be set explicitly for `STORAGE=s3`. For a fully local, AWS-free run use `STORAGE=filesystem ./run.sh run` (persistent, under `~/.emissary`) or `STORAGE=memory ./run.sh run`.
- When no selector and no backend configuration is provided, the runner sets every selector to its local/no-op value (`memory`, `log`, `none`) before starting the binary. `config.Load` itself never supplies these defaults.
- `LOG_LEVEL` defaults to `DEBUG`; set it explicitly (e.g. `LOG_LEVEL=ERROR ./run.sh run`) to reduce local log volume.

See [DEVELOPMENT.md](DEVELOPMENT.md#run-locally) for the build and run workflow.

## Deployed values (set by the CDK client stack)

The Lambda sets every selector explicitly (`STORAGE=s3`, `SENDER=aws-ses`, and `METADATA`, `SESSION`, `USER_MANAGER`, `RECEIVER`), plus `TABLE_PREFIX`, region variables for enabled DDB stores, `JWT_SECRET` as a Secrets Manager ARN, `DOMAIN`, admin credentials and JWT audience/issuer. Deploy-time inputs are listed in [DEPLOYMENT.md](DEPLOYMENT.md#deploy-time-variables).
