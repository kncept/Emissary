# Configuration

Most runtime configuration is read from environment variables by
`mailer/config` when `cmd/frontend` starts. `LOG_LEVEL` is the exception:
`mailer/logging` reads it at process startup, before configuration is loaded.
Values are cached, and values naming a Secrets Manager secret
(`arn:*:secretsmanager:...`) are transparently resolved from AWS Secrets
Manager (`mailer/envutil` + `mailer/awsutil`). This is how the deployed Lambda
receives its JWT secret as an ARN. Other ARNs (for example `AWS_ROLE_ARN`) are
left untouched. A failed resolution aborts startup: the ARN is never used as
if it were the resolved credential. Client creation is retried on the next
call, so a transient failure does not poison the process permanently.

## Backend selection (all pluggable components)

`STORAGE`, `SENDER`, `RECEIVER`, `METADATA`, `SESSION` and `USER_MANAGER`
share one rule, applied by `config.Load` at startup:

1. **An explicitly set selector wins**, whatever other configuration exists.
2. **If the selector is unset and exactly one backend has configuration, use
   that backend.**
3. **If none has configuration, error.** The local/no-op implementations
   (`memory`, `log` and `none`) are still supported, but are opt-ins: set
   `STORAGE=memory`, `SENDER=log`, `METADATA=memory`, `SESSION=memory`,
   `RECEIVER=memory` or `USER_MANAGER=none` explicitly.
4. **If several backends have configuration, error.** Set the selector to
   choose one rather than letting startup guess.

What counts as configuration for a backend (when the selector is unset):

| Component | Backend | Configuration that selects it |
| --- | --- | --- |
| `STORAGE` | `s3` | AWS credentials plus `S3_BUCKET` (all S3 settings are static environment values). |
| `STORAGE` | `filesystem` | `FILESYSTEM_DIR` set explicitly. The implicit `~/.emissary` default does not count. |
| `SENDER` | `aws-ses` | AWS credentials from the SDK chain, including IAM roles. |
| `SENDER` | `mta` | `MTA_PORT` set explicitly. Its default of `25` does not count. |
| `RECEIVER` | `s3` | AWS credentials plus `RECEIVER_BUCKET` (or `S3_BUCKET`). |
| `RECEIVER` | `filesystem` | `RECEIVER_FILESYSTEM_DIR` or `FILESYSTEM_DIR` set explicitly. |
| `RECEIVER` | `ddb` | AWS credentials plus an explicitly set `TABLE_PREFIX`/`DDB_TABLE_PREFIX`. |
| `METADATA` | `ddb` | AWS credentials plus an explicitly set `TABLE_PREFIX`/`DDB_TABLE_PREFIX`. |
| `METADATA` | `file` | `METADATA_FILE` or `FILESYSTEM_DIR` set explicitly. |
| `SESSION` | `ddb` | AWS credentials plus an explicitly set `TABLE_PREFIX`/`DDB_TABLE_PREFIX`. |
| `USER_MANAGER` | `s3` | AWS credentials plus `S3_BUCKET`. |
| `USER_MANAGER` | `filesystem` | `FILESYSTEM_DIR` set explicitly. |
| `USER_MANAGER` | `ddb` | AWS credentials plus an explicitly set `TABLE_PREFIX`/`DDB_TABLE_PREFIX`. |
| `USER_MANAGER` | `ldap` | `LDAP_URL` set explicitly. |

DynamoDB has a single configuration input: an explicitly set table prefix.
Table names are fixed suffixes of that prefix, so there are no per-table
overrides and no default prefix. S3 and filesystem checks take no further
input arguments: S3 uses only static environment values, and the filesystem
check is just the presence of the root variable.

For example, with `USER_MANAGER` unset, `S3_BUCKET` plus `TABLE_PREFIX` (and
AWS credentials) is an error, not a silent choice; add `USER_MANAGER=s3` or
`USER_MANAGER=ddb`. Conversely, with no backend configuration at all, add
`USER_MANAGER=none` to opt into the administrator-only implementation. An
explicit DDB selector such as `METADATA=ddb` without `TABLE_PREFIX` is also an
error.

