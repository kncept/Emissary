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
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
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
		Sender:       sender.NewInMemoryRecording(),
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
		Sender:       sender.NewInMemoryRecording(),
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

func TestSelfServiceChangePasswordOverHTTP(t *testing.T) {
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
		Session:      session.NewMemorySessionStore(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, adminToken, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	for _, body := range []string{
		`{"email":"user@example.com","password":"user-pass"}`,
		`{"email":"other@example.com","password":"other-pass"}`,
	} {
		if w := request(t, h, http.MethodPost, "/api/user/", body, adminToken); w.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body = %s, want 201", w.Code, w.Body.String())
		}
	}

	status, userToken, _ := login(t, h, "user@example.com", "user-pass")
	if status != http.StatusOK {
		t.Fatalf("user login status = %d, want 200", status)
	}
	claims := decodeAuthClaims(t, userToken)
	if claims.EmissaryUserManagement {
		t.Fatal("ordinary user unexpectedly has the EmissaryUserManagement claim")
	}

	// Management operations are still denied at the handler level.
	if w := request(t, h, http.MethodGet, "/api/user/", "", userToken); w.Code != http.StatusForbidden {
		t.Errorf("list users status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	// Changing somebody else's password is still denied at the handler level.
	w := request(t, h, http.MethodPost, "/api/user/other@example.com/password", `{"newPassword":"stolen"}`, userToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("reset other user status = %d, body = %s, want 403", w.Code, w.Body.String())
	}

	// The self-service path itself must work for a user without management privileges.
	w = request(t, h, http.MethodPost, "/api/user/user@example.com/password",
		`{"oldPassword":"user-pass","newPassword":"user-new"}`, userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("self password change status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if status, _, _ := login(t, h, "user@example.com", "user-pass"); status != http.StatusUnauthorized {
		t.Errorf("old password login status = %d, want 401", status)
	}
	if status, _, _ := login(t, h, "user@example.com", "user-new"); status != http.StatusOK {
		t.Errorf("new password login status = %d, want 200", status)
	}
}

func TestChangeOwnPasswordOverHTTP(t *testing.T) {
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
		Session:      session.NewMemorySessionStore(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, adminToken, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	w := request(t, h, http.MethodPost, "/api/user/",
		`{"email":"user@example.com","password":"user-pass"}`, adminToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, body = %s, want 201", w.Code, w.Body.String())
	}

	status, userToken, _ := login(t, h, "user@example.com", "user-pass")
	if status != http.StatusOK {
		t.Fatalf("user login status = %d, want 200", status)
	}
	if claims := decodeAuthClaims(t, userToken); claims.EmissaryUserManagement {
		t.Fatal("ordinary user unexpectedly has the EmissaryUserManagement claim")
	}

	const endpoint = "/api/user/change-password"
	if w := request(t, h, http.MethodPost, endpoint,
		`{"newPassword":"user-new"}`, userToken); w.Code != http.StatusBadRequest {
		t.Errorf("missing old password status = %d, body = %s, want 400", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodPost, endpoint,
		`{"oldPassword":"wrong","newPassword":"user-new"}`, userToken); w.Code != http.StatusConflict {
		t.Errorf("wrong old password status = %d, body = %s, want 409", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodPost, endpoint,
		`{"oldPassword":"user-pass","newPassword":"user-new"}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, body = %s, want 401", w.Code, w.Body.String())
	}

	w = request(t, h, http.MethodPost, endpoint,
		`{"oldPassword":"user-pass","newPassword":"user-new"}`, userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("change own password status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if status, _, _ := login(t, h, "user@example.com", "user-pass"); status != http.StatusUnauthorized {
		t.Errorf("old password login status = %d, want 401", status)
	}
	if status, _, _ := login(t, h, "user@example.com", "user-new"); status != http.StatusOK {
		t.Errorf("new password login status = %d, want 200", status)
	}
}

func TestPasswordChangeSupportedOverHTTP(t *testing.T) {
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
		Session:      session.NewMemorySessionStore(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, adminToken, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	w := request(t, h, http.MethodPost, "/api/user/",
		`{"email":"user@example.com","password":"user-pass"}`, adminToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, body = %s, want 201", w.Code, w.Body.String())
	}
	status, userToken, _ := login(t, h, "user@example.com", "user-pass")
	if status != http.StatusOK {
		t.Fatalf("user login status = %d, want 200", status)
	}
	if claims := decodeAuthClaims(t, userToken); claims.EmissaryUserManagement {
		t.Fatal("ordinary user unexpectedly has the EmissaryUserManagement claim")
	}

	w = request(t, h, http.MethodGet, "/api/user/password-change-supported", "", userToken)
	if w.Code != http.StatusOK {
		t.Fatalf("capability status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"supported":true`) {
		t.Errorf("capability body = %s, want supported=true", w.Body.String())
	}
	if w := request(t, h, http.MethodGet, "/api/user/password-change-supported", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated capability status = %d, body = %s, want 401", w.Code, w.Body.String())
	}
}

func TestPasswordChangeUnsupportedBackendReportsFalse(t *testing.T) {
	// testUserManager does not implement EmissaryAuthManager.
	h, _, _ := newHandlerForTest(t, "secret")
	status, token, _ := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200", status)
	}
	w := request(t, h, http.MethodGet, "/api/user/password-change-supported", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("capability status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"supported":false`) {
		t.Errorf("capability body = %s, want supported=false", w.Body.String())
	}
}

func TestChangeOwnPasswordUnsupportedBackend(t *testing.T) {
	// testUserManager does not implement EmissaryAuthManager: it cannot store
	// a password verifier, so the endpoint must fail rather than appear to work.
	h, _, _ := newHandlerForTest(t, "secret")
	status, token, _ := login(t, h, "admin", "secret")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200", status)
	}
	w := request(t, h, http.MethodPost, "/api/user/change-password",
		`{"oldPassword":"secret","newPassword":"new-secret"}`, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("unsupported backend status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not supported") {
		t.Errorf("unsupported backend body = %s, want an explanatory message", w.Body.String())
	}
}

func newPasswordTestHandler(t *testing.T) (*usersHandler, *usermanager.FileManager, *auth.Authenticator) {
	t.Helper()
	manager, err := usermanager.NewFile(usermanager.Config{
		AdminUsername: "admin",
		AdminPassword: "admin-pass",
		FilesystemDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewFile() error = %v", err)
	}
	authenticator := auth.NewWithUserManager(manager, session.NewMemorySessionStore(), "secret", time.Hour, "", "", "admin")
	return NewUsersHandler(manager, authenticator), manager, authenticator
}

func passwordTestContext(subject string, systemManager, userManager bool) context.Context {
	return context.WithValue(context.Background(), claimsContextKey{}, &auth.Claims{
		Subject:                subject,
		SystemManager:          systemManager,
		EmissaryUserManagement: userManager,
	})
}

func createPasswordTestUser(t *testing.T, manager *usermanager.FileManager, email, password string, systemManager, userManager bool) {
	t.Helper()
	user := &usermanager.EmissaryUser{
		User:           usermanager.User{Email: email, SystemManager: systemManager},
		Enabled:        true,
		SaltedPassword: password,
		UserManager:    userManager,
	}
	if err := manager.Create(context.Background(), user); err != nil {
		t.Fatalf("Create(%s) error = %v", email, err)
	}
}

func TestChangePasswordRequiresSystemManagerForPrivilegedTarget(t *testing.T) {
	h, manager, _ := newPasswordTestHandler(t)
	createPasswordTestUser(t, manager, "sys@example.com", "sys-pass", true, false)
	createPasswordTestUser(t, manager, "manager@example.com", "manager-pass", false, true)
	createPasswordTestUser(t, manager, "manager2@example.com", "manager2-pass", false, true)
	createPasswordTestUser(t, manager, "user@example.com", "user-pass", false, false)

	managerCtx := passwordTestContext("manager@example.com", false, true)
	for _, email := range []string{"sys@example.com", "manager2@example.com"} {
		res, err := h.ChangePassword(managerCtx, &usersapi.ChangePasswordRequest{
			NewPassword: "stolen",
		}, usersapi.ChangePasswordParams{Email: email})
		if err != nil {
			t.Fatalf("ChangePassword(%s) error = %v", email, err)
		}
		if _, ok := res.(*usersapi.ChangePasswordForbidden); !ok {
			t.Errorf("ChangePassword(%s) response = %T, want ChangePasswordForbidden", email, res)
		}
	}
	// The denied resets left the privileged accounts untouched.
	if _, err := manager.Authenticate(context.Background(), "sys@example.com", "sys-pass"); err != nil {
		t.Errorf("System Manager password was changed: Authenticate() error = %v", err)
	}
	if _, err := manager.Authenticate(context.Background(), "manager2@example.com", "manager2-pass"); err != nil {
		t.Errorf("User Manager password was changed: Authenticate() error = %v", err)
	}

	// A User Manager can still reset an ordinary account.
	res, err := h.ChangePassword(managerCtx, &usersapi.ChangePasswordRequest{
		NewPassword: "user-new",
	}, usersapi.ChangePasswordParams{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("ChangePassword(user) error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
		t.Fatalf("ChangePassword(user) response = %T, want ChangePasswordResponse", res)
	}
	if _, err := manager.Authenticate(context.Background(), "user@example.com", "user-new"); err != nil {
		t.Errorf("new password rejected: Authenticate() error = %v", err)
	}

	// A System Manager may reset privileged accounts, and users may always
	// change their own password with the current password.
	systemCtx := passwordTestContext("admin", true, true)
	for _, email := range []string{"sys@example.com", "manager2@example.com"} {
		res, err := h.ChangePassword(systemCtx, &usersapi.ChangePasswordRequest{
			NewPassword: "reset-by-sys",
		}, usersapi.ChangePasswordParams{Email: email})
		if err != nil {
			t.Fatalf("ChangePassword(%s) error = %v", email, err)
		}
		if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
			t.Errorf("ChangePassword(%s) response = %T, want ChangePasswordResponse", email, res)
		}
	}
	res, err = h.ChangePassword(passwordTestContext("manager@example.com", false, true), &usersapi.ChangePasswordRequest{
		OldPassword: usersapi.NewOptString("manager-pass"),
		NewPassword: "manager-new",
	}, usersapi.ChangePasswordParams{Email: "manager@example.com"})
	if err != nil {
		t.Fatalf("self ChangePassword() error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
		t.Fatalf("self ChangePassword() response = %T, want ChangePasswordResponse", res)
	}
}

func TestChangePasswordRevokesSessionsForEveryUser(t *testing.T) {
	h, manager, authenticator := newPasswordTestHandler(t)
	ctx := context.Background()
	createPasswordTestUser(t, manager, "user@example.com", "user-pass", false, false)
	createPasswordTestUser(t, manager, "sys@example.com", "sys-pass", true, false)
	createPasswordTestUser(t, manager, "other@example.com", "other-pass", false, false)

	store := authenticator.SessionStore()
	makeSession := func(id, email string) {
		t.Helper()
		sess := &session.Session{
			ID: id, UserEmail: email, Issue: 1,
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := store.Create(ctx, sess); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	assertSession := func(id string, wantGone bool) {
		t.Helper()
		_, err := store.Get(ctx, id)
		if wantGone && !errors.Is(err, session.ErrSessionNotFound) {
			t.Errorf("session %s still active: Get() error = %v", id, err)
		}
		if !wantGone && err != nil {
			t.Errorf("unrelated session %s was revoked: Get() error = %v", id, err)
		}
	}
	for _, s := range []struct{ id, email string }{
		{"user-1", "user@example.com"},
		{"user-2", "user@example.com"},
		{"sys-1", "sys@example.com"},
		{"other-1", "other@example.com"},
	} {
		makeSession(s.id, s.email)
	}

	// Self-service password change revokes the caller's sessions only.
	res, err := h.ChangePassword(passwordTestContext("user@example.com", false, false), &usersapi.ChangePasswordRequest{
		OldPassword: usersapi.NewOptString("user-pass"),
		NewPassword: "user-new",
	}, usersapi.ChangePasswordParams{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("self ChangePassword() error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
		t.Fatalf("self ChangePassword() response = %T, want ChangePasswordResponse", res)
	}
	assertSession("user-1", true)
	assertSession("user-2", true)
	assertSession("sys-1", false)
	assertSession("other-1", false)

	// An admin reset of a privileged account also revokes that account's sessions.
	res, err = h.ChangePassword(passwordTestContext("admin", true, true), &usersapi.ChangePasswordRequest{
		NewPassword: "sys-new",
	}, usersapi.ChangePasswordParams{Email: "sys@example.com"})
	if err != nil {
		t.Fatalf("sys reset error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
		t.Fatalf("sys reset response = %T, want ChangePasswordResponse", res)
	}
	assertSession("sys-1", true)

	// A failed password change leaves sessions alone.
	makeSession("other-2", "other@example.com")
	res, err = h.ChangePassword(passwordTestContext("other@example.com", false, false), &usersapi.ChangePasswordRequest{
		OldPassword: usersapi.NewOptString("wrong"),
		NewPassword: "other-new",
	}, usersapi.ChangePasswordParams{Email: "other@example.com"})
	if err != nil {
		t.Fatalf("failed ChangePassword() error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordConflict); !ok {
		t.Fatalf("failed ChangePassword() response = %T, want ChangePasswordConflict", res)
	}
	assertSession("other-1", false)
	assertSession("other-2", false)

	// An admin reset of an unprivileged account revokes its sessions too.
	res, err = h.ChangePassword(passwordTestContext("admin", true, true), &usersapi.ChangePasswordRequest{
		NewPassword: "other-new",
	}, usersapi.ChangePasswordParams{Email: "other@example.com"})
	if err != nil {
		t.Fatalf("other reset error = %v", err)
	}
	if _, ok := res.(*usersapi.ChangePasswordResponse); !ok {
		t.Fatalf("other reset response = %T, want ChangePasswordResponse", res)
	}
	assertSession("other-1", true)
	assertSession("other-2", true)
}

func TestUserManagerCannotEscalateToSystemManagerOverHTTP(t *testing.T) {
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
		Session:      session.NewMemorySessionStore(),
		UserManager:  manager,
		UserDomain:   "example.com",
		Sender:       sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	status, adminToken, _ := login(t, h, "admin", "admin-pass")
	if status != http.StatusOK {
		t.Fatalf("admin login status = %d, want 200", status)
	}
	for _, body := range []string{
		`{"email":"sys@example.com","password":"sys-pass","systemManager":true}`,
		`{"email":"manager@example.com","password":"manager-pass","userManager":true}`,
		`{"email":"user@example.com","password":"user-pass"}`,
	} {
		w := request(t, h, http.MethodPost, "/api/user/", body, adminToken)
		if w.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body = %s, want 201", w.Code, w.Body.String())
		}
	}

	status, _, sysLogin := login(t, h, "sys@example.com", "sys-pass")
	if status != http.StatusOK {
		t.Fatalf("sys login status = %d, want 200", status)
	}
	sysRefresh, _ := sysLogin["refreshToken"].(string)

	status, managerToken, _ := login(t, h, "manager@example.com", "manager-pass")
	if status != http.StatusOK {
		t.Fatalf("manager login status = %d, want 200", status)
	}

	w := request(t, h, http.MethodPost, "/api/user/sys@example.com/password", `{"newPassword":"stolen"}`, managerToken)
	if w.Code != http.StatusForbidden {
		t.Errorf("manager reset of System Manager status = %d, body = %s, want 403", w.Code, w.Body.String())
	}
	if status, _, _ := login(t, h, "sys@example.com", "sys-pass"); status != http.StatusOK {
		t.Errorf("System Manager password was changed by User Manager: login status = %d", status)
	}

	// Resetting an ordinary account remains allowed for a User Manager.
	w = request(t, h, http.MethodPost, "/api/user/user@example.com/password", `{"newPassword":"user-new"}`, managerToken)
	if w.Code != http.StatusOK {
		t.Errorf("manager reset of ordinary user status = %d, body = %s, want 200", w.Code, w.Body.String())
	}

	// The System Manager's own reset succeeds and kills its prior session.
	w = request(t, h, http.MethodPost, "/api/user/sys@example.com/password", `{"newPassword":"sys-new"}`, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin reset status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if w := refreshRequest(t, h, sysRefresh); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh after reset status = %d, body = %s, want 401", w.Code, w.Body.String())
	}
	if status, _, _ := login(t, h, "sys@example.com", "sys-pass"); status != http.StatusUnauthorized {
		t.Errorf("old password still accepted: login status = %d", status)
	}
	if status, _, _ := login(t, h, "sys@example.com", "sys-new"); status != http.StatusOK {
		t.Errorf("new password rejected: login status = %d", status)
	}
}
