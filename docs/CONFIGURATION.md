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
| `STORAGE` | `s3` if `S3_BUCKET` is set, else `memory` | `s3` or `memory`. |
| `S3_BUCKET` | — | Bucket holding raw inbound messages. Required when `STORAGE=s3`. Objects are read at their exact key (SES writes them at the bucket root). |

## Sender (write side of mail)

| Variable | Default | Description |
| --- | --- | --- |
| `SENDER` | `aws-ses` if `SES_FROM` is set, else `log` | `aws-ses` (alias `ses`) or `log`. |
| `SES_FROM` | — | The From address used when a request does not carry one (`no-reply@<DOMAIN>` in the CDK deployment). Required for real sending. |

## Metadata (read/flag state)

| Variable | Default | Description |
| --- | --- | --- |
| `METADATA` | `memory` | `memory`, `file` or `ddb` (alias `dynamodb`). |
| `METADATA_FILE` | `emissary-metadata.json` | JSON file used when `METADATA=file`. |
| `DDB_TABLE` | — | DynamoDB table. Required when `METADATA=ddb`. |
| `METADATA_REGION` | AWS region | Region of the DynamoDB table. |

## User manager (login for ordinary users)

| Variable | Default | Description |
| --- | --- | --- |
| `USER_MANAGER` | `none` | `none`, `any`, `ddb` or `ldap`. |
| `DOMAIN` | — | Receiving domain; required by the `any` backend (any valid address `@DOMAIN` can log in with any password). Also used by the CDK app. |
| `USER_TABLE` | — | DynamoDB table; required by the `ddb` backend (not yet implemented). |
| `USER_REGION` | AWS region | Region of the user table. |
| `LDAP_URL` | — | LDAP server URL; required by the `ldap` backend (not yet implemented). |
| `LDAP_BIND_DN` / `LDAP_BIND_PASSWORD` | — | Bind credentials for the `ldap` backend. |
| `LDAP_BASE_DN` | — | Search base for the `ldap` backend. |
| `LDAP_USER_FILTER` | — | User search filter for the `ldap` backend. |
| `LDAP_CACHE_TTL` | — | Cache TTL for the `ldap` backend. |

## AWS

| Variable | Description |
| --- | --- |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | Default region for DynamoDB and the SDK config chain. |
| `AWS_LAMBDA_FUNCTION_NAME` | Presence switches `cmd/frontend` into Lambda mode (set by the Lambda runtime). |

## Local runs (`run.sh run`)

`run.sh run` builds everything and starts `.build/frontend` with these
behaviours on top of the table above:

- `USERNAME` defaults to `Emissary`.
- A random `PASSWORD` is generated when the variable is **unset**;
  explicitly setting it blank disables admin login.
- A random `JWT_SECRET` is generated when unset.
- `S3_BUCKET` defaults to `emissary-emails`, which implies
  `STORAGE=s3`. For a fully local, AWS-free run use
  `STORAGE=memory ./run.sh run`.
- `METADATA` defaults to `memory` and `PORT` to `8080`.

## Deployed values (set by the CDK client stack)

The Lambda is created with: `STORAGE=s3`, `SENDER=aws-ses`,
`METADATA=ddb`, `METADATA_REGION`/`DDB_TABLE` pointing at the metadata
table, `JWT_SECRET` as the Secrets Manager ARN, plus `USERNAME`,
`PASSWORD` (only when provided at deploy time), `USER_MANAGER`
(default `none`), `DOMAIN`, `SES_FROM` (default `no-reply@<DOMAIN>`),
`JWT_AUDIENCE=mail.<DOMAIN>` and `JWT_ISSUER=emissary-<DOMAIN>`.
Deploy-time variables are listed in
[DEPLOYMENT.md](DEPLOYMENT.md#deploy-time-variables).
