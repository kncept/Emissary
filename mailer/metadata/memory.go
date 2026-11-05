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
		return EmailMetadata{ID: id}, nil
	}
	return metadata, nil
}

func (m *MemoryMetadata) SetRead(ctx context.Context, id string, read bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	metadata, ok := m.data[id]
	if !ok {
		metadata = EmailMetadata{ID: id}
	}
	metadata.Read = read
	m.data[id] = metadata
	return nil
}

func (m *MemoryMetadata) SetFlagged(ctx context.Context, id string, flagged bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	metadata, ok := m.data[id]
	if !ok {
		metadata = EmailMetadata{ID: id}
	}
	metadata.Flagged = flagged
	m.data[id] = metadata
	return nil
}

func (m *MemoryMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]EmailMetadata, 0, len(m.data))
	for _, v := range m.data {
		result = append(result, v)
	}
	return result, nil
}

func (m *MemoryMetadata) IsRead(ctx context.Context, id string) (bool, error) {
	metadata, err := m.Get(ctx, id)
	if err != nil {
		return false, err
	}
	return metadata.Read, nil
}

func (m *MemoryMetadata) MarkRead(ctx context.Context, id string) error {
	return m.SetRead(ctx, id, true)
}

func (m *MemoryMetadata) MarkUnread(ctx context.Context, id string) error {
	return m.SetRead(ctx, id, false)
}
