package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

const mailboxRecordName = "mailbox.json"

// FileStorage is the local-filesystem implementation of Storage. It uses the
// same logical keys as S3Storage (users/<account>/<mailbox>/<token>.eml for
// messages and _storage/<session>/... for scratch state), so ids returned by
// either backend have the same shape.
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

func accountRoot(accountID string) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return "", errors.New("accountID is required")
	}
	return email.MailboxKeyRoot + accountID + "/", nil
}

func mailboxDirKey(accountID, mailboxID string) (string, error) {
	prefix, err := accountRoot(accountID)
	if err != nil {
		return "", err
	}
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		return "", errors.New("mailboxID is required")
	}
	return prefix + mailboxID + "/", nil
}

func mailboxRecordKey(accountID, mailboxID string) (string, error) {
	dir, err := mailboxDirKey(accountID, mailboxID)
	if err != nil {
		return "", err
	}
	return dir + mailboxRecordName, nil
}

func (s *FileStorage) ensureDefaultMailboxes(accountID string) error {
	for _, mb := range DefaultMailboxes() {
		dir, err := mailboxDirKey(accountID, mb.ID)
		if err != nil {
			return err
		}
		path, err := s.objectPath(dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
		recKey, err := mailboxRecordKey(accountID, mb.ID)
		if err != nil {
			return err
		}
		recPath, err := s.objectPath(recKey)
		if err != nil {
			return err
		}
		if _, err := os.Stat(recPath); os.IsNotExist(err) {
			body, err := json.Marshal(mb)
			if err != nil {
				return err
			}
			if err := fsutil.WriteFileAtomically(recPath, body, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *FileStorage) readMailboxRecord(accountID, mailboxID string) (Mailbox, error) {
	key, err := mailboxRecordKey(accountID, mailboxID)
	if err != nil {
		return Mailbox{}, ErrNotFound
	}
	path, err := s.objectPath(key)
	if err != nil {
		return Mailbox{}, ErrNotFound
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Folder may exist without record (legacy/inferred mailbox).
			dirKey, _ := mailboxDirKey(accountID, mailboxID)
			dirPath, _ := s.objectPath(dirKey)
			if info, statErr := os.Stat(dirPath); statErr == nil && info.IsDir() {
				name := mailboxID
				if len(name) > 0 {
					name = strings.ToUpper(name[:1]) + name[1:]
				}
				mb := Mailbox{ID: NormalizeMailboxID(mailboxID), Name: name, IsSubscribed: true}
				for _, def := range DefaultMailboxes() {
					if def.ID == mb.ID {
						mb = def
						break
					}
				}
				return mb, nil
			}
			return Mailbox{}, ErrNotFound
		}
		return Mailbox{}, err
	}
	var mb Mailbox
	if err := json.Unmarshal(body, &mb); err != nil {
		return Mailbox{}, err
	}
	mb.ID = NormalizeMailboxID(mailboxID)
	if mb.Name == "" {
		mb.Name = mb.ID
	}
	return mb, nil
}

func (s *FileStorage) writeMailboxRecord(accountID string, mailbox Mailbox) error {
	mailbox.ID = NormalizeMailboxID(mailbox.ID)
	key, err := mailboxRecordKey(accountID, mailbox.ID)
	if err != nil {
		return err
	}
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	body, err := json.Marshal(mailbox)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomically(path, body, 0644)
}

func (s *FileStorage) ListMailboxes(_ context.Context, accountID string) ([]Mailbox, error) {
	prefix, err := accountRoot(accountID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	_ = s.ensureDefaultMailboxes(accountID)
	s.mu.Unlock()

	rootPath, err := s.objectPath(prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Mailbox{}, nil
		}
		return nil, err
	}
	mailboxes := make([]Mailbox, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := NormalizeMailboxID(entry.Name())
		if id == "" || seen[id] {
			continue
		}
		mb, err := s.readMailboxRecord(accountID, id)
		if err != nil {
			continue
		}
		seen[id] = true
		mailboxes = append(mailboxes, mb)
	}
	// Ensure default mailboxes are present in result even if dir missing somehow.
	for _, def := range DefaultMailboxes() {
		if !seen[def.ID] {
			mailboxes = append(mailboxes, def)
		}
	}
	SortMailboxes(mailboxes)
	return mailboxes, nil
}

func (s *FileStorage) GetMailbox(_ context.Context, accountID, mailboxID string) (Mailbox, error) {
	s.mu.Lock()
	_ = s.ensureDefaultMailboxes(accountID)
	s.mu.Unlock()
	return s.readMailboxRecord(accountID, mailboxID)
}

func (s *FileStorage) CreateMailbox(_ context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	mailbox.ID = NormalizeMailboxID(mailbox.ID)
	mailbox.Name = strings.TrimSpace(mailbox.Name)
	if mailbox.ParentID != nil {
		p := NormalizeMailboxID(*mailbox.ParentID)
		mailbox.ParentID = &p
	}
	if mailbox.ID == "" || mailbox.Name == "" {
		return Mailbox{}, errors.New("invalid mailbox")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDefaultMailboxes(accountID); err != nil {
		return Mailbox{}, err
	}
	dir, err := mailboxDirKey(accountID, mailbox.ID)
	if err != nil {
		return Mailbox{}, err
	}
	path, err := s.objectPath(dir)
	if err != nil {
		return Mailbox{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Mailbox{}, ErrMailboxExists
	}
	if mailbox.ParentID != nil && *mailbox.ParentID != "" {
		if _, err := s.readMailboxRecord(accountID, *mailbox.ParentID); err != nil {
			return Mailbox{}, ErrNotFound
		}
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return Mailbox{}, err
	}
	if err := s.writeMailboxRecord(accountID, mailbox); err != nil {
		return Mailbox{}, err
	}
	return mailbox, nil
}

func (s *FileStorage) UpdateMailbox(_ context.Context, accountID string, mailbox Mailbox) (Mailbox, error) {
	mailbox.ID = NormalizeMailboxID(mailbox.ID)
	mailbox.Name = strings.TrimSpace(mailbox.Name)
	if mailbox.ParentID != nil {
		p := NormalizeMailboxID(*mailbox.ParentID)
		mailbox.ParentID = &p
	}
	if mailbox.ID == "" || mailbox.Name == "" {
		return Mailbox{}, errors.New("invalid mailbox")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDefaultMailboxes(accountID); err != nil {
		return Mailbox{}, err
	}
	if _, err := s.readMailboxRecord(accountID, mailbox.ID); err != nil {
		return Mailbox{}, ErrNotFound
	}
	if mailbox.ParentID != nil && *mailbox.ParentID != "" {
		if *mailbox.ParentID == mailbox.ID {
			return Mailbox{}, errors.New("mailbox cannot be its own parent")
		}
		if _, err := s.readMailboxRecord(accountID, *mailbox.ParentID); err != nil {
			return Mailbox{}, ErrNotFound
		}
	}
	if err := s.writeMailboxRecord(accountID, mailbox); err != nil {
		return Mailbox{}, err
	}
	return mailbox, nil
}

func (s *FileStorage) DeleteMailbox(_ context.Context, accountID, mailboxID string) error {
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDefaultMailboxes(accountID); err != nil {
		return err
	}
	dir, err := mailboxDirKey(accountID, mailboxID)
	if err != nil {
		return err
	}
	path, err := s.objectPath(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	// Reject if it has child mailboxes or any .eml messages.
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return ErrMailboxNotEmpty
		}
		name := strings.ToLower(entry.Name())
		if strings.HasSuffix(name, ".eml") {
			return ErrMailboxNotEmpty
		}
	}
	// Safe to remove folder (may contain mailbox.json only).
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return nil
}

// ListEmails walks only the requested account/mailbox directory. As with the
// S3 backend, an empty mailboxID lists every mailbox in the account and
// system files (drafts, user records) are never returned as messages.
func (s *FileStorage) ListEmails(_ context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error) {
	prefix := email.MailboxKeyPrefix(accountID, mailboxID)
	if prefix == "" {
		return nil, fmt.Errorf("accountID is required")
	}
	s.mu.Lock()
	_ = s.ensureDefaultMailboxes(accountID)
	s.mu.Unlock()
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
	if info.Size() > email.MaxPartSize() {
		return nil, fmt.Errorf("message %q exceeds maximum size of %d bytes", key, email.MaxPartSize())
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

func (s *FileStorage) listMailboxIDsLocked(accountID string) ([]string, error) {
	prefix, err := accountRoot(accountID)
	if err != nil {
		return nil, err
	}
	rootPath, err := s.objectPath(prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := NormalizeMailboxID(entry.Name())
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, def := range DefaultMailboxes() {
		if !seen[def.ID] {
			ids = append(ids, def.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *FileStorage) resolveEmailKeyLocked(accountID, id string) (string, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return "", ErrNotFound
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "/")
	if id == "" {
		return "", ErrNotFound
	}
	// Full physical key (possibly legacy prefix form).
	if email.IsDeliveredMessageKey(id) {
		acct, _, ok := email.MailboxFromID(id)
		if !ok || !strings.EqualFold(acct, accountID) {
			return "", ErrNotFound
		}
		return id, nil
	}
	// Stable token: locate in account mailboxes.
	token := id
	mailboxIDs, err := s.listMailboxIDsLocked(accountID)
	if err != nil {
		return "", err
	}
	for _, mailboxID := range mailboxIDs {
		key := email.DeliveredMessageKey(accountID, mailboxID, token)
		if key == "" {
			continue
		}
		path, err := s.objectPath(key)
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return key, nil
		}
	}
	return "", ErrNotFound
}

// GetEmail implements the Storage interface.
func (s *FileStorage) GetEmail(_ context.Context, accountID, id string) (*email.ListedEmail, error) {
	s.mu.Lock()
	_ = s.ensureDefaultMailboxes(accountID)
	key, err := s.resolveEmailKeyLocked(accountID, id)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.getEmailByKey(key)
}

// GetEmailRaw implements the Storage interface, returning the delivered
// message bytes verbatim.
func (s *FileStorage) GetEmailRaw(_ context.Context, accountID, id string) ([]byte, error) {
	s.mu.Lock()
	_ = s.ensureDefaultMailboxes(accountID)
	key, err := s.resolveEmailKeyLocked(accountID, id)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.getRawByKey(key)
}

// AddEmail implements the Storage interface. The returned id is the stable
// message token (filename), which does not change when the message is moved.
func (s *FileStorage) AddEmail(_ context.Context, accountID, mailboxID string, message *email.UserEmail, raw []byte) (string, error) {
	mailboxID = NormalizeMailboxID(mailboxID)
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	if _, err := accountRoot(accountID); err != nil {
		return "", err
	}
	if message == nil {
		return "", errors.New("invalid email")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDefaultMailboxes(accountID); err != nil {
		return "", err
	}
	// Ensure destination mailbox exists.
	if _, err := s.readMailboxRecord(accountID, mailboxID); err != nil {
		return "", ErrNotFound
	}

	idToken, err := newMessageToken()
	if err != nil {
		return "", err
	}
	timestamp := message.Date
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	token := fmt.Sprintf("%s_%s", email.FormatDeliveredTimestamp(timestamp), idToken)
	key := email.DeliveredMessageKey(accountID, mailboxID, token)

	objectPath, err := s.objectPath(key)
	if err != nil {
		return "", err
	}
	stored := raw
	if stored == nil {
		stored = email.RawFromUserEmail(message)
	}
	slog.Debug("Filesystem: storing email", "key", key, "root", s.root, "rawBytes", len(stored))
	if err := fsutil.WriteFileAtomically(objectPath, stored, 0644); err != nil {
		slog.Error("Filesystem: failed to store email", "key", key, "err", err)
		return "", fmt.Errorf("failed to add email %q: %w", key, err)
	}
	return token, nil
}

func (s *FileStorage) MoveEmail(_ context.Context, accountID, id, destMailboxID string) (*email.ListedEmail, error) {
	destMailboxID = NormalizeMailboxID(destMailboxID)
	if destMailboxID == "" {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDefaultMailboxes(accountID); err != nil {
		return nil, err
	}
	if _, err := s.readMailboxRecord(accountID, destMailboxID); err != nil {
		return nil, ErrNotFound
	}
	key, err := s.resolveEmailKeyLocked(accountID, id)
	if err != nil {
		return nil, err
	}
	token, ok := email.DeliveredMessageToken(key)
	if !ok {
		return nil, ErrNotFound
	}
	destKey := email.DeliveredMessageKey(accountID, destMailboxID, token)
	if destKey == key {
		return s.getEmailByKey(key)
	}
	srcPath, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	destPath, err := s.objectPath(destKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return nil, err
	}
	// Use rename for atomic-ish move on same filesystem.
	if err := os.Rename(srcPath, destPath); err != nil {
		// Fallback to copy+remove if needed.
		raw, rerr := os.ReadFile(srcPath)
		if rerr != nil {
			return nil, err
		}
		if werr := fsutil.WriteFileAtomically(destPath, raw, 0644); werr != nil {
			return nil, err
		}
		_ = os.Remove(srcPath)
	}
	return s.getEmailByKey(destKey)
}

// DeleteEmail implements the Storage interface.
func (s *FileStorage) DeleteEmail(_ context.Context, accountID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := s.resolveEmailKeyLocked(accountID, id)
	if err != nil {
		return err
	}
	objectPath, err := s.objectPath(key)
	if err != nil {
		return err
	}

	slog.Debug("Filesystem: deleting email", "id", id, "key", key)
	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		slog.Error("Filesystem: failed to stat message before delete", "key", key, "err", err)
		return fmt.Errorf("failed to stat message %q: %w", key, err)
	}
	if info.IsDir() {
		return ErrNotFound
	}
	if err := os.Remove(objectPath); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		slog.Error("Filesystem: failed to delete message", "key", key, "err", err)
		return fmt.Errorf("failed to delete message %q: %w", key, err)
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
	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to stat draft %q: %w", key, err)
	}
	if info.Size() > email.MaxPartSize() {
		return nil, fmt.Errorf("draft %q exceeds maximum size of %d bytes", key, email.MaxPartSize())
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

