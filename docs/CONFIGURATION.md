# Configuration

All runtime configuration is read from environment variables by
`mailer/config` when `cmd/frontend` starts. Values are cached, and any
value that starts with `arn:aws:` is transparently resolved from AWS
Secrets Manager (`mailer/envutil` + `mailer/awsutil`). This is how the
deployed Lambda receives its JWT secret as an ARN.

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

## Storage (read side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `STORAGE` | `memory` | `s3` or `memory`. |
| `S3_BUCKET` | — (required when `STORAGE=s3`) | Bucket holding raw inbound messages. Required when `STORAGE=s3`. Objects are read at their exact key (SES writes them at the bucket root). |

## Sender (write side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `SENDER` | `aws-ses` when AWS credentials are configured, otherwise `log` | `aws-ses` (alias `ses`), `mta` or `log`. |
| `MTA_PORT` | `25` | SMTP port used by the `mta` sender (e.g. `1025` for a local mail sink). |

There is no configured From address. From is always the submitting user's own
address (or the masquerade target's when a User Manager sends on behalf of
another account). Reply-To is carried on the message and, when masquerading,
must match From (it defaults to From when omitted).

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

## Metadata (read/flag state)

| Variable | Default | Description |
| --- | --- | --- |
| `METADATA` | `memory` | `memory`, `file` or `ddb` (alias `dynamodb`). |
| `METADATA_FILE` | `emissary-metadata.json` | JSON file used when `METADATA=file`. |
| `DDB_TABLE` | derived from `TABLE_PREFIX` | DynamoDB table for message read/flag state. When unset, the name is derived from `TABLE_PREFIX` (`metadata` table). |
| `METADATA_REGION` | AWS region | Region of the DynamoDB table. |

## Sessions (refresh tokens)

| Variable | Default | Description |
| --- | --- | --- |
| `SESSION` | `memory` | `memory`, `ddb` or `dynamodb`. |
| `SESSION_TABLE` | derived from `TABLE_PREFIX` | DynamoDB table for sessions. When unset, the name is derived from `TABLE_PREFIX` (`sessions` table). |
| `SESSION_REGION` | AWS region | Region of the DynamoDB sessions table. |

## User manager (login for ordinary users)

| Variable | Default | Description |
| --- | --- | --- |
| `USER_MANAGER` | auto-selected | `none`, `ddb`, `s3` or `ldap`. When unset: `s3` if `STORAGE=s3`, otherwise `none`. The `s3` backend stores each user as a `users/<email>/user.json` object in the mail storage bucket and requires `S3_BUCKET`. |
| `DOMAIN` | — | Receiving domain; used for mail delivery, bounce sender addresses and the CDK app. |
| `USER_TABLE` | derived from `TABLE_PREFIX` | DynamoDB table for the `ddb` backend. When unset, the name is derived from `TABLE_PREFIX` (`users` table). |
| `USER_REGION` | AWS region | Region of the user table (and of the `s3` backend's S3 client). |
| `LDAP_URL` | — | LDAP server URL; required by the `ldap` backend (not yet implemented). |
| `LDAP_BIND_DN` / `LDAP_BIND_PASSWORD` | — | Bind credentials for the `ldap` backend. |
| `LDAP_BASE_DN` | — | Search base for the `ldap` backend. |
| `LDAP_USER_FILTER` | — | User search filter for the `ldap` backend. |
| `LDAP_CACHE_TTL` | — | Cache TTL for the `ldap` backend. |

## DynamoDB table naming

DynamoDB table names are derived from a prefix the same way as the
household project:

| Variable | Default | Description |
| --- | --- | --- |
| `TABLE_PREFIX` | `emissary` | Prefix applied to logical DynamoDB table names. `DDB_TABLE_PREFIX` is accepted as an alias. |

The resolved table name is `<prefix><name>` when the prefix already ends
with `-` or `_`, otherwise `<prefix>-<name>`. When `TABLE_PREFIX` is not
set, the default prefix is `emissary` (for example `emissary-metadata`).

The logical table names are `metadata` (read/flag state), `sessions`
(refresh tokens) and `users` (the `ddb` user manager). Explicit
`DDB_TABLE`, `SESSION_TABLE` and `USER_TABLE` values override the derived
names.

## AWS

| Variable | Description |
| --- | --- |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | Default region for DynamoDB and the SDK config chain. |
| AWS credentials | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, a web identity token (`AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN`) or ECS container credentials. When configured and `SENDER` is unset, the sender defaults to `aws-ses`. |
| `AWS_LAMBDA_FUNCTION_NAME` | Presence switches `cmd/frontend` into Lambda mode (set by the Lambda runtime). |

## Local runs (`run.sh run`)

`run.sh run` builds everything and starts `.build/frontend` with these
behaviours on top of the table above:

- `USERNAME` defaults to `Emissary`.
- A random `PASSWORD` is generated when the variable is **unset**;
  explicitly setting it blank disables admin login.
- A random `JWT_SECRET` is generated when unset.
- `S3_BUCKET` must be set explicitly when using `STORAGE=s3`. For a
  fully local, AWS-free run use `STORAGE=memory ./run.sh run`.
- `METADATA` defaults to `memory` and `PORT` to `8080`.

## Deployed values (set by the CDK client stack)

The Lambda is created with: `STORAGE=s3`, `SENDER=aws-ses`,
`METADATA=ddb`, `TABLE_PREFIX`, and the resolved DynamoDB table/region
variables for the enabled DDB-backed stores (including
`METADATA_REGION`/`DDB_TABLE`), `JWT_SECRET` as the Secrets Manager ARN,
plus `USERNAME`, `PASSWORD` (only when provided at deploy time),
`USER_MANAGER` (only when provided at deploy time; the app auto-selects
the `s3` backend since storage is S3), `DOMAIN`, `JWT_AUDIENCE=mail.<DOMAIN>` and
`JWT_ISSUER=emissary-<DOMAIN>`. Deploy-time variables are listed in
[DEPLOYMENT.md](DEPLOYMENT.md#deploy-time-variables).
