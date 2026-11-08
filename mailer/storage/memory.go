package storage

import (
	"context"
	"sort"
	"sync"

	"github.com/kncept/Emissary/mailer/email"
)

var _ Storage = (*MemoryStorage)(nil)

// MemoryStorage is a small in-memory store. It is useful for local
// development and tests; messages are lost when the process exits.
type MemoryStorage struct {
	mu     sync.RWMutex
	emails map[string]*email.UserEmail
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{emails: make(map[string]*email.UserEmail)}
}

func (s *MemoryStorage) Name() string {
	return "MemoryStorage"
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

func (s *MemoryStorage) ListAllEmails(context.Context) ([]*email.UserEmail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	emails := make([]*email.UserEmail, 0, len(s.emails))
	for _, e := range s.emails {
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

func clone(e *email.UserEmail) *email.UserEmail {
	copied := *e
	copied.To = append([]email.Address(nil), e.To...)
	copied.Cc = append([]email.Address(nil), e.Cc...)
	copied.Bcc = append([]email.Address(nil), e.Bcc...)
	return &copied
}