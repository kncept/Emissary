package usermanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestFileManager(t *testing.T) (*FileManager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "users")
	m, err := NewFile(Config{AdminUsername: "admin", AdminPassword: "admin-pass", FilesystemDir: dir})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	return m, dir
}

func TestFileManagerCRUD(t *testing.T) {
	ctx := context.Background()
	m, dir := newTestFileManager(t)
	if m.Name() != "filesystem" {
		t.Errorf("Name() = %q, want filesystem", m.Name())
	}

	user := &EmissaryUser{
		User:           User{Email: "Alice@Example.com"},
		Enabled:        true,
		SaltedPassword: "secret-pass",
	}
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Create() email = %q", user.Email)
	}
	record := filepath.Join(dir, "alice@example.com", userRecordName)
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("user record %q missing: %v", record, err)
	}
	if user.Salt == "" || user.SaltedPassword == "secret-pass" {
		t.Errorf("Create() did not hash password: salt=%q hash=%q", user.Salt, user.SaltedPassword)
	}
	if err := m.Create(ctx, user); !errors.Is(err, ErrUserExists) {
		t.Errorf("Create() duplicate = %v, want ErrUserExists", err)
	}

	got, err := m.Get(ctx, "alice@example.com")
	if err != nil || !got.Enabled || got.Rules == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	principal, err := m.Authenticate(ctx, "alice@example.com", "secret-pass")
	if err != nil || principal.Email != "alice@example.com" {
		t.Fatalf("Authenticate() = %+v, %v", principal, err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("bad password = %v", err)
	}
	lookup, err := m.Lookup(ctx, "alice@example.com")
	if err != nil || lookup.Email != "alice@example.com" {
		t.Errorf("Lookup() = %+v, %v", lookup, err)
	}

	got.Enabled = false
	if err := m.Save(ctx, got); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "secret-pass"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("disabled login = %v, want ErrUserDisabled", err)
	}
	got.Enabled = true
	if err := m.Save(ctx, got); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := m.ChangePassword(ctx, "alice@example.com", "secret-pass", "new-pass"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "secret-pass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "new-pass"); err != nil {
		t.Errorf("new password failed: %v", err)
	}

	// The built-in administrator works with every backend.
	admin, err := m.Authenticate(ctx, "admin", "admin-pass")
	if err != nil || !admin.SystemManager {
		t.Errorf("admin Authenticate() = %+v, %v", admin, err)
	}

	if err := m.Delete(ctx, "alice@example.com"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := m.Get(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Get() after delete = %v", err)
	}
	if err := m.Delete(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Delete() again = %v", err)
	}
}

func TestFileManagerListAndAutocomplete(t *testing.T) {
	ctx := context.Background()
	m, dir := newTestFileManager(t)
	for _, email := range []string{"bob@example.com", "alice@example.com", "carol@example.net"} {
		if err := m.Create(ctx, &EmissaryUser{User: User{Email: email}, Enabled: true, SaltedPassword: "pw"}); err != nil {
			t.Fatalf("Create(%q) error = %v", email, err)
		}
	}
	// A directory without a user.json is not a user.
	if err := os.MkdirAll(filepath.Join(dir, "nobody@example.com"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	first, cursor, err := m.List(ctx, "", 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(first) != 2 || cursor == "" {
		t.Fatalf("List() = %d users, cursor %q", len(first), cursor)
	}
	second, cursor2, err := m.List(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("List(next) error = %v", err)
	}
	if len(second) != 1 || cursor2 != "" {
		t.Fatalf("List(next) = %d users, cursor %q", len(second), cursor2)
	}
	seen := map[string]bool{}
	for _, u := range append(first, second...) {
		seen[u.Email] = true
	}
	if len(seen) != 3 || !seen["alice@example.com"] || !seen["bob@example.com"] || !seen["carol@example.net"] {
		t.Errorf("List() users = %v", seen)
	}

	matches, err := m.Autocomplete(ctx, "example.com")
	if err != nil || len(matches) != 2 {
		t.Errorf("Autocomplete() = %v, %v", matches, err)
	}
	if _, _, err := m.List(ctx, "!!!not-base64!!!", 2); err == nil {
		t.Error("List() with invalid cursor succeeded")
	}
}

func TestFileManagerRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestFileManager(t)

	if _, err := m.Get(ctx, "../outside"); err == nil {
		t.Error("Get() traversal succeeded")
	}
	if err := m.Create(ctx, &EmissaryUser{User: User{Email: "../outside"}, SaltedPassword: "pw"}); err == nil {
		t.Error("Create() traversal succeeded")
	}
	if err := m.Create(ctx, &EmissaryUser{User: User{Email: "not-an-address"}, SaltedPassword: "pw"}); err == nil {
		t.Error("Create() invalid address succeeded")
	}
}

func TestNew_SystemManagerBackendFromFactory(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "users")
	users, err := New(Config{Type: "filesystem", FilesystemDir: dir})
	if err != nil {
		t.Fatalf("New(filesystem) error = %v", err)
	}
	if users.Name() != "filesystem" {
		t.Errorf("Name() = %q", users.Name())
	}
	if err := users.(EmissaryAuthManager).Create(ctx, &EmissaryUser{User: User{Email: "dave@example.com"}, Enabled: true, SaltedPassword: "pw"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dave@example.com", userRecordName)); err != nil {
		t.Errorf("user record missing: %v", err)
	}
}
