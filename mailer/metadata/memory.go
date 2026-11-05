package metadata

import (
	"sync"
)

type MemoryMetadata struct {
	mu   sync.RWMutex
	data map[string]EmailMetadata
}

type EmailMetadata struct {
	ID      string
	Read    bool
	Flagged bool
}

func NewMemoryMetadata() *MemoryMetadata {
	return &MemoryMetadata{
		data: make(map[string]EmailMetadata),
	}
}

func (m *MemoryMetadata) Get(id string) (EmailMetadata, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	metadata, ok := m.data[id]
	return metadata, ok
}

func (m *MemoryMetadata) SetRead(id string, read bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metadata, ok := m.data[id]
	if !ok {
		metadata = EmailMetadata{ID: id}
	}
	metadata.Read = read
	m.data[id] = metadata
}

func (m *MemoryMetadata) SetFlagged(id string, flagged bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metadata, ok := m.data[id]
	if !ok {
		metadata = EmailMetadata{ID: id}
	}
	metadata.Flagged = flagged
	m.data[id] = metadata
}

func (m *MemoryMetadata) List() []EmailMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]EmailMetadata, 0, len(m.data))
	for _, v := range m.data {
		result = append(result, v)
	}
	return result
}