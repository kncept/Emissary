# Emissary

A minimal email service.

## Usage
This provides a simple and lightweight way to access emails from an AWS SES S3 setup.

It can be set up as a lambda, run natively or embedded in a dockerfile.

## Building
Use the run.sh build command, as it handles embedding the frontend

## Users and authentication

`AUTH_USERNAME` and `AUTH_PASSWORD` (also available as
`EMISSARY_USERNAME`/`EMISSARY_PASSWORD`) define the single built-in
administrator. If `AUTH_PASSWORD` is blank the administrator cannot log in,
but other user managers can still authenticate ordinary users.

The user manager is selected with `EMISSARY_USER_MANAGER`:

| Type | Description |
| --- | --- |
| `none` (default) | Only the built-in administrator can log in. |
| `any` | Any valid receiving address in `EMISSARY_DOMAIN` can log in with any password. |
| `ddb` | Reserved for in-application user management backed by DynamoDB. |
| `ldap` | Reserved for proxying authentication to an LDAP server. |

The JWT carries the user id in the `sub` claim and only includes
`admin: true` for the built-in administrator. Ordinary users only see mail
addressed to them or sent by them. Administrators can filter the mailbox with
`GET /api/emails?user=<address>` (omit the filter to see every message) and can
send as any address.