## Server and authentication

| Variable | Default | Description |
| --- | --- | --- |
| `USERNAME` | — (required) | The single built-in administrator's username. |
| `PASSWORD` | unset | Administrator password. Blank (explicitly empty) **disables** administrator login; other user managers keep working. `run.sh run` generates a random password when the variable is *unset*. |
| `JWT_SECRET` | generated | HS256 signing secret. When unset, an ephemeral 32-byte secret is generated and tokens die on restart. May be a Secrets Manager ARN. |
| `JWT_TTL` | `24h` | Token lifetime, parsed with Go duration syntax (`30m`, `12h`, ...). |
| `JWT_AUDIENCE` | unset | Expected `aud` claim; tokens with a different audience are rejected. The CDK app sets it to `mail.<DOMAIN>`. |
| `JWT_ISSUER` | unset | Expected `iss` claim. The CDK app sets it to `emissary-<domain>`. |
| `LISTEN_ADDR` | — | Listen address, e.g. `:9090` or `127.0.0.1:8080`. Takes precedence over `PORT`. |
| `PORT` | `8080` | Listen port when `LISTEN_ADDR` is unset. |
| `MAX_FILESIZE` | `10485760` (10 MiB) | Maximum size, in bytes, of one JMAP request body and one MIME message (including decoded parts). Must be a positive integer below 1 TiB; a bad value aborts startup. |

The standalone HTTP server applies bounded read-header, read, write and idle
timeouts and shuts down gracefully on SIGINT/SIGTERM, allowing a bounded window
for in-flight requests to finish.

`MAX_FILESIZE` is enforced by both the frontend and the inbound receiver. Every
HTTP request body is capped at this size; an oversized JMAP batch is rejected
with HTTP 413 and an `application/problem+json` body of type
`urn:ietf:params:jmap:error:limit` and `limit: "maxSizeRequest"`. The configured
value is advertised in the JMAP session as `maxSizeRequest`, `maxSizeUpload`
and `maxSizeAttachmentsPerEmail`, so conformant clients can avoid sending
requests that would be rejected.

## Logging

All backend logs use Go's `log/slog` with a single threshold from `LOG_LEVEL`:

| Variable | Default | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `WARN` when unset (`ERROR` in deployments, `DEBUG` for `run.sh run`) | Minimum level emitted: `DEBUG` (-4), `INFO` (0), `WARN` (4) or `ERROR` (8). Values are case-insensitive; the numeric slog values are also accepted. Unknown values fall back to `WARN`. |

Records are written to stderr, which the Lambda runtime forwards to
CloudWatch Logs. `DEBUG` carries per-request and per-message traces,
`INFO` records lifecycle events and successful operations, `WARN` records
recoverable or degraded conditions, and `ERROR` records failed operations
that need operator attention.

## Storage (read side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | `s3`, `filesystem` or `memory`. `memory` must be set explicitly. |
| `S3_BUCKET` | — (required when `STORAGE=s3`) | Bucket holding raw inbound messages. Required when `STORAGE=s3`. Objects are read at their exact key (SES writes them at the bucket root). |
| `FILESYSTEM_DIR` | `~/.emissary` after a filesystem backend is selected | Shared root for filesystem-backed stores. `STORAGE=filesystem` stores mail under `<FILESYSTEM_DIR>/mail`. Setting it explicitly counts as backend configuration; the implicit default does not. |

## Sender (write side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `SENDER` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | `aws-ses` (alias `ses`), `mta` or `log`. `log` must be set explicitly. |
| `MTA_PORT` | `25` | SMTP port used by the `mta` sender (e.g. `1025` for a local mail sink). Setting it explicitly counts as `mta` configuration when `SENDER` is unset; the default does not. |

