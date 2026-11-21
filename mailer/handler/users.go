package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

type usersHandler struct {
	manager EmissaryAuthManager
	auth    *auth.Authenticator
}

func NewUsersHandler(manager EmissaryAuthManager, authenticator *auth.Authenticator) *usersHandler {
	return &usersHandler{
		manager: manager,
		auth:    authenticator,
	}
}

func (h *usersHandler) ListUsers(ctx context.Context, params usersapi.ListUsersParams) (usersapi.ListUsersRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.ListUsersForbidden{Error: "insufficient permissions"}, nil
	}

	cursor := ""
	if params.Cursor.IsSet() {
		cursor = params.Cursor.Value
	}

	pageSize := 20
	if params.PageSize.IsSet() {
		pageSize = params.PageSize.Value
		if pageSize < 1 {
			pageSize = 1
		}
		if pageSize > 100 {
			pageSize = 100
		}
	}

	users, nextCursor, err := h.manager.List(ctx, cursor, pageSize)
	if err != nil {
		slog.Error("List users failed", "err", err)
		return &usersapi.ListUsersForbidden{Error: "failed to list users"}, nil
	}
	slog.Debug("Listed users", "count", len(users))

	response := &usersapi.UsersListResponse{
		Users: make([]usersapi.User, 0, len(users)),
	}
	for _, u := range users {
		response.Users = append(response.Users, usersapi.User{
			Email:         u.Email,
			Enabled:       u.Enabled,
			SystemManager: u.SystemManager,
			Rules:         u.Rules,
			UserManager:   u.UserManager,
		})
	}

	if nextCursor != "" {
		response.NextCursor = usersapi.NewOptString(nextCursor)
	}

	return response, nil
}

func (h *usersHandler) CreateUser(ctx context.Context, req *usersapi.CreateUserRequest) (usersapi.CreateUserRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.CreateUserForbidden{Error: "insufficient permissions"}, nil
	}

	// SystemManager can only be assigned by a user who also has SystemManager permission
	systemManager := false
	if req.SystemManager.IsSet() {
		systemManager = req.SystemManager.Value
		if systemManager && !claims.SystemManager {
			return &usersapi.CreateUserForbidden{Error: "insufficient permissions to assign SystemManager role"}, nil
		}
	}

	if req.Email == "" {
		return &usersapi.CreateUserBadRequest{Error: "email is required"}, nil
	}
	// Match the frontend email input rule: an @ with content before and after it.
	at := strings.LastIndexByte(req.Email, '@')
	if at <= 0 || at == len(req.Email)-1 || strings.Contains(req.Email[:at], "@") {
		return &usersapi.CreateUserBadRequest{Error: "email must contain an @ with content before and after it"}, nil
	}

	// An omitted or blank password creates an account that cannot log in.
	password := req.Password.Or("")

	enabled := true
	if req.Enabled.IsSet() {
		enabled = req.Enabled.Value
	}

	userManager := false
	if req.UserManager.IsSet() {
		userManager = req.UserManager.Value
	}

	rules := req.Rules
	if rules == nil {
		rules = []string{}
	}

	user := &EmissaryUser{
		User: usermanager.User{
			Email:         req.Email,
			SystemManager: systemManager,
		},
		Enabled:        enabled,
		SaltedPassword: password,
		Rules:          rules,
		UserManager:    userManager,
	}

	if err := h.manager.Create(ctx, user); err != nil {
		if errors.Is(err, usermanager.ErrUserExists) {
			slog.Warn("Create user: user already exists", "email", user.Email)
			return &usersapi.CreateUserConflict{Error: "user already exists"}, nil
		}
		slog.Error("Create user failed", "email", user.Email, "err", err)
		return &usersapi.CreateUserBadRequest{Error: "failed to create user"}, nil
	}
	slog.Info("User created", "email", user.Email)

	return &usersapi.UserResponse{
		Email:         user.Email,
		Enabled:       user.Enabled,
		SystemManager: user.SystemManager,
		UserManager:   user.UserManager,
		Rules:         user.Rules,
	}, nil
}

func (h *usersHandler) GetUser(ctx context.Context, params usersapi.GetUserParams) (usersapi.GetUserRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.GetUserForbidden{Error: "insufficient permissions"}, nil
	}

	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		if errors.Is(err, usermanager.ErrUserNotFound) {
			slog.Debug("Get user: not found", "email", params.Email)
		} else {
			slog.Error("Get user failed", "email", params.Email, "err", err)
		}
		return &usersapi.GetUserNotFound{Error: "user not found"}, nil
	}
	slog.Debug("Got user", "email", params.Email)

	return &usersapi.UserResponse{
		Email:         user.Email,
		Enabled:       user.Enabled,
		SystemManager: user.SystemManager,
		UserManager:   user.UserManager,
		Rules:         user.Rules,
	}, nil
}

