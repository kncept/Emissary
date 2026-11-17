# Emissary

A minimal email service.

## Usage
This provides a simple and lightweight way to access emails from an AWS SES S3 setup.

It can be set up as a lambda, run natively or embedded in a dockerfile.

## Documentation
- [Design](docs/DESIGN.md) — what Emissary is, its goals and technology choices
- [Architecture](docs/ARCHITECTURE.md) — how the components fit together and the mail/auth flows
- [Authentication](docs/AUTH.md) — login and the access/refresh token pair
- [Users](docs/USERS.md) — in-application user management (the `ddb` backend)
- [Support](docs/SUPPORT.md) — admin support tools for configuration and inbound staging
- [Configuration](docs/CONFIGURATION.md) — every environment variable
- [API](docs/API.md) — the HTTP API reference
- [Deployment](docs/DEPLOYMENT.md) — the CDK stacks and deploy workflow
- [Development](docs/DEVELOPMENT.md) — building, testing and extending
- [Testing](docs/TESTING.md) — the test strategy, layers and conventions

## Building
Use the run.sh build command, as it handles embedding the frontend

## Users and authentication

`USERNAME` and `PASSWORD` define the single built-in
administrator. If `PASSWORD` is blank the administrator cannot log in,
but other user managers can still authenticate ordinary users.

The user manager is selected with `USER_MANAGER`:

| Type | Description |
| --- | --- |
| `none` (default) | Only the built-in administrator can log in. |
| `any` | Any valid receiving address in `DOMAIN` can log in with any password. |
| `ddb` | In-application user management backed by DynamoDB. Administrator users can manage accounts through `/api/user/`. |
| `ldap` | Reserved for proxying authentication to an LDAP server. |

The JWT carries the user id (the user's email address) in the `sub` claim and
only includes `admin: true` for the built-in administrator. Mail is exposed
through JMAP as per-user **mailboxes**: ordinary users read only their own
`accountId`, while an administrator can pass another account's `accountId` on a
JMAP method call to masquerade for support. Administrators can send as any
address. See [API.md](docs/API.md#jmap-mail-operations) and
[S3.md](docs/S3.md#bucket-layout) for details.
