package handler

import (
	"context"
	"log"

	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/usermanager"
)

// usersHandler implements the OpenAPI users handler interface.
type usersHandler struct {
	manager EmissaryAuthManager
	auth    *auth.Authenticator
}

// NewUsersHandler creates a new users handler.
func NewUsersHandler(manager EmissaryAuthManager, authenticator *auth.Authenticator) *usersHandler {
	return &usersHandler{
		manager: manager,
		auth:    authenticator,
	}
}

// ListUsers returns a cursor-based page of users.
func (h *usersHandler) ListUsers(ctx context.Context, params usersapi.ListUsersParams) (usersapi.ListUsersRes, error) {
	claims := currentClaimsFromContext(ctx)
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
		log.Printf("list users: %v", err)
		return &usersapi.ListUsersForbidden{Error: "failed to list users"}, nil
	}

	response := &usersapi.UsersListResponse{
		Users: make([]usersapi.User, 0, len(users)),
	}
	for _, u := range users {
		response.Users = append(response.Users, usersapi.User{
			Email:   u.Email,
			Enabled: u.Enabled,
			Admin:   u.Admin,
			Rules:   u.Rules,
		})
	}

	if nextCursor != "" {
		response.NextCursor = usersapi.NewOptString(nextCursor)
	}

	return response, nil
}

// CreateUser creates a new user.
func (h *usersHandler) CreateUser(ctx context.Context, req *usersapi.CreateUserRequest) (usersapi.CreateUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.CreateUserForbidden{Error: "insufficient permissions"}, nil
	}

	if req.Email == "" {
		return &usersapi.CreateUserBadRequest{Error: "email is required"}, nil
	}
	if req.Password == "" {
		return &usersapi.CreateUserBadRequest{Error: "password is required"}, nil
	}

	enabled := true
	if req.Enabled.IsSet() {
		enabled = req.Enabled.Value
	}

	admin := false
	if req.Admin.IsSet() {
		admin = req.Admin.Value
	}

	rules := req.Rules
	if rules == nil {
		rules = []string{}
	}

	// TODO: Salt and hash the password
	// For now, we store it as-is (will be fixed when DdbManager is implemented)
	user := &EmissaryUser{
		User: &usermanager.User{
			ID:       req.Email,
			Username: req.Email,
			Email:    req.Email,
			Admin:    admin,
		},
		Enabled:        enabled,
		SaltedPassword: req.Password, // TODO: hash
		Rules:          rules,
	}

	if err := h.manager.Create(ctx, user); err != nil {
		log.Printf("create user: %v", err)
		// Check if it's a duplicate
		return &usersapi.CreateUserConflict{Error: "user already exists"}, nil
	}

	return &usersapi.UserResponse{
		Email:   user.Email,
		Enabled: user.Enabled,
		Admin:   user.Admin,
		Rules:   user.Rules,
	}, nil
}

// GetUser returns a user by email.
func (h *usersHandler) GetUser(ctx context.Context, params usersapi.GetUserParams) (usersapi.GetUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.GetUserForbidden{Error: "insufficient permissions"}, nil
	}

	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		log.Printf("get user: %v", err)
		return &usersapi.GetUserNotFound{Error: "user not found"}, nil
	}

	return &usersapi.UserResponse{
		Email:   user.Email,
		Enabled: user.Enabled,
		Admin:   user.Admin,
		Rules:   user.Rules,
	}, nil
}

// UpdateUser updates an existing user.
func (h *usersHandler) UpdateUser(ctx context.Context, req *usersapi.UpdateUserRequest, params usersapi.UpdateUserParams) (usersapi.UpdateUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.UpdateUserForbidden{Error: "insufficient permissions"}, nil
	}

	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		log.Printf("get user for update: %v", err)
		return &usersapi.UpdateUserNotFound{Error: "user not found"}, nil
	}

	if req.Enabled.IsSet() {
		user.Enabled = req.Enabled.Value
	}
	if req.Admin.IsSet() {
		user.Admin = req.Admin.Value
	}
	if req.Rules != nil {
		user.Rules = req.Rules
	}

	if err := h.manager.Save(ctx, user); err != nil {
		log.Printf("save user: %v", err)
		return &usersapi.UpdateUserBadRequest{Error: "failed to update user"}, nil
	}

	return &usersapi.UserResponse{
		Email:   user.Email,
		Enabled: user.Enabled,
		Admin:   user.Admin,
		Rules:   user.Rules,
	}, nil
}

// DeleteUser deletes a user by email.
func (h *usersHandler) DeleteUser(ctx context.Context, params usersapi.DeleteUserParams) (usersapi.DeleteUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.DeleteUserForbidden{Error: "insufficient permissions"}, nil
	}

	if err := h.manager.Delete(ctx, params.Email); err != nil {
		log.Printf("delete user: %v", err)
		return &usersapi.DeleteUserNotFound{Error: "user not found"}, nil
	}

	return &usersapi.DeleteUserResponse{Success: true}, nil
}

// ChangePassword changes a user's password.
func (h *usersHandler) ChangePassword(ctx context.Context, req *usersapi.ChangePasswordRequest, params usersapi.ChangePasswordParams) (usersapi.ChangePasswordRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil {
		return &usersapi.ChangePasswordUnauthorized{Error: "authentication required"}, nil
	}

	// Check if the user is changing their own password or is an admin with EmissaryUserManagement
	isOwnAccount := claims.Subject == params.Email
	isAdmin := claims.EmissaryUserManagement

	if !isOwnAccount && !isAdmin {
		return &usersapi.ChangePasswordForbidden{Error: "insufficient permissions"}, nil
	}

	// Get the user to verify old password
	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		log.Printf("get user for password change: %v", err)
		return &usersapi.ChangePasswordNotFound{Error: "user not found"}, nil
	}

	// Verify old password if provided (required for own account, optional for admin)
	oldPassword := req.OldPassword.Value
	if req.OldPassword.IsSet() || isOwnAccount {
		if !req.OldPassword.IsSet() {
			return &usersapi.ChangePasswordBadRequest{Error: "old password is required"}, nil
		}
		// TODO: Properly verify salted password
		// For now, simple string comparison (will be fixed when DdbManager is implemented)
		if user.SaltedPassword != oldPassword {
			return &usersapi.ChangePasswordConflict{Error: "old password is incorrect"}, nil
		}
	}

	// Update the password
	// TODO: Salt and hash the password
	user.SaltedPassword = req.NewPassword // TODO: hash

	if err := h.manager.Save(ctx, user); err != nil {
		log.Printf("save user after password change: %v", err)
		return &usersapi.ChangePasswordBadRequest{Error: "failed to change password"}, nil
	}

	return &usersapi.ChangePasswordResponse{Success: true}, nil
}

// currentClaimsFromContext extracts the claims from the request context.
func currentClaimsFromContext(ctx context.Context) *auth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*auth.Claims)
	return claims
}