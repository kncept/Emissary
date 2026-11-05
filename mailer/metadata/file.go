package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var _ Metadata = (*FileMetadata)(nil)

// FileMetadata persists read state in a JSON file. It is useful for a single
// long-running instance without a database.
type FileMetadata struct {
	mu   sync.RWMutex
	path string
	read map[string]bool
}

func NewFileMetadata(path string) (*FileMetadata, error) {
	if path == "" {
		return nil, errors.New("metadata file path is not configured (set EMISSARY_METADATA_FILE)")
	}

	m := &FileMetadata{path: path, read: make(map[string]bool)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata file: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m.read); err != nil {
			return nil, fmt.Errorf("parse metadata file: %w", err)
		}
	}
	return m, nil
}

func (*FileMetadata) Name() string {
	return "FileMetadata"
}

func (m *FileMetadata) IsRead(_ context.Context, emailID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.read[emailID], nil
}

func (m *FileMetadata) MarkRead(_ context.Context, emailID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.read[emailID] = true
	return m.saveLocked()
}

func (m *FileMetadata) MarkUnread(_ context.Context, emailID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.read, emailID)
	return m.saveLocked()
}

func (m *FileMetadata) saveLocked() error {
	data, err := json.MarshalIndent(m.read, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(m.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tempPath := m.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, m.path)
}
