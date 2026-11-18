package storage

import (
	"bytes"
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
	"time"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/fsutil"
)

var _ Storage = (*FileStorage)(nil)

// FileStorage is the local-filesystem implementation of Storage. It uses the
// same logical keys as S3Storage (users/<account>/<mailbox>/<timestamp>_<id>.eml
// for messages and _storage/<session>/... for scratch state), so ids returned
// by either backend have the same shape.
type FileStorage struct {
	root string
	mu   sync.RWMutex
}

// NewFileStorage builds a filesystem store rooted at dir. "~" is expanded to
// the current user's home directory. The root is created if it does not exist.
func NewFileStorage(dir string) (*FileStorage, error) {
	root, err := fsutil.ExpandDir(dir)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, errors.New("filesystem storage directory is required (set FILESYSTEM_DIR)")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create filesystem storage directory %q: %w", root, err)
	}
	return &FileStorage{root: root}, nil
}

// NewFileSystemStorage is an alias for NewFileStorage, matching the
// "filesystem" type name used by STORAGE.
func NewFileSystemStorage(dir string) (*FileStorage, error) { return NewFileStorage(dir) }

// RootDir returns the directory used for mail and session state.
func (s *FileStorage) RootDir() string { return s.root }

func (s *FileStorage) Name() string { return "filesystem" }

func (s *FileStorage) objectPath(key string) (string, error) {
	return fsutil.ResolveKey(s.root, key)
}

// ListEmails walks only the requested account/mailbox directory. As with the
// S3 backend, an empty mailboxID lists every mailbox in the account and
// system files (drafts, user records) are never returned as messages.
func (s *FileStorage) ListEmails(_ context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error) {
	prefix := email.MailboxKeyPrefix(accountID, mailboxID)
	if prefix == "" {
		return nil, fmt.Errorf("accountID is required")
	}
	dir, err := s.objectPath(prefix)
	if err != nil {
		return nil, err
	}

	var keys []string
	err = filepath.WalkDir(dir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) && email.IsDeliveredMessageKey(key) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list messages under %q: %w", prefix, err)
	}
	sort.Strings(keys)

	emails := make([]*email.ListedEmail, 0, len(keys))
	for _, key := range keys {
		message, err := s.getEmailByKey(key)
		if err != nil {
			// A file may disappear between the walk and the read, just as an
			// S3 object can. Skip it rather than failing the whole listing.
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		emails = append(emails, message)
	}
	return emails, nil
}

func (s *FileStorage) getEmailByKey(key string) (*email.ListedEmail, error) {
	raw, err := s.getRawByKey(key)
	if err != nil {
		return nil, err
	}
	parsed, err := email.Parse(key, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email %q: %w", key, err)
	}
	ref := email.EmailRefFromID(key)
	return &email.ListedEmail{Ref: &ref, Email: parsed}, nil
}

func (s *FileStorage) getRawByKey(key string) ([]byte, error) {
	objectPath, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to stat message %q: %w", key, err)
	}
	if info.IsDir() {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read message %q: %w", key, err)
	}
	return raw, nil
}

// GetEmail implements the Storage interface.
func (s *FileStorage) GetEmail(_ context.Context, id string) (*email.ListedEmail, error) {
	return s.getEmailByKey(strings.TrimPrefix(id, "/"))
}

// GetEmailRaw implements the Storage interface, returning the delivered
// message bytes verbatim.
func (s *FileStorage) GetEmailRaw(_ context.Context, id string) ([]byte, error) {
	return s.getRawByKey(strings.TrimPrefix(id, "/"))
}

// AddEmail implements the Storage interface. Messages for the account go to
// its inbox; the returned id is the key relative to the storage root, exactly
// like the key returned by S3Storage.
func (s *FileStorage) AddEmail(_ context.Context, accountID string, message *email.UserEmail) (string, error) {
	mailboxPrefix := email.MailboxKeyPrefix(accountID, "inbox")
	if mailboxPrefix == "" {
		return "", errors.New("accountID is required")
	}
	if message == nil {
		return "", errors.New("invalid email")
	}

	id, err := newEmailID()
	if err != nil {
		return "", err
	}
	timestamp := message.Date
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	key := fmt.Sprintf("%s%s_%s.eml", mailboxPrefix, email.FormatDeliveredTimestamp(timestamp), id)

	objectPath, err := s.objectPath(key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fsutil.WriteFileAtomically(objectPath, email.RawFromUserEmail(message), 0644); err != nil {
		return "", fmt.Errorf("failed to add email %q: %w", key, err)
	}
	return key, nil
}

// DeleteEmail implements the Storage interface.
func (s *FileStorage) DeleteEmail(_ context.Context, id string) error {
	objectPath, err := s.objectPath(strings.TrimPrefix(id, "/"))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("failed to stat message %q: %w", id, err)
	}
	if info.IsDir() {
		return ErrNotFound
	}
	if err := os.Remove(objectPath); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("failed to delete message %q: %w", id, err)
	}
	return nil
}

// PutDraft implements DraftStorage. Drafts live under
// _storage/<session>/drafts/ so they are never mistaken for delivered mail.
func (s *FileStorage) PutDraft(_ context.Context, sessionID, creationID string, message *email.UserEmail) (string, error) {
	relative, err := draftKey(creationID)
	if err != nil {
		return "", err
	}
	key, err := sessionObjectKey(sessionID, relative)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal draft: %w", err)
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fsutil.WriteFileAtomically(objectPath, body, 0600); err != nil {
		return "", fmt.Errorf("failed to store draft %q: %w", key, err)
	}
	return relative, nil
}

// GetDraft implements DraftStorage.
func (s *FileStorage) GetDraft(_ context.Context, sessionID, id string) (*email.UserEmail, error) {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return nil, err
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read draft %q: %w", key, err)
	}
	var message email.UserEmail
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("failed to parse draft %q: %w", key, err)
	}
	return &message, nil
}

// DeleteDraft implements DraftStorage. It is idempotent.
func (s *FileStorage) DeleteDraft(_ context.Context, sessionID, id string) error {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return err
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(objectPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete draft %q: %w", key, err)
	}
	return nil
}

// LoadCreatedIDs implements SessionStateStore. A session with no stored map
// simply returns an empty one.
func (s *FileStorage) LoadCreatedIDs(_ context.Context, sessionID string) (map[string]string, error) {
	key, err := sessionCreatedIDsKey(sessionID)
	if err != nil {
		return nil, err
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to read created ids %q: %w", key, err)
	}
	ids := make(map[string]string)
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, fmt.Errorf("failed to parse created ids %q: %w", key, err)
	}
	return ids, nil
}

// SaveCreatedIDs implements SessionStateStore.
func (s *FileStorage) SaveCreatedIDs(_ context.Context, sessionID string, ids map[string]string) error {
	key, err := sessionCreatedIDsKey(sessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal created ids: %w", err)
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fsutil.WriteFileAtomically(objectPath, body, 0600); err != nil {
		return fmt.Errorf("failed to store created ids %q: %w", key, err)
	}
	return nil
}
