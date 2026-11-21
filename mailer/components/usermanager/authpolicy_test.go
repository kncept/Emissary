package usermanager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// policyTestManager is a tiny in-memory EmissaryAuthManager. The policy tests
// exercise the shared helpers directly instead of repeating them for every
// backend; backend suites already cover their client interactions.
type policyTestManager struct {
	users     map[string]*EmissaryUser
	listCalls int
	getErr    error
}

func newPolicyTestManager() *policyTestManager {
	return &policyTestManager{users: make(map[string]*EmissaryUser)}
}

func (m *policyTestManager) add(t *testing.T, email, password string, enabled bool) {
	t.Helper()
	salt, hashed, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	m.users[email] = &EmissaryUser{
		User:           User{Email: email},
		Enabled:        enabled,
		Salt:           salt,
		SaltedPassword: hashed,
		Rules:          []string{},
	}
}

func (m *policyTestManager) Name() string { return "test" }

func (m *policyTestManager) Authenticate(context.Context, string, string) (*User, error) {
	panic("policy tests call authenticateUser directly")
}

func (m *policyTestManager) Lookup(context.Context, string) (*User, error) {
	panic("policy tests call lookupUser directly")
}

func (m *policyTestManager) Autocomplete(context.Context, string) ([]string, error) {
	panic("policy tests call autocompleteFromList directly")
}

func (m *policyTestManager) List(_ context.Context, cursor string, pageSize int) ([]*EmissaryUser, string, error) {
	m.listCalls++
	keys := make([]string, 0, len(m.users))
	for key := range m.users {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if cursor != "" {
		after, err := decodeUserCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		filtered := keys[:0]
		for _, key := range keys {
			if key > after {
				filtered = append(filtered, key)
			}
		}
		keys = filtered
	}
	next := ""
	if pageSize > 0 && len(keys) > pageSize {
		next = encodeUserCursor(keys[pageSize-1])
		keys = keys[:pageSize]
	}
	users := make([]*EmissaryUser, 0, len(keys))
	for _, key := range keys {
		users = append(users, m.users[key])
	}
	return users, next, nil
}

func (m *policyTestManager) Create(_ context.Context, user *EmissaryUser) error {
	m.users[user.Email] = user
	return nil
}

func (m *policyTestManager) Save(_ context.Context, user *EmissaryUser) error {
	m.users[user.Email] = user
	return nil
}

func (m *policyTestManager) Get(_ context.Context, email string) (*EmissaryUser, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	user, ok := m.users[email]
	if !ok {
		return nil, ErrUserNotFound
	}
	return user, nil
}

func (m *policyTestManager) Delete(_ context.Context, email string) error {
	delete(m.users, email)
	return nil
}

func (m *policyTestManager) ChangePassword(context.Context, string, string, string) error {
	panic("policy tests call changePassword directly")
}

func policyAdmin() adminAuth {
	return newAdminAuth(Config{AdminUsername: "Admin@Example.com", AdminPassword: "admin-pass"})
}

func TestAuthenticateUserPolicy(t *testing.T) {
	manager := newPolicyTestManager()
	manager.add(t, "user@example.com", "secret", true)
	manager.add(t, "disabled@example.com", "secret", false)
	admin := policyAdmin()

	tests := []struct {
		name     string
		username string
		password string
		want     string
		wantErr  error
	}{
		{name: "admin", username: "admin@example.com", password: "admin-pass", want: "admin@example.com"},
		{name: "admin case insensitive", username: " ADMIN@EXAMPLE.COM ", password: "admin-pass", want: "admin@example.com"},
		{name: "admin wrong password", username: "admin@example.com", password: "wrong", wantErr: ErrInvalidCredentials},
		{name: "managed user", username: "User@Example.com", password: "secret", want: "user@example.com"},
		{name: "unknown user", username: "missing@example.com", password: "secret", wantErr: ErrInvalidCredentials},
		{name: "malformed address", username: "not-an-address", password: "secret", wantErr: ErrInvalidCredentials},
		{name: "wrong password", username: "user@example.com", password: "wrong", wantErr: ErrInvalidCredentials},
		{name: "disabled wrong password", username: "disabled@example.com", password: "wrong", wantErr: ErrInvalidCredentials},
		{name: "disabled correct password", username: "disabled@example.com", password: "secret", wantErr: ErrUserDisabled},
		{name: "empty password", username: "user@example.com", password: "", wantErr: ErrInvalidCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := authenticateUser(context.Background(), manager, admin, tt.username, tt.password)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("authenticateUser() error = %v, want %v", err, tt.wantErr)
				}
				if user != nil {
					t.Errorf("authenticateUser() user = %+v, want nil", user)
				}
				return
			}
			if err != nil {
				t.Fatalf("authenticateUser() error = %v", err)
			}
			if user == nil || user.Email != tt.want || user.SystemManager != (tt.want == "admin@example.com") {
				t.Errorf("authenticateUser() = %+v, want email %q", user, tt.want)
			}
		})
	}

	manager.getErr = errors.New("store down")
	if _, err := authenticateUser(context.Background(), manager, admin, "user@example.com", "secret"); err == nil {
		t.Error("authenticateUser() error = nil, want backend error")
	}
}

