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
	return authenticateUser(ctx, m, m.admin, username, password)
}

func (m *FileManager) Lookup(ctx context.Context, username string) (*User, error) {
	return lookupUser(ctx, m, m.admin, username)
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
	startAfter, pageSize, err := pageParams(cursor, pageSize, true)
	if err != nil {
		return nil, "", err
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
	return autocompleteFromList(ctx, m, q)
}

func (m *FileManager) Create(ctx context.Context, user *EmissaryUser) error {
	if err := prepareForCreate(user); err != nil {
		return err
	}
	applyCreateSchemaVersion(user)
	id := user.Email

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
	if err := prepareForSave(user); err != nil {
		return err
	}
	id := user.Email

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

func (m *FileManager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
	return changePassword(ctx, m, email, oldPassword, newPassword)
}
