package metadata

import (
	"context"
	"sync"
)

type MemoryMetadata struct {
	mu   sync.RWMutex
	data map[string]EmailMetadata
}

func NewMemoryMetadata() *MemoryMetadata {
	return &MemoryMetadata{
		data: make(map[string]EmailMetadata),
	}
}

func (m *MemoryMetadata) Name() string { return "memory" }

func (m *MemoryMetadata) Get(ctx context.Context, id string) (EmailMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metadata, ok := m.data[id]
	if !ok {
		// Missing metadata is normal for older messages: JMAP must still
		// return the email, with no keywords and no fabricated receive time.
		return EmptyEmailMetadata(id), nil
	}
	return metadata.Clone(), nil
}

func (m *MemoryMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[metadata.ID] = metadata.Clone()
	return nil
}

func (m *MemoryMetadata) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, id)
	return nil
}

func (m *MemoryMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]EmailMetadata, 0, len(m.data))
	for _, v := range m.data {
		result = append(result, v.Clone())
	}
	return result, nil
}
