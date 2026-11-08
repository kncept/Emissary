package usermanager

import (
	"context"
	"errors"
	"testing"
)

func TestNoneManager_AdminOnly(t *testing.T) {
	m := NewNone(Config{AdminUsername: "admin", AdminPassword: "secret"})

	user, err := m.Authenticate(context.Background(), "admin", "secret")
	if err != nil {
		t.Fatalf("admin login failed: %v", err)
	}
	if !user.Admin || user.ID != "admin" {
		t.Fatalf("admin user = %+v, want admin id", user)
	}

	if _, err := m.Authenticate(context.Background(), "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong admin password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(context.Background(), "other@example.com", "secret"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("non-admin login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestNoneManager_BlankAdminPasswordDisablesLogin(t *testing.T) {
	m := NewNone(Config{AdminUsername: "admin", AdminPassword: ""})

	if _, err := m.Authenticate(context.Background(), "admin", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("blank admin password should disable login, got %v", err)
	}
}

func TestAnyManager(t *testing.T) {
	m, err := NewAny(Config{
		AdminUsername: "admin",
		AdminPassword: "secret",
		Domain:        "example.com",
	})
	if err != nil {
		t.Fatalf("NewAny() error = %v", err)
	}

	user, err := m.Authenticate(context.Background(), "Alice@Example.com", "")
	if err != nil {
		t.Fatalf("any login failed: %v", err)
	}
	if user.Admin {
		t.Error("ordinary user must not be admin")
	}
	if user.ID != "alice@example.com" || user.Email != "alice@example.com" {
		t.Errorf("user = %+v, want normalized email", user)
	}

	if _, err := m.Authenticate(context.Background(), "alice@other.com", "anything"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong domain error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(context.Background(), "not-an-email", "anything"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("invalid address error = %v, want ErrInvalidCredentials", err)
	}

	// The administrator must still supply their real password; a blank
	// password must not fall through to the "any" backend.
	if _, err := m.Authenticate(context.Background(), "admin", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("blank admin password error = %v, want ErrInvalidCredentials", err)
	}
	if user, err := m.Authenticate(context.Background(), "admin", "secret"); err != nil || !user.Admin {
		t.Errorf("admin login = (%+v, %v), want admin", user, err)
	}
}

func TestAnyManagerRequiresDomain(t *testing.T) {
	if _, err := NewAny(Config{}); err == nil {
		t.Error("NewAny() should require a domain")
	}
}

func TestDdbAndLdapAreSelectable(t *testing.T) {
	for _, kind := range []string{"ddb", "ldap"} {
		t.Run(kind, func(t *testing.T) {
			m, err := New(Config{
				Type:          kind,
				AdminUsername: "admin",
				AdminPassword: "secret",
				DdbTable:      "users",
				LDAPURL:       "ldap://localhost:389",
			})
			if err != nil {
				t.Fatalf("New(%q) error = %v", kind, err)
			}
			if _, err := m.Authenticate(context.Background(), "admin", "secret"); err != nil {
				t.Fatalf("%s admin login failed: %v", kind, err)
			}

			defer func() {
				if recovered := recover(); recovered != "unimplemented" {
					t.Errorf("%s ordinary login panic = %v, want unimplemented", kind, recovered)
				}
			}()
			_, _ = m.Authenticate(context.Background(), "user@example.com", "password")
		})
	}
}

func TestUnknownManagerType(t *testing.T) {
	if _, err := New(Config{Type: "smtp"}); err == nil {
		t.Error("New() should reject unknown manager types")
	}
}