Credential detection uses the AWS SDK default chain, so IAM roles (EC2
instance profiles, ECS task roles, web identity/IRSA and the Lambda execution
role) select `aws-ses` even when no `AWS_*` credential variables are present.
If credentials are found and `SENDER` is unset, `aws-ses` is selected. If no
credentials and no `MTA_PORT` are found, startup errors rather than using the
log sender; set `SENDER=log` to opt into a sender that prints messages to stdout
and does **not** deliver replies or bounces.

There is no configured From address. From is locked to the submitting user's
own address (or the masquerade target's when a User Manager sends on behalf
of another account); a From address supplied by the client is ignored. The
locked address must belong to one of the account's verified email identities
(SES `ListIdentities`, covering both full addresses and domains) whenever SES
is the outbound sender; submissions from any other address are rejected with
JMAP `forbidden`. Identity lookups are cached for five minutes, and a failed
lookup fails the submission rather than allowing an unverified sender. Other
senders have no SES identity source and keep the locked-From rule only.
Reply-To is carried on the message and, when masquerading, must match From
(it defaults to From when omitted). `In-Reply-To` and `References` headers
from the draft are written into the delivered message, and the message's
stored `Message-ID` is reused on the wire.

`SENDER=mta` delivers directly: for each recipient domain it resolves MX
records, connects to the preferred mail exchanger on `MTA_PORT` and speaks SMTP.
MX hosts are tried in preference order, then the domain itself as a last
resort. Recipients are grouped by domain, and Bcc addresses appear only in the
SMTP envelope. The envelope sender and EHLO/HELO identity are taken from the
message's From address. Before delivery the sender
checks the From domain against the envelope (SPF) domain using
`github.com/mjl-/mox/dmarc`; a misaligned domain is
logged as a warning but does not stop delivery. The sender does not
authenticate to the MX host and uses STARTTLS when the server advertises it.

## Receiver (inbound routing rules)

| Variable | Default | Description |
| --- | --- | --- |
| `RECEIVER` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | Backend for recipient routing rules: `s3`, `filesystem`, `ddb`/`dynamodb` or `memory`. `memory` must be set explicitly. |
| `RECEIVER_BUCKET` | `S3_BUCKET` | S3 bucket holding the rules when `RECEIVER=s3`. Set it explicitly to keep receiver rules in a dedicated bucket. |
| `RECEIVER_FILESYSTEM_DIR` | `FILESYSTEM_DIR` (default `~/.emissary`) | Root directory holding the rules when `RECEIVER=filesystem`. The store adds the `receiver/` subdirectory. Setting it explicitly counts as filesystem configuration. |
| `RECEIVER_REGION` | `AWS_REGION` | AWS region for the `ddb` backend. With `RECEIVER=ddb` the table is `<TABLE_PREFIX>receiver`; `TABLE_PREFIX` is required. |

Each mail identity is stored as one JSON object under the `receiver/` prefix:
`s3://<RECEIVER_BUCKET>/receiver/<identity>.json` or
`<RECEIVER_FILESYSTEM_DIR>/receiver/<identity>.json`. The object is a single
`HandlerRules` entry — `{"mailIdentity": "<identity>", "rules": [...]}` — where
`mailIdentity` repeats the config key and `rules` is the one ordered list tried
first-match-wins. With `RECEIVER=ddb` each identity is one item in
`<TABLE_PREFIX>receiver`, keyed by a string partition key `id` (no sort key);
the rules are held in a `rules` list attribute and the identity is not stored
twice.
The `_` identity holds the base fallback rules (deliver to an existing user,
otherwise bounce). It is always available in memory when the backend has no
`_` entry; loading rules never writes to the store, so it is only persisted
when a save explicitly includes it.
Identities without their own configuration use `_`. The frontend API and
`s3-receiver` both load these rules at startup. The `s3-receiver` Lambda ignores
S3 object keys containing `/`, so writes under `receiver/` never get treated as
inbound mail or deleted by the message consumer. DynamoDB tables are created at
deploy time from `TABLE_PREFIX` without per-table CREATE permissions at
runtime, so the table must exist before the app starts.

