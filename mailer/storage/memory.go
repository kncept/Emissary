package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Storage = (*MemoryStorage)(nil)

// MemoryStorage is a small in-memory store. It is useful for local
// development and tests; messages are lost when the process exits.
type MemoryStorage struct {
	mu         sync.RWMutex
	emails     map[string]*email.ListedEmail
	raw        map[string][]byte
	drafts     map[string]map[string][]byte
	sessionIDs map[string]map[string]string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		emails:     make(map[string]*email.ListedEmail),
		raw:        make(map[string][]byte),
		drafts:     make(map[string]map[string][]byte),
		sessionIDs: make(map[string]map[string]string),
	}
}

func (s *MemoryStorage) Name() string {
	return "MemoryStorage"
}

// AddEmail implements the Storage interface. The id is generated here so the
// caller can use it to create the message's metadata. New messages go to the
// account's inbox; there is no mailbox argument. When raw is non-nil it is
// retained so GetEmailRaw can return the original bytes.
func (s *MemoryStorage) AddEmail(_ context.Context, accountID string, message *email.UserEmail, raw []byte) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	if message == nil {
		return "", errors.New("invalid email")
	}
	id, err := newEmailID()
	if err != nil {
		return "", err
	}
	listed := &email.ListedEmail{
		Ref:   &email.EmailRef{ID: id, AccountID: accountID, MailboxID: "inbox"},
		Email: cloneMessage(message),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.emails[id]; ok {
		return "", errors.New("email already exists")
	}
	s.emails[id] = listed
	if raw != nil {
		s.raw[id] = append([]byte(nil), raw...)
	}
	return id, nil
}

// Add inserts a listed email. It is not part of Storage and exists for local
// development and tests, which supply their own ids and mailboxes.
func (s *MemoryStorage) Add(listed *email.ListedEmail) {
	if listed == nil || listed.Ref == nil || listed.Ref.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails[listed.Ref.ID] = clone(listed)
}

// ListEmails implements the Storage interface. Messages are matched by the
// account and mailbox on their refs, which is the in-memory equivalent of
// reading a user's mailbox prefix.
func (s *MemoryStorage) ListEmails(_ context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, fmt.Errorf("accountID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	emails := make([]*email.ListedEmail, 0, len(s.emails))
	for _, e := range s.emails {
		if !strings.EqualFold(e.Ref.AccountID, accountID) {
			continue
		}
		if mailboxID != "" && !strings.EqualFold(e.Ref.MailboxID, mailboxID) {
			continue
		}
		emails = append(emails, clone(e))
	}
	sort.Slice(emails, func(i, j int) bool {
		if emails[i].Email.Date.Equal(emails[j].Email.Date) {
			return emails[i].Ref.ID > emails[j].Ref.ID
		}
		return emails[i].Email.Date.After(emails[j].Email.Date)
	})
	return emails, nil
}

func (s *MemoryStorage) GetEmail(_ context.Context, id string) (*email.ListedEmail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.emails[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(e), nil
}

// GetEmailRaw implements the Storage interface. Messages added with raw bytes
// return those bytes verbatim; messages added only as parsed content are
// rendered as a minimal RFC 5322 message from their stored fields.
func (s *MemoryStorage) GetEmailRaw(_ context.Context, id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.emails[id]
	if !ok {
		return nil, ErrNotFound
	}
	if raw, ok := s.raw[id]; ok {
		return append([]byte(nil), raw...), nil
	}
	return email.RawFromUserEmail(e.Email), nil
}

// DeleteEmail implements the Storage interface.
func (s *MemoryStorage) DeleteEmail(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.emails[id]; !ok {
		return ErrNotFound
	}
	delete(s.emails, id)
	delete(s.raw, id)
	return nil
}

// PutDraft implements DraftStorage. Drafts are kept per session so a warm or
// cold Lambda can read them on the next request.
func (s *MemoryStorage) PutDraft(_ context.Context, sessionID, creationID string, message *email.UserEmail) (string, error) {
	id, err := draftKey(creationID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal draft: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts[sessionID] == nil {
		s.drafts[sessionID] = make(map[string][]byte)
	}
	s.drafts[sessionID][id] = body
	return id, nil
}

// GetDraft implements DraftStorage.
func (s *MemoryStorage) GetDraft(_ context.Context, sessionID, id string) (*email.UserEmail, error) {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(key, sessionRoot+"/"+sessionID+"/")

	s.mu.RLock()
	defer s.mu.RUnlock()
	body, ok := s.drafts[sessionID][relative]
	if !ok {
		return nil, ErrNotFound
	}
	var message email.UserEmail
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("unmarshal draft: %w", err)
	}
	return &message, nil
}

// DeleteDraft implements DraftStorage. It is idempotent.
func (s *MemoryStorage) DeleteDraft(_ context.Context, sessionID, id string) error {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return err
	}
	relative := strings.TrimPrefix(key, sessionRoot+"/"+sessionID+"/")

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts[sessionID], relative)
	return nil
}

// LoadCreatedIDs implements SessionStateStore.
func (s *MemoryStorage) LoadCreatedIDs(_ context.Context, sessionID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make(map[string]string, len(s.sessionIDs[sessionID]))
	for id, real := range s.sessionIDs[sessionID] {
		ids[id] = real
	}
	return ids, nil
}

// SaveCreatedIDs implements SessionStateStore.
func (s *MemoryStorage) SaveCreatedIDs(_ context.Context, sessionID string, ids map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := make(map[string]string, len(ids))
	for id, real := range ids {
		copy[id] = real
	}
	s.sessionIDs[sessionID] = copy
	return nil
}

func clone(listed *email.ListedEmail) *email.ListedEmail {
	copied := &email.ListedEmail{
		Ref:   nil,
		Email: cloneMessage(listed.Email),
	}
	if listed.Ref != nil {
		ref := *listed.Ref
		copied.Ref = &ref
	}
	return copied
}

func cloneMessage(e *email.UserEmail) *email.UserEmail {
	if e == nil {
		return nil
	}
	copied := *e
	copied.ReplyTo = append([]email.Address(nil), e.ReplyTo...)
	copied.To = append([]email.Address(nil), e.To...)
	copied.Cc = append([]email.Address(nil), e.Cc...)
	copied.Bcc = append([]email.Address(nil), e.Bcc...)
	return &copied
}

// newEmailID returns a unique id for a locally added message.
func newEmailID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate email id: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}
