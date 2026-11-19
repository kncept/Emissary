package receiver

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemoryConfigStore holds receiver configurations for the lifetime of the
// process. It is the default when no persistent backend is configured.
type MemoryConfigStore struct {
	mu   sync.RWMutex
	data map[string][]HandlerRules
}

var _ ConfigStore = (*MemoryConfigStore)(nil)

func NewMemoryConfigStore() *MemoryConfigStore {
	return &MemoryConfigStore{data: make(map[string][]HandlerRules)}
}

func (m *MemoryConfigStore) Name() string { return "memory" }

func (m *MemoryConfigStore) Get(_ context.Context, identity string) ([]HandlerRules, error) {
	key := normalizeIdentity(identity)
	m.mu.RLock()
	defer m.mu.RUnlock()
	rules, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrIdentityConfigNotFound, identity)
	}
	return cloneRules(rules), nil
}

func (m *MemoryConfigStore) Put(_ context.Context, identity string, rules []HandlerRules) error {
	if err := validateConfigKey(identity); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[normalizeIdentity(identity)] = cloneRules(rules)
	return nil
}

func (m *MemoryConfigStore) List(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.data))
	for key := range m.data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
