# User Management

When Emissary manages its own users, an administrator can create,
update, list and delete user accounts through the API. The normative
wire contract will live in `api/users.yaml` (not added yet); this
document describes the design. Error conventions (status codes,
`{"error": "<message>"}` bodies, generic messages) are shared with the
rest of the API — see [API.md](API.md).

## When user management applies

User operations are available only when **both** of the following
hold:

1. The authenticated user carries the `EmissaryUserManagement`
   permission (a JWT claim; see [AUTH.md](AUTH.md)).
2. The configured user manager backend is Emissary-mastered — it owns
   its user store rather than proxying to an external directory.

The backends selected with `USER_MANAGER` are `none`, `any`, `ddb` and
`ldap` (see [CONFIGURATION.md](CONFIGURATION.md)); only the in-
application `ddb` backend is Emissary-mastered. The `UserManager`
interface will gain an `EmissaryMastered` boolean so the API layer can
determine condition 2 without hard-coding backend names.

## Interfaces

`Authenticate` and `Lookup` stay on `UserManager` unchanged:

```go
type UserManager interface {
	Name() string
	Authenticate(ctx context.Context, username, password string) (*User, error)
	Lookup(ctx context.Context, username string) (*User, error)
}
```

The user operations live on a separate interface,
`EmissaryAuthManager`, which deals with `*EmissaryUser` values:

| Operation | Description |
| --- | --- |
| `list` | Cursor-based listing of all users, with a page size (default 20). May return just a projection (id (email), enabled). |
| `create` | Create a new user. |
| `save` | Save changes to an existing user. |
| `get` | Fetch a user by email address. |
| `delete` | Delete a user by email address. |

## The user

A managed user (`EmissaryUser`) supports:

| Field | Description |
| --- | --- |
| id (email) | The email address the user logs in with; the user's stable identifier. |
| enabled | When false, the user is kept but cannot log in. |
| password | Stored salted; verified at login. |
| system rules | A string array of rules applied to mail for the user (below). |

### System rules

Each user carries a `system rules` string array defining how mail for
that user is handled. For example, a disabled user could carry a
`bounce` rule (reject the mail as if the user does not exist) or a
`delete` rule (silently discard it).

The rules are not implemented yet: each has specific requirements that
will be addressed separately.

## Status

Nothing in this document is implemented. The `ddb` user manager is
registered and selectable but panics, `api/users.yaml` does not exist
yet, and `UserManager` has no `EmissaryMastered` method.

See [AUTH.md](AUTH.md) for the token claims,
[API.md](API.md) for the endpoints as implemented, and
[CONFIGURATION.md](CONFIGURATION.md) for `USER_MANAGER` and the
backend settings.