The allowed rules are `DeliverToUser`, `DeliverToMailIdentity`, `Bounce` and
`Drop`. Rules are tried in order until one applies: `DeliverToUser` only
matches when the recipient is a known user, `DeliverToMailIdentity` delivers
to the identity's mailbox, `Bounce` notifies the sender, and `Drop` discards
the message for that recipient without delivering it or sending a bounce.
Because `Drop` is terminal, place it after any rule that should take
precedence. No rule matching leaves the receiver bouncing rather than
discarding the message.

## Metadata (JMAP keywords and received time)

| Variable | Default | Description |
| --- | --- | --- |
| `METADATA` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | `memory`, `file` or `ddb` (alias `dynamodb`). `memory` must be set explicitly. |
| `METADATA_FILE` | `<FILESYSTEM_DIR>/metadata` | File or directory used when `METADATA=file`. Setting it explicitly counts as file-backend configuration. By default each record is a separate `<email id>.json` file under `<FILESYSTEM_DIR>/metadata` (normally `~/.emissary/metadata`); an explicit path ending in `.json` keeps the older single-file layout. |
| `METADATA_REGION` | AWS region | Region of the DynamoDB table. With `METADATA=ddb` the table is `<TABLE_PREFIX>metadata`; `TABLE_PREFIX` is required. The table uses partition key `id` (message id), sort key `userId` (user id), and a `userId-index` GSI (`userId`, then `id`) for per-user listing. |

These settings are shared by the frontend API and the `s3-receiver` delivery
consumer: the receiver creates a metadata record for each delivered message,
stamping a server-owned `receivedAt` (RFC 3339 UTC), and the API reads and
updates keywords over JMAP. `receivedAt` is never accepted from clients. Use a
persistent backend (`file` or `ddb`) in production so both processes see the
same state. A message with no record is treated as having an empty keyword set
and no stored receive time (JMAP falls back to the message `Date` header).

## Sessions (refresh tokens)

| Variable | Default | Description |
| --- | --- | --- |
| `SESSION` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | `memory`, `ddb` or `dynamodb`. `memory` must be set explicitly. |
| `SESSION_REGION` | AWS region | Region of the DynamoDB sessions table. With `SESSION=ddb` the table is `<TABLE_PREFIX>sessions`; `TABLE_PREFIX` is required. The table uses partition key `id` (session id) and sort key `userEmail` (user id). |

## User manager (login for ordinary users)

