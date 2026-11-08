// Package usermanager provides the pluggable authentication backends used by
// Emissary. The configured AUTH_USERNAME/AUTH_PASSWORD pair is always treated
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
)

// User is an authenticated principal.
type User struct {
	// ID is the stable identifier written to the JWT subject claim.
	ID string
	// Username is the name presented at login.
	Username string
	// Email is the mailbox this user owns, when the backend is email based.
	Email string
	// Admin reports whether this is the single built-in administrator.
	Admin bool
}

// Config selects and configures a user manager backend.
type Config struct {
	// Type is one of "none", "any", "ddb" or "ldap".
	Type string

	// AdminUsername and AdminPassword describe the built-in administrator.
	// When AdminPassword is empty the administrator cannot log in.
	AdminUsername string
	AdminPassword string

	// Domain is the receiving domain used by the "any" backend.
	Domain string

	// DdbTable is the DynamoDB table used by the "ddb" backend.
	DdbTable string
	Region   string

	// LDAP settings are reserved for the "ldap" backend.
	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
}

// Manager authenticates a set of users. Implementations must be safe for
// concurrent use.
type Manager interface {
	Name() string
	Authenticate(ctx context.Context, username, password string) (*User, error)
	IsAdmin(username string) bool
}

// New builds the manager selected by cfg.Type. An empty type is equivalent to
// "none" (administrator only), which preserves the original single-user
// behaviour.
func New(cfg Config) (Manager, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "none":
		return NewNone(cfg), nil
	case "any":
		return NewAny(cfg)
	case "ddb", "dynamodb":
		return NewDdb(cfg), nil
	case "ldap":
		return NewLdap(cfg), nil
	default:
		return nil, fmt.Errorf("unknown user manager type %q (want none, any, ddb or ldap)", cfg.Type)
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

var _ Manager = (*NoneManager)(nil)

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

func (m *NoneManager) IsAdmin(username string) bool { return m.admin.isAdmin(username) }

// AnyManager allows any valid address in the receiving domain to log in with
// any password. It is intended for development and trusted deployments.
type AnyManager struct {
	admin  adminAuth
	domain string
}

var _ Manager = (*AnyManager)(nil)

func NewAny(cfg Config) (*AnyManager, error) {
	domain := normalizeDomain(cfg.Domain)
	if domain == "" {
		return nil, errors.New("user manager any requires a domain (set EMISSARY_DOMAIN)")
	}
	return &AnyManager{admin: newAdminAuth(cfg), domain: domain}, nil
}

func (m *AnyManager) Name() string { return "any" }

func (m *AnyManager) Authenticate(_ context.Context, username, password string) (*User, error) {
	// The password is not checked for ordinary users, but the administrator is
	// always validated so a wrong admin password cannot fall through.
	if user, handled, err := m.admin.authenticate(username, password); handled {
		return user, err
	}

	address, ok := normalizeAddress(username)
	if !ok || !strings.HasSuffix(address, "@"+m.domain) {
		return nil, ErrInvalidCredentials
	}
	// Reject addresses such as "a@domain@domain" that merely end in the right
	// suffix but are not valid mailboxes.
	if strings.Count(address, "@") != 1 {
		return nil, ErrInvalidCredentials
	}
	return &User{ID: address, Username: address, Email: address}, nil
}

func (m *AnyManager) IsAdmin(username string) bool { return m.admin.isAdmin(username) }

// DdbManager will provide in-application user management backed by DynamoDB.
// The type is registered now so deployments can select it, but the storage
// implementation is not part of this change.
type DdbManager struct {
	admin  adminAuth
	table  string
	region string
}

var _ Manager = (*DdbManager)(nil)

func NewDdb(cfg Config) *DdbManager {
	return &DdbManager{
		admin:  newAdminAuth(cfg),
		table:  cfg.DdbTable,
		region: cfg.Region,
	}
}

func (m *DdbManager) Name() string { return "ddb" }

func (m *DdbManager) Authenticate(_ context.Context, username, password string) (*User, error) {
	if user, handled, err := m.admin.authenticate(username, password); handled {
		return user, err
	}
	panic("unimplemented")
}

func (m *DdbManager) IsAdmin(username string) bool { return m.admin.isAdmin(username) }

// LdapManager will proxy authentication to an LDAP server. The type is
// registered now so deployments can select it, but the proxy is not part of
// this change.
type LdapManager struct {
	admin adminAuth
	cfg   Config
}

var _ Manager = (*LdapManager)(nil)

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

func (m *LdapManager) IsAdmin(username string) bool { return m.admin.isAdmin(username) }

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(domain), "@"))
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
