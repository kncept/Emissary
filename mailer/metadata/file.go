package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type FileMetadata struct {
	path string
	mu   sync.RWMutex
	data map[string]EmailMetadata
}

func NewFileMetadata(path string) (*FileMetadata, error) {
	fm := &FileMetadata{
		path: path,
		data: make(map[string]EmailMetadata),
	}
	if err := fm.load(); err != nil {
		return nil, err
	}
	return fm, nil
}

func (f *FileMetadata) load() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, err := os.Stat(f.path); os.IsNotExist(err) {
		return nil
	}

	data, err := os.ReadFile(f.path)
	if err != nil {
		return fmt.Errorf("failed to read metadata file: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	var entries []EmailMetadata
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	for _, entry := range entries {
		f.data[entry.ID] = entry.Clone()
	}
	return nil
}

// saveLocked persists the current state to disk. The caller must hold
// the lock (read or write).
func (f *FileMetadata) saveLocked() error {
	entries := make([]EmailMetadata, 0, len(f.data))
	for _, v := range f.data {
		entries = append(entries, v)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
		return fmt.Errorf("failed to create metadata dir: %w", err)
	}

	return os.WriteFile(f.path, data, 0644)
}

func (f *FileMetadata) Name() string { return "file" }

func (f *FileMetadata) Get(ctx context.Context, id string) (EmailMetadata, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	m, ok := f.data[id]
	if !ok {
		// Older messages may have no metadata record.
		return EmptyEmailMetadata(id), nil
	}
	return m.Clone(), nil
}

func (f *FileMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.data[metadata.ID] = metadata.Clone()
	return f.saveLocked()
}

func (f *FileMetadata) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.data, id)
	return f.saveLocked()
}

func (f *FileMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make([]EmailMetadata, 0, len(f.data))
	for _, v := range f.data {
		result = append(result, v.Clone())
	}
	return result, nil
}
