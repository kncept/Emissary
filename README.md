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

We never use `npx`; see [Development](docs/DEVELOPMENT.md#no-npx) for the
tooling convention and the npm-script workaround.

## Users and authentication

`USERNAME` and `PASSWORD` define the single built-in
administrator. If `PASSWORD` is blank the administrator cannot log in,
but other user managers can still authenticate ordinary users.

The user manager is selected with `USER_MANAGER` (auto-selected to `s3`
when `STORAGE=s3` and no backend is chosen):

| Type | Description |
| --- | --- |
| `none` | Only the built-in administrator can log in. |
| `ddb` | In-application user management backed by DynamoDB. Administrator users can manage accounts through `/api/user/`. |
| `s3` | In-application user management in the mail storage bucket: each user is a `users/<email>/user.json` object next to the user's mailboxes. |
| `ldap` | Reserved for proxying authentication to an LDAP server. |

The JWT carries the user id (the user's email address) in the `sub` claim,
`systemManager: true` for the built-in administrator and System Manager users,
and `EmissaryUserManagement: true` for users who may manage accounts. Mail is
exposed through JMAP as per-user **mailboxes**: ordinary users read only their
own `accountId`, while a user with `EmissaryUserManagement` can pass another
account's `accountId` on a JMAP method call to masquerade for support. The
built-in administrator account can send with a custom envelope sender. See
[API.md](docs/API.md#jmap-mail-operations) and [S3.md](docs/S3.md#bucket-layout)
for details.
