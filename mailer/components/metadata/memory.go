package metadata

import (
	"context"
	"sort"
	"sync"
)

// memoryStore is the shared user -> mail -> metadata map used by the in-memory
// backend and by FileMetadata's legacy whole-file layout. It owns its lock so
// callers cannot forget to normalize or clone values on the way in and out.
type memoryStore struct {
	mu   sync.RWMutex
	data map[string]map[string]EmailMetadata
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string]map[string]EmailMetadata)}
}

func (s *memoryStore) get(userID, mailID string) (EmailMetadata, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byUser, ok := s.data[userID]
	if !ok {
		return EmailMetadata{}, false
	}
	metadata, ok := byUser[mailID]
	if !ok {
		return EmailMetadata{}, false
	}
	return metadata.Normalize(userID, mailID), true
}

func (s *memoryStore) save(metadata EmailMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[metadata.UserID] == nil {
		s.data[metadata.UserID] = make(map[string]EmailMetadata)
	}
	s.data[metadata.UserID][metadata.ID] = metadata.Clone()
}

func (s *memoryStore) delete(userID, mailID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byUser, ok := s.data[userID]
	if !ok {
		return
	}
	delete(byUser, mailID)
	if len(byUser) == 0 {
		delete(s.data, userID)
	}
}

func (s *memoryStore) list(userID string) []EmailMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]EmailMetadata, 0, len(s.data[userID]))
	for id, metadata := range s.data[userID] {
		result = append(result, metadata.Normalize(userID, id))
	}
	sortMetadata(result)
	return result
}

// snapshotAll returns every record sorted by user and mail id. It is used by
// the legacy file backend to persist a deterministic JSON array.
func (s *memoryStore) snapshotAll() []EmailMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]string, 0, len(s.data))
	for userID := range s.data {
		users = append(users, userID)
	}
	sort.Strings(users)

	result := make([]EmailMetadata, 0)
	for _, userID := range users {
		ids := make([]string, 0, len(s.data[userID]))
		for id := range s.data[userID] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			result = append(result, s.data[userID][id].Clone())
		}
	}
	return result
}

type MemoryMetadata struct {
	core *memoryStore
}

var _ MetadataStore = (*MemoryMetadata)(nil)

func NewMemoryMetadata() *MemoryMetadata {
	return &MemoryMetadata{core: newMemoryStore()}
}

func (m *MemoryMetadata) Name() string { return "memory" }

func (m *MemoryMetadata) Get(_ context.Context, userID, mailID string) (EmailMetadata, error) {
	if metadata, ok := m.core.get(userID, mailID); ok {
		return metadata, nil
	}
	// Missing metadata is normal for older messages: JMAP must still return
	// the email, with no keywords and no fabricated receive time.
	return EmptyEmailMetadata(userID, mailID), nil
}

func (m *MemoryMetadata) Save(_ context.Context, metadata EmailMetadata) error {
	m.core.save(metadata)
	return nil
}

func (m *MemoryMetadata) Delete(_ context.Context, userID, mailID string) error {
	m.core.delete(userID, mailID)
	return nil
}

func (m *MemoryMetadata) List(_ context.Context, userID string) ([]EmailMetadata, error) {
	return m.core.list(userID), nil
}
