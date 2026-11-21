package usermanager

import (
	"context"
	"errors"
	"strings"
)

// This file is the single home for user-manager policy. Backends only acquire
// a client, perform their store-specific Get/Put/Scan/List, and hand the result
// back to the helpers below. Security-sensitive ordering lives here once:
// admin first, password before disabled status, constant-time compares in the
// password package.

// adminAuthResult applies the built-in administrator check. handled reports
// whether username named the administrator; callers must not fall through to
// the ordinary-user store after a bad admin password.
func adminAuthResult(admin adminAuth, username, password string) (*User, error, bool) {
	user, handled, err := admin.authenticate(username, password)
	return user, err, handled
}

// adminLookup returns the built-in administrator principal when username names
// it.
func adminLookup(admin adminAuth, username string) (*User, bool) {
	if admin.isAdmin(username) {
		return admin.user(), true
	}
	return nil, false
}

// authenticateUser applies the shared authentication policy to an
// Emissary-mastered store reached through m. admin is passed explicitly
// because it is a backend field, not part of EmissaryAuthManager; the check
// itself still lives here.
func authenticateUser(ctx context.Context, m EmissaryAuthManager, admin adminAuth, username, password string) (*User, error) {
	if user, err, handled := adminAuthResult(admin, username, password); handled {
		return user, err
	}

	address, ok := normalizeAddress(username)
	if !ok {
		return nil, ErrInvalidCredentials
	}

	user, err := m.Get(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	// Verify the password before revealing that the account is disabled, so a
	// disabled user with a wrong password is indistinguishable from any other
	// failed login.
	if !checkPassword(user.Salt, user.SaltedPassword, password) {
		return nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	authenticated := user.User
	return &authenticated, nil
}

// lookupUser applies the shared lookup policy. Unlike authentication there is
// no password to verify, so disabled status is returned directly.
func lookupUser(ctx context.Context, m EmissaryAuthManager, admin adminAuth, username string) (*User, error) {
	if user, ok := adminLookup(admin, username); ok {
		return user, nil
	}

	address, ok := normalizeAddress(username)
	if !ok {
		return nil, ErrInvalidCredentials
	}

	user, err := m.Get(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	lookup := user.User
	return &lookup, nil
}

// autocompleteFromList pages through m.List and keeps up to ten case-insensitive
// email matches. An empty query returns a non-nil empty slice.
func autocompleteFromList(ctx context.Context, m EmissaryAuthManager, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []string{}, nil
	}

	qLower := strings.ToLower(q)
	matches := make([]string, 0, 10)
	cursor := ""
	for len(matches) < 10 {
		users, next, err := m.List(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if strings.Contains(strings.ToLower(u.Email), qLower) {
				matches = append(matches, u.Email)
				if len(matches) >= 10 {
					break
				}
			}
		}
		if next == "" || len(users) == 0 {
			break
		}
		cursor = next
	}
	return matches, nil
}

// prepareForCreate validates and normalises a new user record, hashing a
// plaintext password when one was supplied.
func prepareForCreate(u *EmissaryUser) error {
	if u == nil {
		return errors.New("user is required")
	}
	id := normalizeUserID(u.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	if _, ok := normalizeAddress(id); !ok {
		return errors.New("user email must be a valid address")
	}
	u.Email = id

	if u.SaltedPassword == "" {
		// No password: never persist a salt or verifier for this account.
		u.Salt = ""
	} else if u.Salt == "" {
		// Plaintext password provided, hash it with the default (bcrypt).
		salt, hashed, err := hashPassword(u.SaltedPassword)
		if err != nil {
			return err
		}
		u.Salt = salt
		u.SaltedPassword = hashed
	}
	return nil
}

// applyCreateSchemaVersion materialises the default on the caller's struct.
// Filesystem and S3 have always done this; DynamoDB applies the default in
// ddbUserFrom when encoding the record and leaves the caller's struct alone.
func applyCreateSchemaVersion(u *EmissaryUser) {
	if u.SchemaVersion == "" {
		u.SchemaVersion = "1.0.0"
	}
}

// prepareForSave normalises an existing user record. Save deliberately does not
// validate the address or hash a password: those are Create responsibilities.
func prepareForSave(u *EmissaryUser) error {
	if u == nil {
		return errors.New("user is required")
	}
	id := normalizeUserID(u.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	u.Email = id
	if u.SaltedPassword == "" {
		// A blank password must never be persisted with a salt.
		u.Salt = ""
	}
	return nil
}

// changePassword verifies oldPassword when it is non-empty, then stores a new
// hash through m.Save. An empty oldPassword skips verification; callers must
// have authorized the change themselves (for example a System Manager
// resetting another user's password).
func changePassword(ctx context.Context, m EmissaryAuthManager, email, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password is required")
	}

	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	if oldPassword != "" && !checkPassword(user.Salt, user.SaltedPassword, oldPassword) {
		return ErrInvalidCredentials
	}

	salt, hashed, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	user.Salt = salt
	user.SaltedPassword = hashed
	return m.Save(ctx, user)
}
