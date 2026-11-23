# Emissary

A minimal email service. It turns AWS SES inbound mail in S3 into a small webmail application with a JMAP API;
outbound mail goes through SES or directly to recipient MX hosts (`SENDER=mta`). A single Go binary runs as a Lambda, natively, or in a container.

## Usage

Emissary provides a lightweight way to read and send mail from an AWS SES S3 setup: SES stores each raw message, a
consumer delivers a copy into per-user mailboxes, and one Go binary serves the React client and API. See [Architecture](docs/ARCHITECTURE.md#inbound-mail-flow) and [S3](docs/S3.md#bucket-layout).

## Deployment Quickstart

You will need a valid AWS account, and a route-53 zone that includes the configured DOMAIN.
For S3 mail storage with DynamoDB metadata, this is the smallest practical set of variables to start the app:

```bash
export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
export AWS_REGION=ap-southeast-2
export DOMAIN=YOURDOMAIN.com
export TABLE_PREFIX=emissary
export STORAGE=s3
export METADATA=ddb
export RECEIVER=s3
export USER_MANAGER=s3

./run.sh deploy
```

You may need to configure the USERNAME and PASSWORD env var in order to have a bootstrap user to set up receiving email accounts from.

The keys above are full-length dummy values — replace them with real credentials (or use `AWS_PROFILE`/an IAM role). `DOMAIN=YOURDOMAIN.com` is the receiving domain; the CDK deployment for it is reachable at `https://mail.YOURDOMAIN.com`, while this local run listens on `http://localhost:8080`.

`STORAGE=s3` and `METADATA=ddb` select the requested backends. `RECEIVER=s3` and `USER_MANAGER=s3` are also part of the minimum: the bucket and `TABLE_PREFIX` would otherwise configure those components for both S3 and DDB, and startup refuses to guess. `SENDER=aws-ses` is inferred from the AWS credentials, and `SESSION=ddb` from `TABLE_PREFIX` (set `SESSION=memory` if you only want the metadata table). The `kncept-com-metadata` and `kncept-com-sessions` tables must already exist — Emissary does not create DynamoDB tables at startup. `run.sh` generates and prints an admin password and JWT secret when unset; set `PASSWORD` and `JWT_SECRET` yourself to keep them stable across restarts.

## Documentation

- [Design](docs/DESIGN.md) — goals, concepts and technology choices
- [Architecture](docs/ARCHITECTURE.md) — components and mail flows
- [Authentication](docs/AUTH.md) — login and the access/refresh token pair
- [Users](docs/USERS.md) — user management and admin masquerade
- [Support](docs/SUPPORT.md) — System Manager tools
- [Configuration](docs/CONFIGURATION.md) — environment variables and backend selection
- [API](docs/API.md) — HTTP and JMAP API reference ([OpenAPI specs](api/))
- [S3](docs/S3.md) — bucket layout and inbound staging
- [Deployment](docs/DEPLOYMENT.md) — CDK stacks
- [Development](docs/DEVELOPMENT.md) — building and extending
- [Testing](docs/TESTING.md) — test strategy

## Building

Use `./run.sh build`; it generates API code and embeds the frontend. We never use `npx`; see [Development](docs/DEVELOPMENT.md#no-npx). For a local AWS-free run, `./run.sh run` selects the memory/log/none
backends when no backend configuration is present ([Configuration](docs/CONFIGURATION.md#aws-and-local-runs)).

## Users and authentication

`USERNAME`/`PASSWORD` define the built-in administrator. A blank `PASSWORD` disables administrator login without affecting other user managers. `USER_MANAGER` selects one of the following:

| Backend | Description |
| --- | --- |
| `none` | Only the built-in administrator can log in (explicit opt-in). |
| `ddb` | In-application user management in DynamoDB; administrators manage accounts through `/api/user/`. |
| `s3` | Users stored as `users/<email>/user.json` in the mail bucket. |
| `filesystem` | Users stored as `<FILESYSTEM_DIR>/users/<email>/user.json`. |
| `ldap` | Reserved for LDAP proxy authentication; unimplemented. |

Set selectors explicitly, or leave them unset only when exactly one backend has configuration; see
[Backend selection](docs/CONFIGURATION.md#backend-selection-all-pluggable-components). The same rule applies to `STORAGE`, `SENDER`, `RECEIVER`, `METADATA` and `SESSION`.

The access token's `sub` is the user's email address; `systemManager` and `EmissaryUserManagement` grant support and
user-management rights. Mail is exposed through JMAP accounts: a user with `EmissaryUserManagement` may pass another
`accountId` to masquerade for support ([USERS.md](docs/USERS.md#admin-masquerade)). From addresses are locked to the
submitting account (or the masquerade target), and any client-supplied From is ignored. See [API.md](docs/API.md#jmap-mail-operations) and [AUTH.md](docs/AUTH.md).
