package usermanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/fsutil"
)

// FileManager is the Emissary-mastered user manager backed by local files.
// It stores the same plain JSON EmissaryUser records as S3Manager: one record
// per user at a user.json file directly under the user's directory. The
// configured root is the users/ directory itself (FILESYSTEM_DIR/users),
// so a record lives at <root>/<email>/user.json and mailbox directories next
// to it are not users unless they contain a record.
type FileManager struct {
	admin adminAuth
	root  string

	mu sync.RWMutex
}

var _ EmissaryAuthManager = (*FileManager)(nil)

// NewFile builds the filesystem-backed user manager rooted at cfg.FilesystemDir.
// "~" is expanded to the current user's home directory and the root is created
// if it does not exist.
func NewFile(cfg Config) (*FileManager, error) {
	root, err := fsutil.ExpandDir(cfg.FilesystemDir)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, errors.New("filesystem user manager directory is required (set FILESYSTEM_DIR)")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create filesystem user directory %q: %w", root, err)
	}
	return &FileManager{admin: newAdminAuth(cfg), root: root}, nil
}

// NewFileSystem is an alias for NewFile, matching the "filesystem" type name.
func NewFileSystem(cfg Config) (*FileManager, error) { return NewFile(cfg) }

// RootDir returns the directory holding user records.
func (m *FileManager) RootDir() string { return m.root }

func (m *FileManager) Name() string { return "filesystem" }

// fileUserKey is the key, relative to the manager root, of a user's record.
func fileUserKey(id string) string {
	return strings.TrimPrefix(id, "/") + "/" + userRecordName
}

// isFileUserRecordKey reports whether key has the shape <email>/user.json.
// Mail directories live alongside records but never match this predicate.
func isFileUserRecordKey(key string) bool {
	parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] == userRecordName
}

func (m *FileManager) recordPath(id string) (string, error) {
	return fsutil.ResolveKey(m.root, fileUserKey(id))
}

func (m *FileManager) Authenticate(ctx context.Context, username, password string) (*User, error) {
	if user, handled, err := m.admin.authenticate(username, password); handled {
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
	// Verify the password before revealing that the account is disabled.
	if !checkPassword(user.Salt, user.SaltedPassword, password) {
		return nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	authenticated := user.User
	return &authenticated, nil
}

func (m *FileManager) Lookup(ctx context.Context, username string) (*User, error) {
	if m.admin.isAdmin(username) {
		return m.admin.user(), nil
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

// userKeys returns the record keys in stable order. A cursor is the key of
// the last user returned and pagination resumes strictly after it.
func (m *FileManager) userKeys() ([]string, error) {
	var keys []string
	err := filepath.WalkDir(m.root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if isFileUserRecordKey(key) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *FileManager) List(_ context.Context, cursor string, pageSize int) ([]*EmissaryUser, string, error) {
	if pageSize <= 0 {
		pageSize = defaultUserPageSize
	}
	if pageSize > maxUserPageSize {
		pageSize = maxUserPageSize
	}

	startAfter := ""
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		decoded, err := decodeUserCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		startAfter = decoded
	}

	keys, err := m.userKeys()
	if err != nil {
		return nil, "", err
	}

	users := make([]*EmissaryUser, 0, pageSize)
	nextCursor := ""
	lastKey := ""
	for _, key := range keys {
		if key <= startAfter {
			continue
		}
		if len(users) >= pageSize {
			nextCursor = encodeUserCursor(lastKey)
			break
		}
		path, err := fsutil.ResolveKey(m.root, key)
		if err != nil {
			return nil, "", err
		}
		user, err := m.getUserByKey(path)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				// The record disappeared between the walk and the read.
				continue
			}
			return nil, "", err
		}
		users = append(users, user)
		lastKey = key
	}
	return users, nextCursor, nil
}

func (m *FileManager) Autocomplete(ctx context.Context, q string) ([]string, error) {
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

func (m *FileManager) Create(ctx context.Context, user *EmissaryUser) error {
	if user == nil {
		return errors.New("user is required")
	}
	id := normalizeUserID(user.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	if _, ok := normalizeAddress(id); !ok {
		return errors.New("user email must be a valid address")
	}
	user.Email = id
	if user.SchemaVersion == "" {
		user.SchemaVersion = "1.0.0"
	}
	if user.SaltedPassword != "" && user.Salt == "" {
		salt, hashed, err := hashPassword(user.SaltedPassword)
		if err != nil {
			return err
		}
		user.Salt = salt
		user.SaltedPassword = hashed
	}

	body, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}
	path, err := m.recordPath(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return ErrUserExists
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to check user %q: %w", id, err)
	}
	if err := fsutil.WriteFileAtomically(path, body, 0600); err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (m *FileManager) Save(ctx context.Context, user *EmissaryUser) error {
	if user == nil {
		return errors.New("user is required")
	}
	id := normalizeUserID(user.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	user.Email = id

	body, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}
	path, err := m.recordPath(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to check user %q: %w", id, err)
	}
	if err := fsutil.WriteFileAtomically(path, body, 0600); err != nil {
		return fmt.Errorf("failed to save user: %w", err)
	}
	return nil
}

func (m *FileManager) Get(_ context.Context, email string) (*EmissaryUser, error) {
	id := normalizeUserID(email)
	if id == "" {
		return nil, ErrUserNotFound
	}
	path, err := m.recordPath(id)
	if err != nil {
		return nil, err
	}
	return m.getUserByKey(path)
}

// getUserByKey reads and decodes a user record. A directory without a
// user.json is not a user, just as in the S3 backend.
func (m *FileManager) getUserByKey(path string) (*EmissaryUser, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to read user %q: %w", path, err)
	}
	var user EmissaryUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("failed to decode user %q: %w", path, err)
	}
	if user.Rules == nil {
		user.Rules = []string{}
	}
	return &user, nil
}

func (m *FileManager) Delete(_ context.Context, email string) error {
	id := normalizeUserID(email)
	if id == "" {
		return ErrUserNotFound
	}
	path, err := m.recordPath(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to check user %q: %w", id, err)
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

// ChangePassword verifies oldPassword when it is non-empty, then replaces
// the stored password with a salted hash of newPassword.
func (m *FileManager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
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
