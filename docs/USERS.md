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
2. The configured user manager is Emissary-mastered — it owns its
   user store rather than proxying to an external directory.

The backends selected with `USER_MANAGER` are `none`, `any`, `ddb` and
`ldap` (see [CONFIGURATION.md](CONFIGURATION.md)); only the in-
application `ddb` backend is Emissary-mastered. The API layer
determines condition 2 by type assertion: the configured
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
}

type EmissaryAuthManager interface {
	UserManager

	List(ctx context.Context, cursor string, pageSize int) (users []*EmissaryUser, nextCursor string, err error)
	Create(ctx context.Context, user *EmissaryUser) error
	Save(ctx context.Context, user *EmissaryUser) error
	Get(ctx context.Context, email string) (*EmissaryUser, error)
	Delete(ctx context.Context, email string) error
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

## The user

`EmissaryUser` is a superstruct of `User`: it embeds `User` (whose
`ID`/`Email` is the email address the user logs in with — the user's
stable identifier), so it is a `User` as well: it promotes `User`'s
fields and methods, and its embedded `User` can be handed anywhere a
`User` (or `*User`) is expected. The fields it adds:

```go
type EmissaryUser struct {
	User // ID, Username, Email, Admin

	Enabled        bool     // kept, but cannot log in when false
	SaltedPassword string   // the user's stored, salted password
	Rules          []string // system rules, see below
}
```

| Field | Description |
| --- | --- |
| `Enabled` | When false, the user is kept but cannot log in. |
| `SaltedPassword` | The user's stored, salted password; verified at login. |
| `Rules` | A string array of rules applied to mail for the user (below). |

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
yet, and no backend implements `EmissaryAuthManager`.

See [AUTH.md](AUTH.md) for login, refresh and the token claims,
[API.md](API.md) for the endpoints as implemented, and
[CONFIGURATION.md](CONFIGURATION.md) for `USER_MANAGER` and the
backend settings.