func TestLookupUserPolicy(t *testing.T) {
	manager := newPolicyTestManager()
	manager.add(t, "user@example.com", "secret", true)
	manager.add(t, "disabled@example.com", "secret", false)
	admin := policyAdmin()

	tests := []struct {
		name     string
		username string
		want     string
		wantErr  error
	}{
		{name: "admin", username: "admin@example.com", want: "admin@example.com"},
		{name: "managed user", username: "user@example.com", want: "user@example.com"},
		{name: "unknown user", username: "missing@example.com", wantErr: ErrInvalidCredentials},
		{name: "malformed address", username: "not-an-address", wantErr: ErrInvalidCredentials},
		{name: "disabled user", username: "disabled@example.com", wantErr: ErrUserDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := lookupUser(context.Background(), manager, admin, tt.username)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("lookupUser() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("lookupUser() error = %v", err)
			}
			if user == nil || user.Email != tt.want {
				t.Errorf("lookupUser() = %+v, want email %q", user, tt.want)
			}
		})
	}
}

func TestAutocompleteFromListPolicy(t *testing.T) {
	manager := newPolicyTestManager()
	// Fill a first page with non-matches so the helper must follow the cursor.
	for i := 0; i < 100; i++ {
		email := fmt.Sprintf("alpha%03d@example.com", i)
		manager.users[email] = &EmissaryUser{User: User{Email: email}, Enabled: true}
	}
	for i := 0; i < 12; i++ {
		manager.add(t, fmt.Sprintf("user%02d@example.com", i), "secret", true)
	}

	got, err := autocompleteFromList(context.Background(), manager, "  USER  ")
	if err != nil {
		t.Fatalf("autocompleteFromList() error = %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("autocompleteFromList() returned %d matches, want 10", len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, email := range got {
		if seen[email] {
			t.Errorf("autocompleteFromList() returned duplicate %q", email)
		}
		seen[email] = true
		if !strings.Contains(strings.ToLower(email), "user") {
			t.Errorf("autocompleteFromList() returned non-match %q", email)
		}
	}
	if manager.listCalls < 2 {
		t.Errorf("List calls = %d, want paginated calls", manager.listCalls)
	}

	empty, err := autocompleteFromList(context.Background(), manager, "  ")
	if err != nil {
		t.Fatalf("autocompleteFromList(empty) error = %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("autocompleteFromList(empty) = %#v, want non-nil empty slice", empty)
	}
}

func TestPrepareForCreateAndSavePolicy(t *testing.T) {
	createTests := []struct {
		name       string
		user       *EmissaryUser
		wantErr    string
		wantEmail  string
		wantHash   bool
		wantNoSalt bool
	}{
		{name: "nil", user: nil, wantErr: "user is required"},
		{name: "missing email", user: &EmissaryUser{}, wantErr: "user email is required"},
		{name: "invalid address", user: &EmissaryUser{User: User{Email: "not-an-address"}}, wantErr: "user email must be a valid address"},
		{
			name:      "normalises and hashes",
			user:      &EmissaryUser{User: User{Email: "  User@Example.com  "}, SaltedPassword: "secret"},
			wantEmail: "user@example.com",
			wantHash:  true,
		},
		{
			name:       "blank password clears salt",
			user:       &EmissaryUser{User: User{Email: "user@example.com"}, Salt: "stale"},
			wantEmail:  "user@example.com",
			wantNoSalt: true,
		},
	}
	for _, tt := range createTests {
		t.Run("create/"+tt.name, func(t *testing.T) {
			err := prepareForCreate(tt.user)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("prepareForCreate() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepareForCreate() error = %v", err)
			}
			if tt.user.Email != tt.wantEmail {
				t.Errorf("Email = %q, want %q", tt.user.Email, tt.wantEmail)
			}
			if tt.wantHash && !checkPassword(tt.user.Salt, tt.user.SaltedPassword, "secret") {
				t.Error("plaintext password was not hashed")
			}
			if tt.wantNoSalt && tt.user.Salt != "" {
				t.Errorf("Salt = %q, want empty", tt.user.Salt)
			}
			if tt.user.SchemaVersion != "" {
				t.Errorf("SchemaVersion = %q, want empty (applyCreateSchemaVersion is backend-specific)", tt.user.SchemaVersion)
			}
		})
	}

	saveUser := &EmissaryUser{User: User{Email: " User@Example.com "}, Salt: "stale"}
	if err := prepareForSave(saveUser); err != nil {
		t.Fatalf("prepareForSave() error = %v", err)
	}
	if saveUser.Email != "user@example.com" || saveUser.Salt != "" {
		t.Errorf("prepareForSave() = %+v, want normalised email and no salt", saveUser)
	}
	if err := prepareForSave(nil); err == nil {
		t.Error("prepareForSave(nil) error = nil, want error")
	}
}

func TestChangePasswordPolicy(t *testing.T) {
	manager := newPolicyTestManager()
	manager.add(t, "user@example.com", "old-secret", true)

	tests := []struct {
		name    string
		email   string
		old     string
		newPass string
		wantErr error
	}{
		{name: "blank new password", email: "user@example.com", old: "old-secret", newPass: "   ", wantErr: errors.New("new password is required")},
		{name: "unknown user", email: "missing@example.com", old: "", newPass: "new-secret", wantErr: ErrUserNotFound},
		{name: "wrong old password", email: "user@example.com", old: "wrong", newPass: "new-secret", wantErr: ErrInvalidCredentials},
		{name: "correct old password", email: "user@example.com", old: "old-secret", newPass: "new-secret"},
		{name: "system-manager reset", email: "user@example.com", old: "", newPass: "reset-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := changePassword(context.Background(), manager, tt.email, tt.old, tt.newPass)
			if tt.wantErr != nil {
				if err.Error() != tt.wantErr.Error() && !errors.Is(err, tt.wantErr) {
					t.Fatalf("changePassword() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("changePassword() error = %v", err)
			}
			user := manager.users[tt.email]
			if !checkPassword(user.Salt, user.SaltedPassword, tt.newPass) {
				t.Error("new password was not stored")
			}
			if checkPassword(user.Salt, user.SaltedPassword, "old-secret") {
				t.Error("old password still verifies")
			}
		})
	}
}

func TestPageParamsPolicy(t *testing.T) {
	tests := []struct {
		name       string
		cursor     string
		pageSize   int
		trim       bool
		wantAfter  string
		wantSize   int
		wantBadErr bool
	}{
		{name: "defaults", cursor: "  ", pageSize: 0, wantSize: defaultUserPageSize},
		{name: "clamped high", cursor: "", pageSize: maxUserPageSize + 1, wantSize: maxUserPageSize},
		{name: "padded cursor trimmed", cursor: "  " + encodeUserCursor("user@example.com") + "  ", pageSize: 2, trim: true, wantAfter: "user@example.com", wantSize: 2},
		{name: "padded cursor rejected when not trimmed", cursor: " " + encodeUserCursor("user@example.com"), pageSize: 2, wantBadErr: true},
		{name: "malformed cursor", cursor: "not-base64!!", pageSize: 2, wantBadErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			after, size, err := pageParams(tt.cursor, tt.pageSize, tt.trim)
			if tt.wantBadErr {
				if err == nil {
					t.Fatal("pageParams() error = nil, want invalid cursor")
				}
				return
			}
			if err != nil {
				t.Fatalf("pageParams() error = %v", err)
			}
			if after != tt.wantAfter || size != tt.wantSize {
				t.Errorf("pageParams() = (%q, %d), want (%q, %d)", after, size, tt.wantAfter, tt.wantSize)
			}
		})
	}

	if got := encodeUserCursor("user@example.com"); got != "dXNlckBleGFtcGxlLmNvbQ" {
		t.Errorf("encodeUserCursor() = %q, want raw base64url", got)
	}
	if _, err := decodeUserCursor("ICAgIA"); err == nil {
		t.Error("decodeUserCursor(whitespace) error = nil, want invalid cursor")
	}
}
