// Package usermanager provides the pluggable authentication backends used by
// Emissary. The configured USERNAME/PASSWORD pair is always treated
// as the single built-in administrator; a backend only decides how ordinary
// (non-admin) users are authenticated.
package usermanager

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

var (
	// ErrInvalidCredentials is returned when a login attempt cannot be
	// authenticated. Callers should not distinguish between an unknown user
	// and a wrong password.
	ErrInvalidCredentials = errors.New("invalid username or password")

	// ErrUserNotFound is returned by Emissary-mastered managers when a user
	// does not exist.
	ErrUserNotFound = errors.New("user not found")

	// ErrUserExists is returned when creating a user whose id is already in
	// use.
	ErrUserExists = errors.New("user already exists")

	// ErrUserDisabled is returned when the supplied credentials are correct
	// but the account has been disabled.
	ErrUserDisabled = errors.New("user is disabled")
)

// User is an authenticated principal.
type User struct {
	// ID is the stable identifier written to the JWT subject claim. The user
	// id is the user's email address.
	ID string
	// Username is the name presented at login.
	Username string
	// Email is the user's email address (the user id).
	Email string
	// Admin reports whether this is the single built-in administrator.
	Admin bool
}

// IsAdmin returns whether this user is an administrator.
func (u *User) IsAdmin() bool {
	return u.Admin
}

// EmissaryUser is a user owned by an Emissary-mastered user manager. It embeds
// User, so it can be used anywhere a *User is expected.
type EmissaryUser struct {
	User // ID, Username, Email, Admin

	// Enabled reports whether the user may log in. A disabled user is kept
	// but rejected at login.
	Enabled bool

	// SaltedPassword is the user's stored password verifier. Passwords are
	// salted and hashed by the manager; callers supply or change a plaintext
	// password through Create and ChangePassword.
	SaltedPassword string

	// Salt is the salt/prefix identifying the password hash algorithm.
	// Empty = plaintext, b- = bcrypt, a2- = argon2, p2- = pbkdf2.
	Salt string

	// Rules are the system rules applied to mail for this user.
	Rules []string

	// UserManager reports whether the user has user management privileges
	// (can create, update, delete users via the API).
	UserManager bool

	// SystemManager reports whether the user has system management privileges
	// (can access support endpoints, masquerade as other users, etc.).
	SystemManager bool

	// SchemaVersion tracks the data schema version for migrations.
	// Defaults to "1.0.0" when creating new users. Empty string means legacy (pre-schema versioning).
	SchemaVersion string
}

// IsEnabled reports whether the user may log in.
func (u *EmissaryUser) IsEnabled() bool {
	return u.Enabled
}

// Config selects and configures a user manager backend.
type Config struct {
	// Type is one of "none", "ddb", "ldap" or "s3".
	Type string

	// AdminUsername and AdminPassword describe the built-in administrator.
	// When AdminPassword is empty the administrator cannot log in.
	AdminUsername string
	AdminPassword string

	// Domain is the receiving domain, used for mail delivery and reporting.
	Domain string

	// DdbTable is the DynamoDB table used by the "ddb" backend.
	DdbTable string
	Region   string

	// S3Bucket is the mail storage bucket used by the "s3" backend, which
	// stores each user as users/<email>/user.json next to the user's
	// mailboxes.
	S3Bucket string

	// LDAP settings are reserved for the "ldap" backend.
	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
}

// UserManager authenticates and looks up users. Implementations must be safe for
// concurrent use.
type UserManager interface {
	Name() string
	Authenticate(ctx context.Context, username, password string) (*User, error)
	Lookup(ctx context.Context, username string) (*User, error)
	// Autocomplete returns up to 10 user ID (email address) suggestions for
	// the given query.
	Autocomplete(ctx context.Context, q string) ([]string, error)
}

// EmissaryAuthManager is implemented by user managers that own their user
// store instead of delegating authentication to an external directory. The
// management operations back the /api/user/ endpoints.
type EmissaryAuthManager interface {
	UserManager

	List(ctx context.Context, cursor string, pageSize int) (users []*EmissaryUser, nextCursor string, err error)
	Create(ctx context.Context, user *EmissaryUser) error
	Save(ctx context.Context, user *EmissaryUser) error
	Get(ctx context.Context, email string) (*EmissaryUser, error)
	Delete(ctx context.Context, email string) error
	ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error
}

