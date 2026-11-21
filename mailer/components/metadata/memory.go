package metadata

import (
	"context"
	"sync"
)

type MemoryMetadata struct {
	mu   sync.RWMutex
	data map[string]map[string]EmailMetadata // userID -> mailID -> metadata
}

func NewMemoryMetadata() *MemoryMetadata {
	return &MemoryMetadata{
		data: make(map[string]map[string]EmailMetadata),
	}
}

func (m *MemoryMetadata) Name() string { return "memory" }

func (m *MemoryMetadata) Get(ctx context.Context, userID, mailID string) (EmailMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if byUser, ok := m.data[userID]; ok {
		if metadata, ok := byUser[mailID]; ok {
			return metadata.Clone(), nil
		}
	}
	// Missing metadata is normal for older messages: JMAP must still return
	// the email, with no keywords and no fabricated receive time.
	return EmptyEmailMetadata(userID, mailID), nil
}

func (m *MemoryMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	byUser, ok := m.data[metadata.UserID]
	if !ok {
		byUser = make(map[string]EmailMetadata)
		m.data[metadata.UserID] = byUser
	}
	byUser[metadata.ID] = metadata.Clone()
	return nil
}

func (m *MemoryMetadata) Delete(ctx context.Context, userID, mailID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if byUser, ok := m.data[userID]; ok {
		delete(byUser, mailID)
		if len(byUser) == 0 {
			delete(m.data, userID)
		}
	}
	return nil
}

func (m *MemoryMetadata) List(ctx context.Context, userID string) ([]EmailMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	byUser := m.data[userID]
	result := make([]EmailMetadata, 0, len(byUser))
	for _, v := range byUser {
		result = append(result, v.Clone())
	}
	return result, nil
}
