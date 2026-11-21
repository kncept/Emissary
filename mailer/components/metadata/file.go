package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/fsutil"
)

// FileMetadata has two on-disk layouts:
//
//   - directory mode: one JSON file per record, at <dir>/<userID>/<mailID>.json.
//     This is the default (METADATA=file with FILESYSTEM_DIR) and mirrors the
//     filesystem storage and user-manager layouts.
//   - legacy file mode: one JSON array holding every record. A path with a
//     .json suffix, or an existing regular file, keeps this old layout so
//     existing METADATA_FILE deployments and data continue to work.
type FileMetadata struct {
	path      string
	directory bool
	mu        sync.RWMutex // guards directory-mode record files
	core      *memoryStore // legacy file mode only
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
		core: newMemoryStore(),
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
		f.core.save(entry)
	}
	return nil
}

// saveLocked persists the legacy whole-file layout. The caller must hold the
// lock (read or write).
func (f *FileMetadata) saveLocked() error {
	entries := f.core.snapshotAll()
	if entries == nil {
		entries = []EmailMetadata{}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return fsutil.WriteFileAtomically(f.path, data, 0644)
}

func (f *FileMetadata) recordPath(userID, mailID string) (string, error) {
	key := filepath.ToSlash(filepath.Join(strings.TrimPrefix(userID, "/"), strings.TrimPrefix(mailID, "/"))) + ".json"
	return fsutil.ResolveKey(f.path, key)
}

func (f *FileMetadata) legacyRecordPath(mailID string) (string, error) {
	key := strings.TrimPrefix(mailID, "/") + ".json"
	return fsutil.ResolveKey(f.path, key)
}

func (f *FileMetadata) Name() string { return "file" }

func (f *FileMetadata) Get(_ context.Context, userID, mailID string) (EmailMetadata, error) {
	if !f.directory {
		if m, ok := f.core.get(userID, mailID); ok {
			return m, nil
		}
		return EmptyEmailMetadata(userID, mailID), nil
	}

	path, err := f.recordPath(userID, mailID)
	if err != nil {
		return EmptyEmailMetadata(userID, mailID), err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Backward compatibility: older directory-mode records were stored
			// by mail id only.
			legacyPath, lerr := f.legacyRecordPath(mailID)
			if lerr == nil && legacyPath != path {
				if lbody, lreadErr := os.ReadFile(legacyPath); lreadErr == nil {
					body = lbody
					err = nil
				} else if os.IsNotExist(lreadErr) {
					return EmptyEmailMetadata(userID, mailID), nil
				} else {
					return EmptyEmailMetadata(userID, mailID), fmt.Errorf("failed to read metadata for %q/%q: %w", userID, mailID, lreadErr)
				}
			} else {
				return EmptyEmailMetadata(userID, mailID), nil
			}
		} else {
			return EmptyEmailMetadata(userID, mailID), fmt.Errorf("failed to read metadata for %q/%q: %w", userID, mailID, err)
		}
	}
	var m EmailMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return EmptyEmailMetadata(userID, mailID), fmt.Errorf("failed to unmarshal metadata for %q/%q: %w", userID, mailID, err)
	}
	return m.Normalize(userID, mailID), nil
}

func (f *FileMetadata) Save(_ context.Context, metadata EmailMetadata) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.core.save(metadata)
		return f.saveLocked()
	}

	path, err := f.recordPath(metadata.UserID, metadata.ID)
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

func (f *FileMetadata) Delete(_ context.Context, userID, mailID string) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.core.delete(userID, mailID)
		return f.saveLocked()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	path, err := f.recordPath(userID, mailID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata for %q/%q: %w", userID, mailID, err)
	}
	legacyPath, lerr := f.legacyRecordPath(mailID)
	if lerr == nil && legacyPath != path {
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete metadata for %q/%q: %w", userID, mailID, err)
		}
	}
	return nil
}

func (f *FileMetadata) List(_ context.Context, userID string) ([]EmailMetadata, error) {
	if !f.directory {
		return f.core.list(userID), nil
	}

	var result []EmailMetadata
	userDir := filepath.Join(f.path, filepath.FromSlash(strings.TrimPrefix(userID, "/")))
	err := filepath.WalkDir(userDir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(userDir, p)
		if err != nil {
			return err
		}
		mailID := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		body, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("failed to read metadata for %q/%q: %w", userID, mailID, err)
		}
		var m EmailMetadata
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("failed to unmarshal metadata for %q/%q: %w", userID, mailID, err)
		}
		result = append(result, m.Normalize(userID, mailID))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortMetadata(result)
	if result == nil {
		result = []EmailMetadata{}
	}
	return result, nil
}
