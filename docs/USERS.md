# User Management

When Emissary manages its own users, an administrator can create,
update, list and delete user accounts through the API. The normative
wire contract for the management endpoints lives in `api/users.yaml`;
this document describes the design. Error conventions (status codes,
`{"error": "<message>"}` bodies, generic messages) are shared with the
rest of the API — see [API.md](API.md).

## When user management applies

User operations are available only when **both** of the following
hold:

1. The authenticated user carries the `EmissaryUserManagement`
   permission (a JWT claim; see [AUTH.md](AUTH.md)).
2. The configured user manager is Emissary-mastered — it owns its
   user store rather than proxying to an external directory.

The backends selected with `USER_MANAGER` are `none`, `ddb`, `s3` and
`ldap` (see [CONFIGURATION.md](CONFIGURATION.md)); the in-application
`ddb` and `s3` backends are Emissary-mastered. When no backend is selected
and the storage backend is S3, the `s3` backend is selected automatically.
When using DynamoDB, table names are derived from `TABLE_PREFIX` the same
way as the household project (default `emissary`), with explicit
`USER_TABLE`/`DDB_TABLE`/`SESSION_TABLE` overrides available (see
[CONFIGURATION.md](CONFIGURATION.md#dynamodb-table-naming)). The `s3`
backend stores each user as a `users/<email>/user.json` object in the mail
storage bucket, next to the user's mailboxes (see
[S3.md](S3.md#bucket-layout)). The API layer determines condition 2 by
type assertion: the configured
`UserManager` is Emissary-mastered when it also implements
`EmissaryAuthManager`, the superinterface defined below.

## Interfaces

`EmissaryAuthManager` is a superinterface of `UserManager`: it
embeds `UserManager`, so any type that implements
`EmissaryAuthManager` also implements `UserManager` and can be
assigned wherever a `UserManager` is expected. `Authenticate` and
`Lookup` therefore stay on `UserManager` unchanged:

```go
type UserManager interface {
	Name() string
	Authenticate(ctx context.Context, username, password string) (*User, error)
	Lookup(ctx context.Context, username string) (*User, error)
	Autocomplete(ctx context.Context, q string) ([]string, error)
}

type EmissaryAuthManager interface {
	UserManager

	List(ctx context.Context, cursor string, pageSize int) (users []*EmissaryUser, nextCursor string, err error)
	Create(ctx context.Context, user *EmissaryUser) error
	Save(ctx context.Context, user *EmissaryUser) error
	Get(ctx context.Context, email string) (*EmissaryUser, error)
	Delete(ctx context.Context, email string) error
	ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error
}
```

The management operations deal with `*EmissaryUser` values:

| Operation | Description |
| --- | --- |
| `List` | Cursor-based listing of all users, with a page size (default 20). Returns the page and a cursor for the next page; may return just a projection (id (email), enabled). |
| `Create` | Create a new user. |
| `Save` | Save changes to an existing user. |
| `Get` | Fetch a user by email address. |
| `Delete` | Delete a user by email address. |
| `ChangePassword` | Change a user's password. Requires the old password for verification. |

Users cannot delete their own account: the API rejects it with
`403 Forbidden`, even for administrators. The login name is compared
case-insensitively with the authenticated user's email (the JWT subject),
and the admin web UI disables **Delete** on the signed-in user's row.

In the admin web UI, **Delete** opens an in-app confirmation modal: the
account is removed only after **Delete** is clicked, and clicking the
backdrop cancels. No browser dialog is used (see
[DESIGN.md](DESIGN.md#frontend-ui-conventions)).

The Create User form has a **No password** checkbox. When checked, the
password field is disabled and reads *cannot log in*; the account is
created with no password verifier and no salt, and no password (not even
an empty one) can authenticate it. A system manager can assign a password
later with the Change Password endpoint.

## Change Password

Clients can discover whether password change is available with `GET /api/user/password-change-supported`. It needs only a valid bearer token and returns `{"supported": true}` or `{"supported": false}` for every backend. The frontend calls this once per page load, caches the answer in memory, and hides the Settings screen's **Change password** UI when it is unsupported.

Any authenticated user can change their own password via `POST /api/user/change-password`. The account is taken from the bearer token's `sub` claim, so no email or special permission is needed. The current password is verified against the stored verifier, and the new password is stored with the backend's password hashing scheme.

The same operation is available via `POST /api/user/{email}/password` when the path names the authenticated user. Administrators with the `EmissaryUserManagement` claim can also use that endpoint to change any user's password. When an admin changes another user's password, the `oldPassword` field is optional in the spec (but if provided, it is verified).

Resetting a privileged account (one with the System Manager or User Manager role) additionally requires the caller to have the `systemManager` claim. This prevents a User Manager from escalating to System Manager by resetting a System Manager's password. Users can always change their own password with their current password.

Every successful password change revokes all sessions for that user, so refresh tokens issued before the change can no longer be exchanged.

The change endpoints are only available when the configured user manager implements `EmissaryAuthManager` (i.e., the `ddb`, `filesystem` or `s3` backend). Otherwise, they return `403 Forbidden`; the capability endpoint still returns `{"supported": false}`.

Request body:
```json
{
  "oldPassword": "current password",
  "newPassword": "new password"
}
```

Response (`200`):
```json
{ "success": true }
```

Errors:
- `400` — Missing or invalid fields
- `401` — Authentication required
- `403` — Insufficient permissions (not the account owner, no EmissaryUserManagement permission, no systemManager permission for a privileged target, or backend doesn't support password change)
- `404` — User not found
- `409` — Old password is incorrect

## The user

`EmissaryUser` is a superstruct of `User`: it embeds `User` (whose
`Email` is the user's email address — the only user id in the system, also
used as the JWT subject — and whose `SystemManager` flag marks users with
system management privileges), so it is a `User` as well: it promotes
`User`'s fields and methods, and its embedded `User` can be handed anywhere
a `User` (or `*User`) is expected. The fields it adds:

```go
type EmissaryUser struct {
	User // Email, SystemManager

	Enabled        bool     // kept, but cannot log in when false
	SaltedPassword string   // the user's stored, salted password
	Salt           string   // The salt for the users password
	Rules          []string // system rules, see below
	UserManager    bool     // can manage users via the API
}
```

| Field | Description |
| --- | --- |
| `Enabled` | When false, the user is kept but cannot log in. |
| `SaltedPassword` | The user's stored, salted password; verified at login. |
| `Salt` | The users salt. Note that this has usage rules |
| `Rules` | A string array of rules applied to mail for the user (below). |
| `UserManager` | Whether the user may create, update, and delete users via the API. |

### System rules

Each user carries a `system rules` string array defining how mail for
that user is handled. For example, a disabled user could carry a
`bounce` rule (reject the mail as if the user does not exist) or a
`delete` rule (silently discard it).

The rules are not implemented yet: each has specific requirements that
will be addressed separately.

## Salt Rules

The `Salt` field determines how the password in `SaltedPassword` is hashed.
The prefix of the salt value identifies the algorithm:

| Prefix | Algorithm | Notes |
| --- | --- | --- |
| *(blank / no prefix)* | Plaintext | **Insecure** — only for migration or testing |
| `b-` | bcrypt | Default for new passwords |
| `a2-` | Argon2id | |
| `p2-` | PBKDF2-SHA256 | Legacy; used by existing `ddb` records |

When hashing a new password (on create or password change), the default is **bcrypt** (`b-` prefix). If the supplied password is blank, both `SaltedPassword` and `Salt` are stored empty: the user has no password and cannot log in.

Verification works by inspecting the salt prefix:
- No prefix → plaintext comparison
- `b-` → bcrypt.CompareHashAndPassword
- `a2-` → argon2.Verify
- `p2-` → PBKDF2-SHA256 verification

## Admin Masquerade

Users with the User Manager permission can **masquerade as another user** to
view and act in that user's JMAP account/mailboxes for support and
administration purposes. Selecting a target uses the
`EmissaryUserManagement` claim (the autocomplete endpoint), and the JMAP
dispatcher accepts the target `accountId` only when the access token has the
`EmissaryUserManagement` claim. The `systemManager` claim is not required for
masquerade; it governs the support tools instead.

### Frontend UI

- A **"Masquerade" button** (with a mask icon) appears in the top header bar for users with admin permissions (the `EmissaryUserManagement` claim in their JWT).
- Clicking the button opens a **modal** with a user ID (email address) input field and an autocomplete results area.
- The **User Management** user list also has a **"Masquerade" action** in each row's Actions column; it switches directly to that user's account.
- **Autocomplete behavior:**
  - Search begins only after **at least 3 characters** are entered.
  - Requests are **debounced by 100 ms** after the last keystroke.
  - A **spinner** shows while a search is in progress.
  - Only **one request** is in flight at a time; rapid typing does not fire parallel requests.
  - The client caches the **last 3 lookups in memory** (keyed by normalized query).
  - The UI **always displays the latest valid results** for the current query (subject to the debounce delay); stale responses never override newer ones.
  - Up to **10 matches** are shown; clicking a result selects that full user ID (email address).
  - When a search completes with no matches, an **empty state** is displayed.
- The frontend restricts the masquerade UI to users with `EmissaryUserManagement`, and **security is enforced server-side** (the API returns `403` without that claim).

### API Endpoint (Autocomplete)

The autocomplete endpoint lives under the `/api/user/` prefix but is served directly by the configured `UserManager` (see `mailer/handler/handler.go`), not by the generated user-management server. `Autocomplete` is part of the `UserManager` interface only.

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/user/autocomplete` | Bearer\* | Search users by partial user ID (email address) for admin masquerade. |

\* Requires `EmissaryUserManagement` JWT claim and an Emissary-mastered user manager (`ddb`, `filesystem` or `s3` backend).

**Request:**
- Query parameter `q` (string): the partial user ID (email address) to search (minimum 3 characters).

**Response (`200`):**
```json
{
  "users": [
    "user@example.com",
    "other@example.com"
  ]
}
```
Returns up to **10 user IDs (email addresses)**. An empty list indicates no matches (not a `404`).

**Errors:**
- `401` — Authentication required (missing/invalid token).
- `403` — Insufficient permissions (caller lacks `EmissaryUserManagement` claim in a valid JWT).

The endpoint follows shared API conventions from [API.md](API.md) (error body `{"error": "<message>"}`).

### Activation & Session

After selecting a user from autocomplete, the SPA stores the address in
`window.localStorage['masquerade_as']`. JMAP calls include it as the
method call's `accountId`, which is the effective mailbox context for
listing, getting, deleting and submitting mail. The JWT is unchanged and
still identifies the caller; the JMAP dispatcher authorizes the
masquerade by checking the `EmissaryUserManagement` claim.

- **Exit masquerade:** Clear `masquerade_as` from localStorage; JMAP calls
  revert to the JWT `sub` as `accountId`.
- **Active indicator:** The header shows the masquerade target while it is set.
- **Search semantics:** Prefix vs. substring match, case sensitivity,
  exclusion of current user or disabled accounts.

## Status

The user management API is implemented in `api/users.yaml` and `mailer/handler/users.go`. The `ddb` user manager is implemented in `mailer/usermanager/ddb.go` and backs the `users` DynamoDB table, using the user's email address as the `id` partition key. The `s3` user manager is implemented in `mailer/usermanager/s3.go` and stores each user as a plain JSON write-out of the `EmissaryUser` at `users/<email>/user.json` in the mail storage bucket; a mailbox directory without a `user.json` is not a user. The `filesystem` backend stores users under the configured filesystem root. Passwords are stored using the salt-prefix scheme described above (defaulting to bcrypt for new hashes; existing `ddb` records use `p2-` PBKDF2-SHA256). The `ldap` and `none` backends do not implement `EmissaryAuthManager` and therefore do not support user management operations (including password change).

See [AUTH.md](AUTH.md) for login, refresh and the token claims, [API.md](API.md) for the endpoints as implemented, and [CONFIGURATION.md](CONFIGURATION.md) for `USER_MANAGER` and the backend settings.
