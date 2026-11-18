package handler

import (
	"context"
	"errors"
	"net/http"
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
