package storage

import (
	"context"
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
	emails     map[string]*email.UserEmail
	drafts     map[string]map[string][]byte
	sessionIDs map[string]map[string]string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		emails:     make(map[string]*email.UserEmail),
		drafts:     make(map[string]map[string][]byte),
		sessionIDs: make(map[string]map[string]string),
	}
}

func (s *MemoryStorage) Name() string {
	return "MemoryStorage"
}

// AddEmail implements the Storage interface.
func (s *MemoryStorage) AddEmail(_ context.Context, e *email.UserEmail) error {
	if e == nil || e.ID == "" {
		return errors.New("invalid email")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.emails[e.ID]; ok {
		return errors.New("email already exists")
	}
	s.emails[e.ID] = clone(e)
	return nil
}

// Add inserts an email. It is not part of Storage and exists for local
// development and tests.
func (s *MemoryStorage) Add(e *email.UserEmail) {
	if e == nil || e.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails[e.ID] = clone(e)
}

// ListAccountEmails implements the Storage interface. Messages are matched by
// the account and mailbox recorded on them (derived from the delivered object
// key), which is the in-memory equivalent of reading a user's mailbox prefix.
func (s *MemoryStorage) ListAccountEmails(_ context.Context, accountID, mailboxID string) ([]*email.UserEmail, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, fmt.Errorf("accountID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	emails := make([]*email.UserEmail, 0, len(s.emails))
	for _, e := range s.emails {
		if !strings.EqualFold(e.AccountID, accountID) {
			continue
		}
		if mailboxID != "" && !strings.EqualFold(e.MailboxID, mailboxID) {
			continue
		}
		emails = append(emails, clone(e))
	}
	sort.Slice(emails, func(i, j int) bool {
		if emails[i].Date.Equal(emails[j].Date) {
			return emails[i].ID > emails[j].ID
		}
		return emails[i].Date.After(emails[j].Date)
	})
	return emails, nil
}

func (s *MemoryStorage) GetEmail(_ context.Context, id string) (*email.UserEmail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.emails[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(e), nil
}

// GetEmailRaw implements the Storage interface. The in-memory store holds only
// parsed messages, so it renders a minimal RFC 5322 message from the stored
// fields. This is useful for local development; production stores such as S3
// return the original delivered bytes instead.
func (s *MemoryStorage) GetEmailRaw(_ context.Context, id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.emails[id]
	if !ok {
		return nil, ErrNotFound
	}
	return email.RawFromUserEmail(e), nil
}

// DeleteEmail implements the Storage interface.
func (s *MemoryStorage) DeleteEmail(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.emails[id]; !ok {
		return ErrNotFound
	}
	delete(s.emails, id)
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

func clone(e *email.UserEmail) *email.UserEmail {
	copied := *e
	copied.ReplyTo = append([]email.Address(nil), e.ReplyTo...)
	copied.To = append([]email.Address(nil), e.To...)
	copied.Cc = append([]email.Address(nil), e.Cc...)
	copied.Bcc = append([]email.Address(nil), e.Bcc...)
	return &copied
}
