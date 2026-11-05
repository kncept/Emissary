package metadata

import (
	"context"
	"sync"
)

var _ Metadata = (*MemoryMetadata)(nil)

// MemoryMetadata is the default metadata provider. Read state is lost when the
// process exits.
type MemoryMetadata struct {
	mu   sync.RWMutex
	read map[string]bool
}

func NewMemoryMetadata() *MemoryMetadata {
	return &MemoryMetadata{read: make(map[string]bool)}
}

func (*MemoryMetadata) Name() string {
	return "MemoryMetadata"
}

func (m *MemoryMetadata) IsRead(_ context.Context, emailID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.read[emailID], nil
}

func (m *MemoryMetadata) MarkRead(_ context.Context, emailID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.read[emailID] = true
	return nil
}

func (m *MemoryMetadata) MarkUnread(_ context.Context, emailID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.read, emailID)
	return nil
}
