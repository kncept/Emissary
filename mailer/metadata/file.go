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
//   - directory mode: one JSON file per record, at <dir>/<userID>/<mailID>.json.
//     This is the default (METADATA=file with FILESYSTEM_DIR) and mirrors the
//     filesystem storage and user-manager layouts.
//   - legacy file mode: one JSON array holding every record. A path with a
//     .json suffix, or an existing regular file, keeps this old layout so
//     existing METADATA_FILE deployments and data continue to work.
type FileMetadata struct {
	path      string
	directory bool
	mu        sync.RWMutex
	data      map[string]map[string]EmailMetadata // legacy file mode only: userID -> mailID -> metadata
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
		data: make(map[string]map[string]EmailMetadata),
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
		m := entry.Clone()
		if f.data[m.UserID] == nil {
			f.data[m.UserID] = make(map[string]EmailMetadata)
		}
		f.data[m.UserID][m.ID] = m
	}
	return nil
}

// saveLocked persists the legacy whole-file layout. The caller must hold the
// lock (read or write).
func (f *FileMetadata) saveLocked() error {
	var entries []EmailMetadata
	users := make([]string, 0, len(f.data))
	for u := range f.data {
		users = append(users, u)
	}
	sort.Strings(users)
	for _, u := range users {
		ids := make([]string, 0, len(f.data[u]))
		for id := range f.data[u] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entries = append(entries, f.data[u][id])
		}
	}
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

func (f *FileMetadata) Get(ctx context.Context, userID, mailID string) (EmailMetadata, error) {
	if !f.directory {
		f.mu.RLock()
		defer f.mu.RUnlock()
		if byUser, ok := f.data[userID]; ok {
			if m, ok := byUser[mailID]; ok {
				return m.Clone(), nil
			}
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
	if m.UserID == "" {
		m.UserID = userID
	}
	m.ID = mailID
	return m.Clone(), nil
}

func (f *FileMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.data[metadata.UserID] == nil {
			f.data[metadata.UserID] = make(map[string]EmailMetadata)
		}
		f.data[metadata.UserID][metadata.ID] = metadata.Clone()
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

func (f *FileMetadata) Delete(ctx context.Context, userID, mailID string) error {
	if !f.directory {
		f.mu.Lock()
		defer f.mu.Unlock()
		if byUser, ok := f.data[userID]; ok {
			delete(byUser, mailID)
			if len(byUser) == 0 {
				delete(f.data, userID)
			}
		}
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

func (f *FileMetadata) List(ctx context.Context, userID string) ([]EmailMetadata, error) {
	if !f.directory {
		f.mu.RLock()
		defer f.mu.RUnlock()
		byUser := f.data[userID]
		result := make([]EmailMetadata, 0, len(byUser))
		for _, v := range byUser {
			result = append(result, v.Clone())
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result, nil
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
		if m.UserID == "" {
			m.UserID = userID
		}
		m.ID = mailID
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
