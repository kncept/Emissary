package receiver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/fsutil"
)

// FileConfigStore stores one JSON file per mail identity at
// <dir>/receiver/<identity>.json, where dir is the shared FILESYSTEM_DIR root.
type FileConfigStore struct {
	dir string
}

var _ ConfigStore = (*FileConfigStore)(nil)

func NewFileConfigStore(dir string) (*FileConfigStore, error) {
	expanded, err := fsutil.ExpandDir(dir)
	if err != nil {
		return nil, err
	}
	if expanded == "" {
		return nil, fmt.Errorf("receiver config directory is required")
	}
	return &FileConfigStore{dir: filepath.Join(expanded, configPrefix)}, nil
}

func (f *FileConfigStore) Name() string { return "file" }

func (f *FileConfigStore) objectPath(identity string) (string, error) {
	if err := validateConfigKey(identity); err != nil {
		return "", err
	}
	return fsutil.ResolveKey(f.dir, normalizeIdentity(identity)+".json")
}

func (f *FileConfigStore) Get(_ context.Context, identity string) (HandlerRules, error) {
	path, err := f.objectPath(identity)
	if err != nil {
		return HandlerRules{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HandlerRules{}, fmt.Errorf("%w: %q", ErrIdentityConfigNotFound, identity)
		}
		return HandlerRules{}, fmt.Errorf("read receiver config %q: %w", identity, err)
	}
	var rules HandlerRules
	if err := json.Unmarshal(body, &rules); err != nil {
		return HandlerRules{}, fmt.Errorf("unmarshal receiver config %q: %w", identity, err)
	}
	return rules, nil
}

func (f *FileConfigStore) Put(_ context.Context, identity string, rules HandlerRules) error {
	path, err := f.objectPath(identity)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal receiver config %q: %w", identity, err)
	}
	if err := fsutil.WriteFileAtomically(path, body, 0644); err != nil {
		return fmt.Errorf("write receiver config %q: %w", identity, err)
	}
	return nil
}

func (f *FileConfigStore) Delete(_ context.Context, identity string) error {
	if err := validateDeletableConfigKey(identity); err != nil {
		return err
	}
	path, err := f.objectPath(identity)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete receiver config %q: %w", identity, err)
	}
	return nil
}

func (f *FileConfigStore) List(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list receiver config directory: %w", err)
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		keys = append(keys, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(keys)
	return keys, nil
}
