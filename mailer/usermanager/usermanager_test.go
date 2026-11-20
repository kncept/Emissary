package usermanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNoneManager_AdminOnly(t *testing.T) {
	m := NewNone(Config{AdminUsername: "admin", AdminPassword: "secret"})

	user, err := m.Authenticate(context.Background(), "admin", "secret")
	if err != nil {
		t.Fatalf("admin login failed: %v", err)
	}
	if !user.SystemManager || user.Email != "admin" {
		t.Fatalf("admin user = %+v, want admin email and SystemManager", user)
	}

	if _, err := m.Authenticate(context.Background(), "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong admin password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(context.Background(), "other@example.com", "secret"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("non-admin login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAdminPasswordUsesBcryptWorkFactor(t *testing.T) {
	a := newAdminAuth(Config{AdminUsername: "admin", AdminPassword: "secret"})
	if _, handled, err := a.authenticate("admin", "secret"); !handled || err != nil {
		t.Fatalf("admin authenticate() = handled %v, err %v; want true, nil", handled, err)
	}
	cost, err := bcrypt.Cost(a.verifier.hash)
	if err != nil {
		t.Fatalf("admin verifier is not a bcrypt hash: %v", err)
	}
	if cost != bcryptCost {
		t.Errorf("admin bcrypt cost = %d, want %d", cost, bcryptCost)
	}
	if _, _, err := a.authenticate("admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong admin password error = %v, want ErrInvalidCredentials", err)
	}

	// bcrypt truncates input past 72 bytes; pre-hashing must keep long
	// administrator passwords distinct from their 72-byte prefix.
	long := strings.Repeat("x", 100)
	a = newAdminAuth(Config{AdminUsername: "admin", AdminPassword: long})
	if _, _, err := a.authenticate("admin", long); err != nil {
		t.Errorf("long admin password rejected: %v", err)
	}
	if _, _, err := a.authenticate("admin", long[:72]); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("truncated admin password accepted, error = %v", err)
	}
}

func TestNoneManager_BlankAdminPasswordDisablesLogin(t *testing.T) {
	m := NewNone(Config{AdminUsername: "admin", AdminPassword: ""})

	if _, err := m.Authenticate(context.Background(), "admin", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("blank admin password should disable login, got %v", err)
	}
}

func TestLdapIsSelectableButUnimplemented(t *testing.T) {
	m, err := New(Config{
		Type:          "ldap",
		AdminUsername: "admin",
		AdminPassword: "secret",
		LDAPURL:       "ldap://localhost:389",
	})
	if err != nil {
		t.Fatalf("New(%q) error = %v", "ldap", err)
	}
	if _, err := m.Authenticate(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("%s admin login failed: %v", "ldap", err)
	}

	defer func() {
		if recovered := recover(); recovered != "unimplemented" {
			t.Errorf("%s ordinary login panic = %v, want unimplemented", "ldap", recovered)
		}
	}()
	_, _ = m.Authenticate(context.Background(), "user@example.com", "password")
}

func TestDdbIsSelectable(t *testing.T) {
	m, err := New(Config{
		Type:          "ddb",
		AdminUsername: "admin",
		AdminPassword: "secret",
		DdbTable:      "users",
	})
	if err != nil {
		t.Fatalf("New(%q) error = %v", "ddb", err)
	}
	ddb, ok := m.(*DdbManager)
	if !ok {
		t.Fatalf("New(%q) = %T, want *DdbManager", "ddb", m)
	}
	if _, ok := any(ddb).(EmissaryAuthManager); !ok {
		t.Error("DdbManager must implement EmissaryAuthManager")
	}
	if _, err := m.Authenticate(context.Background(), "admin", "secret"); err != nil {
		t.Fatalf("ddb admin login failed: %v", err)
	}
}

func TestUnknownManagerType(t *testing.T) {
	for _, managerType := range []string{"any", "smtp"} {
		t.Run(managerType, func(t *testing.T) {
			if _, err := New(Config{Type: managerType}); err == nil {
				t.Errorf("New(%q) should reject unsupported manager type", managerType)
			}
		})
	}
}
