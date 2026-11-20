package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

func TestCreateUserWithoutPassword(t *testing.T) {
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	var emissaryManager usermanager.EmissaryAuthManager = manager
	h := NewUsersHandler(emissaryManager, nil)
	ctx := context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{
		Subject:                "admin",
		SystemManager:          true,
		EmissaryUserManagement: true,
	})

	for _, tc := range []struct {
		name     string
		email    string
		password usersapi.OptString
	}{
		{"blank password", "blank@example.com", usersapi.NewOptString("")},
		{"omitted password", "omitted@example.com", usersapi.OptString{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := tc.email
			res, err := h.CreateUser(ctx, &usersapi.CreateUserRequest{
				Email:    email,
				Password: tc.password,
			})
			if err != nil {
				t.Fatalf("CreateUser() error = %v", err)
			}
			if _, ok := res.(*usersapi.UserResponse); !ok {
				t.Fatalf("CreateUser() response = %T, want UserResponse", res)
			}

			user, err := emissaryManager.Get(ctx, email)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if user.Salt != "" || user.SaltedPassword != "" {
				t.Errorf("created user has password material: salt=%q hash=%q", user.Salt, user.SaltedPassword)
			}
			if _, err := emissaryManager.Authenticate(ctx, email, ""); !errors.Is(err, usermanager.ErrInvalidCredentials) {
				t.Errorf("blank login = %v, want ErrInvalidCredentials", err)
			}
			if _, err := emissaryManager.Authenticate(ctx, email, "guess"); !errors.Is(err, usermanager.ErrInvalidCredentials) {
				t.Errorf("guessed login = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestCreateUserInvalidEmail(t *testing.T) {
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	h := NewUsersHandler(manager, nil)
	ctx := context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{
		Subject:                "admin",
		SystemManager:          true,
		EmissaryUserManagement: true,
	})

	for _, email := range []string{
		"no-at-sign.example.com",
		"@example.com",
		"user@",
		"user@@example.com",
		"@",
	} {
		t.Run(email, func(t *testing.T) {
			res, err := h.CreateUser(ctx, &usersapi.CreateUserRequest{Email: email})
			if err != nil {
				t.Fatalf("CreateUser() error = %v", err)
			}
			bad, ok := res.(*usersapi.CreateUserBadRequest)
			if !ok {
				t.Fatalf("CreateUser() response = %T, want CreateUserBadRequest", res)
			}
			if bad.Error == "" {
				t.Error("CreateUser() error message is blank")
			}
		})
	}
}

func TestNoPasswordUserOverHTTP(t *testing.T) {
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "admin-pass",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       &captureSender{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, token, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	for _, tc := range []struct {
		email string
		body  string
	}{
		{"blank@example.com", `{"email":"blank@example.com","password":""}`},
		{"omitted@example.com", `{"email":"omitted@example.com"}`},
	} {
		w := request(t, h, http.MethodPost, "/api/user/", tc.body, token)
		if w.Code != http.StatusCreated {
			t.Errorf("create %s status = %d, body = %s", tc.email, w.Code, w.Body.String())
		}
		user, err := manager.Get(context.Background(), tc.email)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", tc.email, err)
		}
		if user.Salt != "" || user.SaltedPassword != "" {
			t.Errorf("created %s has password material: salt=%q hash=%q", tc.email, user.Salt, user.SaltedPassword)
		}
	}
	for _, password := range []string{"", "guess"} {
		status, _, _ := login(t, h, "blank@example.com", password)
		if status != http.StatusUnauthorized {
			t.Errorf("login with password %q status = %d, want 401", password, status)
		}
	}
}

func TestDeleteUserRejectsSelfDeletion(t *testing.T) {
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	h := NewUsersHandler(manager, nil)
	ctx := context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{
		Subject:                "admin@example.com",
		EmissaryUserManagement: true,
	})
	for _, email := range []string{"admin@example.com", "other@example.com"} {
		if _, err := h.CreateUser(ctx, &usersapi.CreateUserRequest{Email: email}); err != nil {
			t.Fatalf("CreateUser(%q) error = %v", email, err)
		}
	}

	for _, email := range []string{"admin@example.com", "ADMIN@EXAMPLE.COM"} {
		res, err := h.DeleteUser(ctx, usersapi.DeleteUserParams{Email: email})
		if err != nil {
			t.Fatalf("DeleteUser(%q) error = %v", email, err)
		}
		if _, ok := res.(*usersapi.DeleteUserForbidden); !ok {
			t.Errorf("DeleteUser(%q) response = %T, want DeleteUserForbidden", email, res)
		}
		if _, err := manager.Get(ctx, "admin@example.com"); err != nil {
			t.Errorf("self-deletion removed the account: Get() error = %v", err)
		}
	}

	res, err := h.DeleteUser(ctx, usersapi.DeleteUserParams{Email: "other@example.com"})
	if err != nil {
		t.Fatalf("DeleteUser(other) error = %v", err)
	}
	if _, ok := res.(*usersapi.DeleteUserResponse); !ok {
		t.Fatalf("DeleteUser(other) response = %T, want DeleteUserResponse", res)
	}
	if _, err := manager.Get(ctx, "other@example.com"); !errors.Is(err, usermanager.ErrUserNotFound) {
		t.Errorf("Get(other) error = %v, want ErrUserNotFound", err)
	}
}

func TestDeleteSelfOverHTTP(t *testing.T) {
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	h, err := New(HandlerConfig{
		AuthUser:     "admin",
		AuthPass:     "admin-pass",
		JWTSecret:    "secret",
		JWTSecretTTL: time.Hour,
		MetadataType: "memory",
		Storage:      storage.NewMemoryStorage(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       &captureSender{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, adminToken, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	for _, body := range []string{
		`{"email":"manager@example.com","password":"manager-pass","userManager":true}`,
		`{"email":"other@example.com"}`,
	} {
		w := request(t, h, http.MethodPost, "/api/user/", body, adminToken)
		if w.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body = %s, want 201", w.Code, w.Body.String())
		}
	}

	status, token, _ := login(t, h, "manager@example.com", "manager-pass")
	if status != http.StatusOK {
		t.Fatalf("manager login status = %d, want 200", status)
	}

	w := request(t, h, http.MethodDelete, "/api/user/manager@example.com", "", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("self-delete status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot delete your own account") {
		t.Errorf("self-delete body = %s, want self-deletion message", w.Body.String())
	}

	w = request(t, h, http.MethodDelete, "/api/user/other@example.com", "", token)
	if w.Code != http.StatusOK {
		t.Errorf("delete other user status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
}
