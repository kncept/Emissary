// Package fsutil holds the small filesystem helpers shared by the local
// (filesystem-backed) storage, user manager and metadata implementations.
// In particular it keeps key validation in one place: logical keys come from
// JMAP ids and user-supplied addresses, so they must never be allowed to
// escape the configured root directory.
package fsutil

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ExpandDir expands a leading "~" (or "~/") to the current user's home
// directory and cleans the result. An empty value stays empty so callers can
// apply their own defaults.
func ExpandDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", dir, err)
		}
		if dir == "~" {
			return home, nil
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(dir, "~/"))), nil
	}
	return filepath.Clean(dir), nil
}

// ResolveKey returns the path for a logical, slash-separated key under root.
// It rejects absolute keys, backslashes, NUL bytes and any ".." segment, and
// verifies the result is still under root after cleaning.
func ResolveKey(root, key string) (string, error) {
	if key = strings.TrimSpace(key); key == "" {
		return "", fmt.Errorf("empty filesystem key")
	}
	if strings.ContainsRune(key, 0) || strings.Contains(key, "\\") {
		return "", fmt.Errorf("invalid filesystem key %q", key)
	}
	if strings.HasPrefix(key, "/") || filepath.IsAbs(key) || path.IsAbs(key) {
		return "", fmt.Errorf("invalid filesystem key %q", key)
	}
	key = strings.TrimPrefix(key, "/")
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." {
			return "", fmt.Errorf("invalid filesystem key %q", key)
		}
	}

	cleaned := path.Clean(key)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid filesystem key %q", key)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve filesystem root %q: %w", root, err)
	}
	candidate := filepath.Join(rootAbs, filepath.FromSlash(cleaned))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("filesystem key %q escapes root %q", key, root)
	}
	return candidate, nil
}

// WriteFileAtomically writes data to path via a temporary file in the same
// directory, then renames it into place. Concurrent readers therefore see
// either the old or the new complete file, never a partial write.
func WriteFileAtomically(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".emissary-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("set permissions on %q: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary file %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary file %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temporary file to %q: %w", path, err)
	}
	return nil
}