// New builds the manager selected by cfg.Type. An empty type is equivalent to
// "none" (administrator only), which preserves the original single-user
// behaviour.
func New(cfg Config) (UserManager, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "none":
		return NewNone(cfg), nil
	case "ddb", "dynamodb":
		return NewDdb(cfg), nil
	case "ldap":
		return NewLdap(cfg), nil
	case "s3":
		return NewS3(cfg), nil
	default:
		return nil, fmt.Errorf("unknown user manager type %q (want none, ddb, ldap or s3)", cfg.Type)
	}
}

// adminAuth implements the built-in administrator checks shared by every
// backend.
type adminAuth struct {
	username string
	password string
}

func newAdminAuth(cfg Config) adminAuth {
	return adminAuth{
		username: strings.TrimSpace(cfg.AdminUsername),
		password: cfg.AdminPassword,
	}
}

// authenticate returns (user, true, nil) when username is the administrator and
// the password is correct. When username is the administrator but the password
// is wrong (or no admin password is configured) it returns (nil, true, err) so
// callers do not accidentally fall through to a backend. A different username
// returns handled=false.
func (a adminAuth) authenticate(username, password string) (*User, bool, error) {
	if a.username == "" || !strings.EqualFold(strings.TrimSpace(username), a.username) {
		return nil, false, nil
	}
	if a.password == "" || !equal(password, a.password) {
		return nil, true, ErrInvalidCredentials
	}
	user := &User{
		ID:       a.username,
		Username: a.username,
		Admin:    true,
	}
	if isEmail(a.username) {
		user.Email = strings.ToLower(a.username)
	}
	return user, true, nil
}

func (a adminAuth) isAdmin(username string) bool {
	if a.username == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(username), a.username)
}

// NoneManager allows only the built-in administrator to log in.
type NoneManager struct {
	admin adminAuth
}

var _ UserManager = (*NoneManager)(nil)

func NewNone(cfg Config) *NoneManager {
	return &NoneManager{admin: newAdminAuth(cfg)}
}

func (m *NoneManager) Name() string { return "none" }

func (m *NoneManager) Authenticate(_ context.Context, username, password string) (*User, error) {
	if user, handled, err := m.admin.authenticate(username, password); handled {
		return user, err
	}
	return nil, ErrInvalidCredentials
}

func (m *NoneManager) Lookup(_ context.Context, username string) (*User, error) {
	if m.admin.isAdmin(username) {
		return &User{
			ID:       m.admin.username,
			Username: m.admin.username,
			Admin:    true,
		}, nil
	}
	// Return a stub user for any username (non-admin)
	return &User{
		ID:       username,
		Username: username,
		Admin:    false,
	}, nil
}

func (m *NoneManager) Autocomplete(_ context.Context, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []string{}, nil
	}
	return []string{q}, nil
}

// LdapManager will proxy authentication to an LDAP server. The type is
// registered now so deployments can select it, but the proxy is not part of
// this change.
type LdapManager struct {
	admin adminAuth
	cfg   Config
}

var _ UserManager = (*LdapManager)(nil)

func NewLdap(cfg Config) *LdapManager {
	return &LdapManager{admin: newAdminAuth(cfg), cfg: cfg}
}

func (m *LdapManager) Name() string { return "ldap" }

func (m *LdapManager) Authenticate(_ context.Context, username, password string) (*User, error) {
	if user, handled, err := m.admin.authenticate(username, password); handled {
		return user, err
	}
	panic("unimplemented")
}

func (m *LdapManager) Lookup(_ context.Context, username string) (*User, error) {
	if m.admin.isAdmin(username) {
		return &User{
			ID:       m.admin.username,
			Username: m.admin.username,
			Admin:    true,
		}, nil
	}
	panic("unimplemented")
}

func (m *LdapManager) Autocomplete(_ context.Context, _ string) ([]string, error) {
	panic("unimplemented")
}

func normalizeAddress(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" {
		return "", false
	}
	return strings.ToLower(parsed.Address), true
}

func isEmail(value string) bool {
	_, ok := normalizeAddress(value)
	return ok
}

func equal(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}