func (h *usersHandler) UpdateUser(ctx context.Context, req *usersapi.UpdateUserRequest, params usersapi.UpdateUserParams) (usersapi.UpdateUserRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.UpdateUserForbidden{Error: "insufficient permissions"}, nil
	}

	// SystemManager can only be assigned by a user who also has SystemManager permission
	if req.SystemManager.IsSet() && req.SystemManager.Value && !claims.SystemManager {
		return &usersapi.UpdateUserForbidden{Error: "insufficient permissions to assign SystemManager role"}, nil
	}

	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		if errors.Is(err, usermanager.ErrUserNotFound) {
			slog.Debug("Update user: not found", "email", params.Email)
		} else {
			slog.Error("Get user for update failed", "email", params.Email, "err", err)
		}
		return &usersapi.UpdateUserNotFound{Error: "user not found"}, nil
	}

	if req.Enabled.IsSet() {
		user.Enabled = req.Enabled.Value
	}
	if req.SystemManager.IsSet() {
		user.SystemManager = req.SystemManager.Value
	}
	if req.UserManager.IsSet() {
		user.UserManager = req.UserManager.Value
	}
	if req.Rules != nil {
		user.Rules = req.Rules
	}

	if err := h.manager.Save(ctx, user); err != nil {
		if errors.Is(err, usermanager.ErrUserNotFound) {
			slog.Debug("Update user: not found", "email", user.Email)
			return &usersapi.UpdateUserNotFound{Error: "user not found"}, nil
		}
		slog.Error("Update user failed", "email", user.Email, "err", err)
		return &usersapi.UpdateUserBadRequest{Error: "failed to update user"}, nil
	}
	slog.Info("User updated", "email", user.Email)

	return &usersapi.UserResponse{
		Email:         user.Email,
		Enabled:       user.Enabled,
		SystemManager: user.SystemManager,
		UserManager:   user.UserManager,
		Rules:         user.Rules,
	}, nil
}

func (h *usersHandler) DeleteUser(ctx context.Context, params usersapi.DeleteUserParams) (usersapi.DeleteUserRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.DeleteUserForbidden{Error: "insufficient permissions"}, nil
	}
	if strings.EqualFold(claims.Subject, params.Email) {
		return &usersapi.DeleteUserForbidden{Error: "cannot delete your own account"}, nil
	}

	if err := h.manager.Delete(ctx, params.Email); err != nil {
		if errors.Is(err, usermanager.ErrUserNotFound) {
			slog.Debug("Delete user: not found", "email", params.Email)
			return &usersapi.DeleteUserNotFound{Error: "user not found"}, nil
		}
		slog.Error("Delete user failed", "email", params.Email, "err", err)
		return &usersapi.DeleteUserNotFound{Error: "failed to delete user"}, nil
	}
	slog.Info("User deleted", "email", params.Email)

	return &usersapi.DeleteUserResponse{Success: true}, nil
}

func (h *usersHandler) ChangePassword(ctx context.Context, req *usersapi.ChangePasswordRequest, params usersapi.ChangePasswordParams) (usersapi.ChangePasswordRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil {
		return &usersapi.ChangePasswordUnauthorized{Error: "authentication required"}, nil
	}

	isOwnAccount := strings.EqualFold(claims.Subject, params.Email)
	isAdmin := claims.EmissaryUserManagement

	if !isOwnAccount && !isAdmin {
		return &usersapi.ChangePasswordForbidden{Error: "insufficient permissions"}, nil
	}

	// Resetting a privileged account (System Manager or User Manager) hands
	// the caller that account's permissions at the next login, so it requires
	// System Manager privileges. Read the stored record rather than trusting
	// any caller-supplied role information.
	if !isOwnAccount {
		target, err := h.manager.Get(ctx, params.Email)
		if err != nil {
			if errors.Is(err, usermanager.ErrUserNotFound) {
				slog.Debug("Change password: user not found", "email", params.Email)
				return &usersapi.ChangePasswordNotFound{Error: "user not found"}, nil
			}
			slog.Error("Get user for password change failed", "email", params.Email, "err", err)
			return &usersapi.ChangePasswordBadRequest{Error: "failed to change password"}, nil
		}
		if isPrivilegedUser(target) && !claims.SystemManager {
			slog.Warn("Password reset of privileged user denied", "email", params.Email, "subject", claims.Subject)
			return &usersapi.ChangePasswordForbidden{Error: "insufficient permissions to reset a privileged user's password"}, nil
		}
	}

	oldPassword := ""
	if req.OldPassword.IsSet() {
		oldPassword = req.OldPassword.Value
	}
	if isOwnAccount && oldPassword == "" {
		return &usersapi.ChangePasswordBadRequest{Error: "old password is required"}, nil
	}
	if req.NewPassword == "" {
		return &usersapi.ChangePasswordBadRequest{Error: "new password is required"}, nil
	}

	if err := h.applyPasswordChange(ctx, params.Email, oldPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, usermanager.ErrUserNotFound):
			slog.Debug("Change password: user not found", "email", params.Email)
			return &usersapi.ChangePasswordNotFound{Error: "user not found"}, nil
		case errors.Is(err, usermanager.ErrInvalidCredentials):
			slog.Warn("Change password: old password incorrect", "email", params.Email)
			return &usersapi.ChangePasswordConflict{Error: "old password is incorrect"}, nil
		case errors.Is(err, errSessionRevocation):
			return &usersapi.ChangePasswordBadRequest{Error: "password changed but failed to revoke sessions"}, nil
		default:
			slog.Error("Change password failed", "email", params.Email, "err", err)
			return &usersapi.ChangePasswordBadRequest{Error: "failed to change password"}, nil
		}
	}

	return &usersapi.ChangePasswordResponse{Success: true}, nil
}

