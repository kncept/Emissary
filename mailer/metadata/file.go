package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/fsutil"
)

// FileMetadata has two on-disk layouts:
//
//   - directory mode: one JSON file per email, at <dir>/<id>.json. This is the
//     default (METADATA=file with FILESYSTEM_DIR) and mirrors the filesystem
//     storage and user-manager layouts.
//   - legacy file mode: one JSON array holding every record. A path with a
//     .json suffix, or an existing regular file, keeps this old layout so
//     existing METADATA_FILE deployments and data continue to work.
type FileMetadata struct {
	path      string
	directory bool
	mu        sync.RWMutex
	data      map[string]EmailMetadata // legacy file mode only
}

func NewFileMetadata(path string) (*FileMetadata, error) {
	expanded, err := fsutil.ExpandDir(path)
	if err != nil {
		return nil, err
	}
	if expanded == "" {
		return nil, fmt.Errorf("metadata file path is required")
	}

	fm := &FileMetadata{
		path: expanded,
		data: make(map[string]EmailMetadata),
	}
	info, err := os.Stat(expanded)
	switch {
	case err == nil && !info.IsDir():
		fm.directory = false
	case err == nil:
		fm.directory = true
	case os.IsNotExist(err):
		fm.directory = !strings.HasSuffix(strings.ToLower(expanded), ".json")
	default:
		return nil, fmt.Errorf("failed to stat metadata path: %w", err)
	}

	if !fm.directory {
		if err := fm.load(); err != nil {
			return nil, err
		}
	}
	return fm, nil
}

func (f *FileMetadata) load() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
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

// saveLocked persists the legacy whole-file layout. The caller must hold the
// lock (read or write).
func (f *FileMetadata) saveLocked() error {
	entries := make([]EmailMetadata, 0, len(f.data))
	for _, v := range f.data {
		entries = append(entries, v)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return fsutil.WriteFileAtomically(f.path, data, 0644)
}

func (f *FileMetadata) recordPath(id string) (string, error) {
	key := strings.TrimPrefix(id, "/") + ".json"
	return fsutil.ResolveKey(f.path, key)
}

func (f *FileMetadata) Name() string { return "file" }

func (f *FileMetadata) Get(ctx context.Context, id string) (EmailMetadata, error) {
	if !f.directory {
		f.mu.RLock()
		defer f.mu.RUnlock()
		m, ok := f.data[id]
		if !ok {
			// Older messages may have no metadata record.
			return EmptyEmailMetadata(id), nil
		}
		return m.Clone(), nil
	}

	path, err := f.recordPath(id)
	if err != nil {
		return EmptyEmailMetadata(id), err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EmptyEmailMetadata(id), nil
		}
		return EmptyEmailMetadata(id), fmt.Errorf("failed to read metadata for %q: %w", id, err)
	}
	var m EmailMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return EmptyEmailMetadata(id), fmt.Errorf("failed to unmarshal metadata for %q: %w", id, err)
	}
	m.ID = id
	return m.Clone(), nil
}

func (f *FileMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.data[metadata.ID] = metadata.Clone()
		return f.saveLocked()
	}

	path, err := f.recordPath(metadata.ID)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(metadata.Clone(), "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return fsutil.WriteFileAtomically(path, body, 0644)
}

func (f *FileMetadata) Delete(ctx context.Context, id string) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.data, id)
		return f.saveLocked()
	}

	path, err := f.recordPath(id)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata for %q: %w", id, err)
	}
	return nil
}

func (f *FileMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	if !f.directory {
		f.mu.RLock()
		defer f.mu.RUnlock()
		result := make([]EmailMetadata, 0, len(f.data))
		for _, v := range f.data {
			result = append(result, v.Clone())
		}
		return result, nil
	}

	var result []EmailMetadata
	err := filepath.WalkDir(f.path, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(f.path, p)
		if err != nil {
			return err
		}
		id := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		body, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("failed to read metadata for %q: %w", id, err)
		}
		var m EmailMetadata
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("failed to unmarshal metadata for %q: %w", id, err)
		}
		m.ID = id
		result = append(result, m.Clone())
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if result == nil {
		result = []EmailMetadata{}
	}
	return result, nil
}
