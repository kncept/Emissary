package handler

import (
	"context"
	"errors"
	"log"
	"strings"

	usersapi "github.com/kncept/Emissary/api/users/generated"
	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/usermanager"
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
			Email:          u.Email,
			Enabled:        u.Enabled,
			SystemManager:  u.SystemManager,
			Rules:          u.Rules,
			UserManager:    u.UserManager,
		})
	}

	if nextCursor != "" {
		response.NextCursor = usersapi.NewOptString(nextCursor)
	}

	return response, nil
}

func (h *usersHandler) CreateUser(ctx context.Context, req *usersapi.CreateUserRequest) (usersapi.CreateUserRes, error) {
	claims := currentClaimsFromContext(ctx)
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
	if req.Password == "" {
		return &usersapi.CreateUserBadRequest{Error: "password is required"}, nil
	}

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
		Enabled:        enabled,
		SaltedPassword: req.Password,
		Rules:          rules,
		UserManager:    userManager,
		SystemManager:  systemManager,
	}
	user.ID = req.Email
	user.Username = req.Email
	user.Email = req.Email

	if err := h.manager.Create(ctx, user); err != nil {
		log.Printf("create user: %v", err)
		if errors.Is(err, usermanager.ErrUserExists) {
			return &usersapi.CreateUserConflict{Error: "user already exists"}, nil
		}
		return &usersapi.CreateUserBadRequest{Error: "failed to create user"}, nil
	}

	return &usersapi.UserResponse{
		Email:          user.Email,
		Enabled:        user.Enabled,
		SystemManager:  user.SystemManager,
		UserManager:    user.UserManager,
		Rules:          user.Rules,
	}, nil
}

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
		Email:          user.Email,
		Enabled:        user.Enabled,
		SystemManager:  user.SystemManager,
		UserManager:    user.UserManager,
		Rules:          user.Rules,
	}, nil
}

func (h *usersHandler) UpdateUser(ctx context.Context, req *usersapi.UpdateUserRequest, params usersapi.UpdateUserParams) (usersapi.UpdateUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.UpdateUserForbidden{Error: "insufficient permissions"}, nil
	}

	// SystemManager can only be assigned by a user who also has SystemManager permission
	if req.SystemManager.IsSet() && req.SystemManager.Value && !claims.SystemManager {
		return &usersapi.UpdateUserForbidden{Error: "insufficient permissions to assign SystemManager role"}, nil
	}

	user, err := h.manager.Get(ctx, params.Email)
	if err != nil {
		log.Printf("get user for update: %v", err)
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
		log.Printf("save user: %v", err)
		if errors.Is(err, usermanager.ErrUserNotFound) {
			return &usersapi.UpdateUserNotFound{Error: "user not found"}, nil
		}
		return &usersapi.UpdateUserBadRequest{Error: "failed to update user"}, nil
	}

	return &usersapi.UserResponse{
		Email:          user.Email,
		Enabled:        user.Enabled,
		SystemManager:  user.SystemManager,
		UserManager:    user.UserManager,
		Rules:          user.Rules,
	}, nil
}

func (h *usersHandler) DeleteUser(ctx context.Context, params usersapi.DeleteUserParams) (usersapi.DeleteUserRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil || !claims.EmissaryUserManagement {
		return &usersapi.DeleteUserForbidden{Error: "insufficient permissions"}, nil
	}

	if err := h.manager.Delete(ctx, params.Email); err != nil {
		log.Printf("delete user: %v", err)
		if errors.Is(err, usermanager.ErrUserNotFound) {
			return &usersapi.DeleteUserNotFound{Error: "user not found"}, nil
		}
		return &usersapi.DeleteUserNotFound{Error: "failed to delete user"}, nil
	}

	return &usersapi.DeleteUserResponse{Success: true}, nil
}

func (h *usersHandler) ChangePassword(ctx context.Context, req *usersapi.ChangePasswordRequest, params usersapi.ChangePasswordParams) (usersapi.ChangePasswordRes, error) {
	claims := currentClaimsFromContext(ctx)
	if claims == nil {
		return &usersapi.ChangePasswordUnauthorized{Error: "authentication required"}, nil
	}

	isOwnAccount := strings.EqualFold(claims.Subject, params.Email)
	isAdmin := claims.EmissaryUserManagement

	if !isOwnAccount && !isAdmin {
		return &usersapi.ChangePasswordForbidden{Error: "insufficient permissions"}, nil
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

	if err := h.manager.ChangePassword(ctx, params.Email, oldPassword, req.NewPassword); err != nil {
		log.Printf("change password: %v", err)
		switch {
		case errors.Is(err, usermanager.ErrUserNotFound):
			return &usersapi.ChangePasswordNotFound{Error: "user not found"}, nil
		case errors.Is(err, usermanager.ErrInvalidCredentials):
			return &usersapi.ChangePasswordConflict{Error: "old password is incorrect"}, nil
		default:
			return &usersapi.ChangePasswordBadRequest{Error: "failed to change password"}, nil
		}
	}

	return &usersapi.ChangePasswordResponse{Success: true}, nil
}

func currentClaimsFromContext(ctx context.Context) *auth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*auth.Claims)
	return claims
}
