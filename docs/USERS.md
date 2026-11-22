# User Management

When Emissary manages its own users, an administrator can create, update, list
and delete accounts through the API. The wire contract is
[`api/users.yaml`](../api/users.yaml); error conventions follow
[API.md](API.md#conventions). Tokens and claims are described in
[AUTH.md](AUTH.md).

## When user management applies

User operations require both:

1. The `EmissaryUserManagement` JWT claim ([AUTH.md](AUTH.md)).
2. An Emissary-mastered user manager — `ddb`, `s3` or `filesystem` — rather
   than an external directory. `none` and `ldap` do not support them.

`USER_MANAGER` must be set explicitly, or left unset only when exactly one
backend has configuration; see
[Backend selection](CONFIGURATION.md#backend-selection-all-pluggable-components).
With DynamoDB, table names are fixed suffixes of an explicitly set
`TABLE_PREFIX` ([CONFIGURATION.md](CONFIGURATION.md#dynamodb-table-naming));
the `s3` backend stores each user as `users/<email>/user.json` in the mail
bucket ([S3.md](S3.md#bucket-layout)) and `filesystem` uses
`<FILESYSTEM_DIR>/users/<email>/user.json`. The API detects condition 2 by
checking that the configured manager implements `EmissaryAuthManager`.

## Interfaces and records

The interfaces are defined in
[`usermanager.go`](../mailer/components/usermanager/usermanager.go):
[`UserManager`](../mailer/components/usermanager/usermanager.go) provides
`Name`, `Authenticate`, `Lookup` and `Autocomplete`, while
[`EmissaryAuthManager`](../mailer/components/usermanager/usermanager.go)
embeds it with the management operations:

| Operation | Description |
| --- | --- |
| `List` | Cursor-based listing, page size default 20; may return a projection (email, enabled) plus a next cursor. |
| `Create` / `Save` / `Get` | Create, update or fetch a user by email address. |
| `Delete` | Delete a user by email address. |
| `ChangePassword` | Change a user's password, verifying the old one. |

Records are [`EmissaryUser`](../mailer/components/usermanager/usermanager.go),
which embeds `User`: the email address is the only user id and JWT subject,
and `SystemManager` marks support privileges. Added fields (see the source for
the full struct):

| Field | Description |
| --- | --- |
| `Enabled` | Kept, but cannot log in when false. |
| `SaltedPassword` | Stored verifier, checked at login. |
| `Salt` | Salt/prefix selecting the hash algorithm ([salt rules](#salt-rules)). |
| `Rules` | System rules applied to the user's mail; not implemented yet (e.g. `bounce` or `delete` for disabled users). |
| `UserManager` | May create, update and delete users via the API. |

Users cannot delete their own account: the API returns `403 Forbidden`,
matching the path name case-insensitively against the JWT subject. The admin
UI ([`Users.tsx`](../frontend/src/components/Users.tsx)) disables **Delete**
on the signed-in row and uses the in-app
[`ConfirmDialog`](../frontend/src/components/ConfirmDialog.tsx) for
destructive actions ([DESIGN.md](DESIGN.md#frontend-ui-conventions)). Its
**No password** checkbox creates a user with no verifier or salt: no password,
not even an empty one, can authenticate it; a System Manager can assign one
later.

## Change Password

`GET /api/user/password-change-supported` needs only a bearer token and
returns `{"supported": true}` or `false` for every backend. The frontend
caches this once per page load and hides the Settings **Change password** UI
when unsupported.

Any authenticated user can change their own password via
`POST /api/user/change-password`; the account comes from the bearer token's
`sub` claim. The current password is verified and the new one stored with the
backend's hashing scheme ([`password.go`](../mailer/components/usermanager/password.go)).
`POST /api/user/{email}/password` does the same when the path names the
caller; users with `EmissaryUserManagement` may change any user's password,
with `oldPassword` optional for other accounts (verified if provided).
Resetting a privileged account (System Manager or User Manager role) also
requires the caller's `systemManager` claim, preventing escalation. Users can
always change their own password with their current password.

Request and response:

```json
{ "oldPassword": "current password", "newPassword": "new password" }
```

```json
{ "success": true }
```

Errors: `400` missing/invalid fields, `401` not authenticated, `403`
insufficient permissions (not the owner, missing claim, or backend cannot
store a verifier), `404` user not found, `409` old password incorrect. Every
successful change revokes all sessions for that user, so refresh tokens issued
before it cannot be exchanged. These endpoints require an Emissary-mastered
backend; otherwise `403` and the capability endpoint returns `false`.

## Vacation Response

The header's **Vacation** button opens a modal
([`VacationSettings.tsx`](../frontend/src/components/VacationSettings.tsx))
that loads and saves the JMAP `VacationResponse` singleton, so any JMAP client
sees the same settings. An administrator who is masquerading edits the target
account's settings.

The form controls `isEnabled`, an optional `fromDate`/`toDate` range,
`subject` and the reply body; the body uses the Compose editor and preserves
both `textBody` and `htmlBody`. Dates are sent as UTC timestamps and blank
optional values clear the property. Replies only go out while enabled and in
range, following RFC 3834 loop prevention ([API.md](API.md)).

The store is shared by the JMAP handler and the inbound receiver
([`vacation.go`](../mailer/components/usermanager/vacation.go)). Settings
persist in `user.json` or the DynamoDB item for durable backends; `none`, LDAP
and test backends keep a process-local store that is lost on restart.

## Salt rules

The salt prefix in
[`password.go`](../mailer/components/usermanager/password.go) selects the
verifier algorithm:

| Prefix | Algorithm | Notes |
| --- | --- | --- |
| *(blank)* | Plaintext | Insecure; migration/testing only |
| `b-` | bcrypt | Default for new passwords |
| `a2-` | Argon2id | |
| `p2-` | PBKDF2-SHA256 | Legacy `ddb` records |

Verification inspects the prefix (plaintext comparison, bcrypt, Argon2 or
PBKDF2-SHA256). A blank password stores both `SaltedPassword` and `Salt`
empty, so the account cannot log in.

## Admin Masquerade

Users with `EmissaryUserManagement` can masquerade as another user to view and
act in that user's JMAP account for support. Selecting a target uses the
autocomplete endpoint; the JMAP dispatcher accepts the target `accountId`
only with that claim. The `systemManager` claim is not required for
masquerade — it governs the support tools instead.

### Frontend UI

The UI ([`MasqueradeModal.tsx`](../frontend/src/components/MasqueradeModal.tsx))
shows a **Masquerade** button in the header for permitted users and an action
in each user row ([`Users.tsx`](../frontend/src/components/Users.tsx)).
Autocomplete behavior:

- Search starts after at least 3 characters, debounced 100 ms; only one
  request is in flight and the last 3 lookups are cached in memory.
- A spinner shows during search and an empty state when there are no matches;
  up to 10 results are listed and clicking one selects the full email.
- Stale responses never override newer ones; the latest valid results are
  always displayed.
- **Cancel**, the close button, the backdrop or **Escape** dismiss without
  changing the target.
- The UI is restricted to users with `EmissaryUserManagement`; security is
  enforced server-side (the API returns `403` without the claim).

### API Endpoint (Autocomplete)

`GET /api/user/autocomplete` is served directly by the configured
`UserManager` (see [`handler.go`](../mailer/handler/handler.go)), not by the
generated management server. It requires `EmissaryUserManagement` and an
Emissary-mastered backend.

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/user/autocomplete` | Bearer + claim | Search users by partial email for masquerade. |

- Query parameter `q`: partial email address, minimum 3 characters.
- Response `200`: `{"users": ["user@example.com", "other@example.com"]}`
  (up to 10; an empty list means no matches, not `404`).
- Errors: `401` not authenticated, `403` lacking the claim. Bodies follow
  [API.md](API.md#conventions).

### Activation and session

After selecting a user, the SPA stores the address in
`window.localStorage['masquerade_as']`. The JMAP client converts it to the
opaque `acct_` id used as `accountId` for listing, getting, deleting and
submitting mail. The JWT is unchanged and still identifies the caller; the
dispatcher authorizes each call. Clearing `masquerade_as` exits masquerade and
reverts to the id derived from the JWT subject; the header shows the active
target while set. Search semantics (prefix vs. substring, case sensitivity,
exclusions) are not specified.

## Implementation

The management API is in [`users.go`](../mailer/handler/users.go). Backends
live in [`mailer/components/usermanager/`](../mailer/components/usermanager/):
`ddb` uses the `users` table keyed by email, `s3` writes `EmissaryUser` to
`users/<email>/user.json` (a mailbox without that file is not a user), and
`filesystem` stores users under the configured root. Passwords use the
salt-prefix scheme above (bcrypt for new hashes; legacy `ddb` records use
`p2-`). `ldap` and `none` do not implement `EmissaryAuthManager`, so they
support neither management nor password change.

See [AUTH.md](AUTH.md) for tokens and
[CONFIGURATION.md](CONFIGURATION.md#user-manager-login-for-ordinary-users)
for backend settings.
