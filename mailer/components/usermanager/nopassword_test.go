package usermanager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestCheckPasswordRejectsBlankCredentials(t *testing.T) {
	tests := []struct {
		name     string
		salt     string
		stored   string
		password string
	}{
		{"no password user, blank attempt", "", "", ""},
		{"no password user, guessed password", "", "", "guess"},
		{"password user, blank attempt", "b-", "$2a$12$not-a-real-hash", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if checkPassword(tc.salt, tc.stored, tc.password) {
				t.Error("checkPassword() = true, want false")
			}
		})
	}

	// Legacy plaintext users with a real password must keep working.
	if !checkPassword("", "legacy-secret", "legacy-secret") {
		t.Error("non-empty legacy plaintext password no longer verifies")
	}
}

func TestFileManagerNoPasswordUser(t *testing.T) {
	ctx := context.Background()
	m, dir := newTestFileManager(t)
	user := &EmissaryUser{
		User:    User{Email: "nopass@example.com"},
		Enabled: true,
		Salt:    "stale-salt",
	}
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if user.Salt != "" || user.SaltedPassword != "" {
		t.Errorf("Create() kept password material: salt=%q hash=%q", user.Salt, user.SaltedPassword)
	}

	body, err := os.ReadFile(filepath.Join(dir, "nopass@example.com", userRecordName))
	if err != nil {
		t.Fatalf("read user record: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("decode user record: %v", err)
	}
	if stored["Salt"] != "" || stored["SaltedPassword"] != "" {
		t.Errorf("stored record has password material: %v", stored)
	}

	for _, password := range []string{"", "guess"} {
		if _, err := m.Authenticate(ctx, "nopass@example.com", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalidCredentials", password, err)
		}
	}

	// Saving the account must not reintroduce a salt for a blank password.
	reloaded, err := m.Get(ctx, "nopass@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	reloaded.Salt = "stale-salt"
	if err := m.Save(ctx, reloaded); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	reloaded, err = m.Get(ctx, "nopass@example.com")
	if err != nil {
		t.Fatalf("Get() after Save error = %v", err)
	}
	if reloaded.Salt != "" {
		t.Errorf("Save() persisted salt %q for a blank password", reloaded.Salt)
	}

	// A system manager can still give the account a password later.
	if err := m.ChangePassword(ctx, "nopass@example.com", "", "new-secret"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "nopass@example.com", "new-secret"); err != nil {
		t.Errorf("Authenticate() with new password = %v", err)
	}
}

func TestDdbManagerNoPasswordUser(t *testing.T) {
	ctx := context.Background()
	m, fake := newDdbTestManager()
	user := testUser("nopass@example.com", "", true)
	user.Salt = "stale-salt"
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	item := fake.items["nopass@example.com"]
	for _, field := range []string{"salt", "saltedPassword"} {
		value, ok := item[field].(*types.AttributeValueMemberS)
		if !ok {
			t.Fatalf("stored %s = %v, want string attribute", field, item[field])
		}
		if value.Value != "" {
			t.Errorf("stored %s = %q, want empty", field, value.Value)
		}
	}
	for _, password := range []string{"", "guess"} {
		if _, err := m.Authenticate(ctx, "nopass@example.com", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalidCredentials", password, err)
		}
	}
}

func TestS3ManagerNoPasswordUser(t *testing.T) {
	ctx := context.Background()
	m, fake := newS3TestManager()
	user := testUser("nopass@example.com", "", true)
	user.Salt = "stale-salt"
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	body, ok := fake.blob["users/nopass@example.com/user.json"]
	if !ok {
		t.Fatal("user object was not created")
	}
	var stored map[string]any
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("decode user object: %v", err)
	}
	if stored["Salt"] != "" || stored["SaltedPassword"] != "" {
		t.Errorf("stored object has password material: %v", stored)
	}
	for _, password := range []string{"", "guess"} {
		if _, err := m.Authenticate(ctx, "nopass@example.com", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalidCredentials", password, err)
		}
	}
}