// PasswordChangeSupported reports that the generated users API is backed by a
// manager that owns password verifiers. A plain JWT is enough; the generated
// security handler has already validated the token.
func (h *usersHandler) PasswordChangeSupported(context.Context) (usersapi.PasswordChangeSupportedRes, error) {
	return &usersapi.PasswordChangeSupportedResponse{Supported: true}, nil
}

// ChangeOwnPassword changes the password of the account in the bearer token.
// No management claim is needed, but the current password must be supplied:
// possessing a token alone must not be enough to take over an account.
func (h *usersHandler) ChangeOwnPassword(ctx context.Context, req *usersapi.ChangeOwnPasswordRequest) (usersapi.ChangeOwnPasswordRes, error) {
	claims := auth.ClaimsFromContext(ctx)
	email := ""
	if claims != nil {
		email = strings.TrimSpace(claims.Subject)
	}
	if email == "" {
		return &usersapi.ChangeOwnPasswordUnauthorized{Error: "authentication required"}, nil
	}
	if !req.OldPassword.IsSet() || req.OldPassword.Value == "" {
		return &usersapi.ChangeOwnPasswordBadRequest{Error: "old password is required"}, nil
	}
	if req.NewPassword == "" {
		return &usersapi.ChangeOwnPasswordBadRequest{Error: "new password is required"}, nil
	}

	if err := h.applyPasswordChange(ctx, email, req.OldPassword.Value, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, usermanager.ErrUserNotFound):
			slog.Debug("Change own password: user not found", "email", email)
			return &usersapi.ChangeOwnPasswordNotFound{Error: "user not found"}, nil
		case errors.Is(err, usermanager.ErrInvalidCredentials):
			slog.Warn("Change own password: old password incorrect", "email", email)
			return &usersapi.ChangeOwnPasswordConflict{Error: "old password is incorrect"}, nil
		case errors.Is(err, errSessionRevocation):
			return &usersapi.ChangeOwnPasswordBadRequest{Error: "password changed but failed to revoke sessions"}, nil
		default:
			slog.Error("Change own password failed", "email", email, "err", err)
			return &usersapi.ChangeOwnPasswordBadRequest{Error: "failed to change password"}, nil
		}
	}

	return &usersapi.ChangePasswordResponse{Success: true}, nil
}

var errSessionRevocation = errors.New("failed to revoke sessions")

// applyPasswordChange stores the new verifier and revokes the target's
// sessions. Every successful password set/reset invalidates those sessions,
// including self-service changes, so a session created before the change
// cannot still be refreshed.
func (h *usersHandler) applyPasswordChange(ctx context.Context, email, oldPassword, newPassword string) error {
	if err := h.manager.ChangePassword(ctx, email, oldPassword, newPassword); err != nil {
		return err
	}
	if h.auth != nil {
		if err := h.auth.RevokeUserSessions(ctx, email); err != nil {
			slog.Error("Failed to revoke sessions after password change", "email", email, "err", err)
			return errSessionRevocation
		}
	} else {
		slog.Warn("Password changed without session revocation: no authenticator configured", "email", email)
	}
	slog.Info("Password changed", "email", email)
	return nil
}

// isPrivilegedUser reports whether the account holds a management role. Both
// roles are sensitive: System Manager unlocks /api/support/*, and User Manager
// can administer other accounts.
func isPrivilegedUser(user *usermanager.EmissaryUser) bool {
	return user != nil && (user.SystemManager || user.UserManager)
}