| Variable | Default | Description |
| --- | --- | --- |
| `USER_MANAGER` | — (see [Backend selection](#backend-selection-all-pluggable-components)) | `none`, `ddb`, `s3`, `filesystem` or `ldap`. `none` must be set explicitly. The `s3` backend stores each user as a `users/<email>/user.json` object in the mail storage bucket and requires `S3_BUCKET`; the `filesystem` backend writes the same record to `<FILESYSTEM_DIR>/users/<email>/user.json`. |
| `DOMAIN` | — | Receiving domain; used for mail delivery, bounce sender addresses and the CDK app. |
| `USER_REGION` | AWS region | Region of the user table (and of the `s3` backend's S3 client). With `USER_MANAGER=ddb` the table is `<TABLE_PREFIX>users`; `TABLE_PREFIX` is required. |
| `LDAP_URL` | — | LDAP server URL; required by the `ldap` backend (not yet implemented). |
| `LDAP_BIND_DN` / `LDAP_BIND_PASSWORD` | — | Bind credentials for the `ldap` backend. |
| `LDAP_BASE_DN` | — | Search base for the `ldap` backend. |
| `LDAP_USER_FILTER` | — | User search filter for the `ldap` backend. |
| `LDAP_CACHE_TTL` | — | Cache TTL for the `ldap` backend. |

## Filesystem backend layout

With `FILESYSTEM_DIR=~/.emissary` (the default) the filesystem-backed stores
share one root:

| Store | Directory | Contents |
| --- | --- | --- |
| `STORAGE=filesystem` | `~/.emissary/mail` | `users/<account>/<mailbox>/<timestamp>_<id>.eml`, `_storage/<session>/` drafts/creation ids, and `_jmap-state/<base64url(account)>/state.json` JMAP counters/change logs/query snapshots. |
| `USER_MANAGER=filesystem` | `~/.emissary/users` | `<email>/user.json` records, the same JSON shape as the S3 user manager. |
| `METADATA=file` | `~/.emissary/metadata` | One `<email id>.json` record per message. |
| `RECEIVER=filesystem` | `~/.emissary/receiver` | One `<identity>.json` routing config per mail identity, including `_` for the base fallback. `RECEIVER_FILESYSTEM_DIR` overrides the root independently. |

All four expand a leading `~` in their configured root to the home directory and
create their directory on first use. Ids are validated before being mapped to
paths, so JMAP ids cannot escape these roots.

## DynamoDB table naming

DynamoDB table names are a configurable prefix plus a fixed logical suffix:

| Variable | Default | Description |
| --- | --- | --- |
| `TABLE_PREFIX` | — (required for any DDB backend) | Prefix applied to the fixed DynamoDB table names. `DDB_TABLE_PREFIX` is accepted as an alias. No default is supplied by the application or the CDK deployment. |

The resolved table name is `<prefix><name>` when the prefix already ends
with `-` or `_`, otherwise `<prefix>-<name>`. The suffixes are `metadata`
(JMAP keywords and `receivedAt`), `sessions` (refresh tokens), `users` (the
`ddb` user manager) and `receiver` (inbound routing rules). There are no
per-table overrides: `DDB_TABLE`, `SESSION_TABLE`, `USER_TABLE` and
`RECEIVER_TABLE` are ignored. If `TABLE_PREFIX` is unset, any DDB backend is
incomplete configuration and startup fails.

## AWS

| Variable | Description |
| --- | --- |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | Default region for DynamoDB and the SDK config chain. |
| AWS credentials | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, a web identity token (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`) or ECS container credentials. These count as configuration for AWS-backed components when their other required values are set; see [Backend selection](#backend-selection-all-pluggable-components). |
| `AWS_LAMBDA_FUNCTION_NAME` | Presence switches `cmd/frontend` into Lambda mode (set by the Lambda runtime). |

## Local runs (`run.sh run`)

`run.sh run` builds everything and starts `.build/frontend` with these
behaviours on top of the table above:

- `USERNAME` defaults to `Emissary`.
- A random `PASSWORD` is generated when the variable is **unset**;
  explicitly setting it blank disables admin login.
- A random `JWT_SECRET` is generated when unset.
- `S3_BUCKET` must be set explicitly when using `STORAGE=s3`. For a
  fully local, AWS-free run use `STORAGE=filesystem ./run.sh run`
  (persistent, under `~/.emissary`) or `STORAGE=memory ./run.sh run`.
- When no backend configuration or selector is provided, the runner sets every
  selector to its local/no-op value (`memory`, `log`, `none`) before starting
  the binary. `config.Load` itself never supplies these defaults. `PORT`
  defaults to `8080`.
- `LOG_LEVEL` defaults to `DEBUG`; set it explicitly (for example
  `LOG_LEVEL=ERROR ./run.sh run`) to reduce local log volume.

## Deployed values (set by the CDK client stack)

The Lambda is created with every selector set explicitly: `STORAGE=s3`,
`SENDER=aws-ses`, `METADATA`, `SESSION`, `USER_MANAGER` and `RECEIVER`, plus
`TABLE_PREFIX` (when configured) and the region variables for the enabled
DDB-backed stores (including `METADATA_REGION`),
`JWT_SECRET` as the Secrets Manager ARN, plus `USERNAME`, `PASSWORD` (only
when provided at deploy time), `DOMAIN`, `JWT_AUDIENCE=mail.<DOMAIN>` and
`JWT_ISSUER=emissary-<DOMAIN>`. Deploy-time variables are listed in
[DEPLOYMENT.md](DEPLOYMENT.md#deploy-time-variables).
