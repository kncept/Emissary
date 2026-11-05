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
		f.data[entry.ID] = entry
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
		return EmailMetadata{ID: id}, nil
	}
	return m, nil
}

func (f *FileMetadata) SetRead(ctx context.Context, id string, read bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	m, ok := f.data[id]
	if !ok {
		m = EmailMetadata{ID: id}
	}
	m.Read = read
	f.data[id] = m
	return f.saveLocked()
}

func (f *FileMetadata) SetFlagged(ctx context.Context, id string, flagged bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	m, ok := f.data[id]
	if !ok {
		m = EmailMetadata{ID: id}
	}
	m.Flagged = flagged
	f.data[id] = m
	return f.saveLocked()
}

func (f *FileMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make([]EmailMetadata, 0, len(f.data))
	for _, v := range f.data {
		result = append(result, v)
	}
	return result, nil
}

func (f *FileMetadata) IsRead(ctx context.Context, id string) (bool, error) {
	metadata, err := f.Get(ctx, id)
	if err != nil {
		return false, err
	}
	return metadata.Read, nil
}

func (f *FileMetadata) MarkRead(ctx context.Context, id string) error {
	return f.SetRead(ctx, id, true)
}

func (f *FileMetadata) MarkUnread(ctx context.Context, id string) error {
	return f.SetRead(ctx, id, false)
}
