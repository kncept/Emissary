package storage

import (
	"context"
	"os"

	"github.com/kncept/Emissary/mailer/fsutil"
)

// LoadJMAPState implements JMAPStateStore. The revision is a hash of the stored
// bytes, which gives the file backend the same optimistic-concurrency contract
// as S3's ETag.
func (s *FileStorage) LoadJMAPState(_ context.Context, accountID string) ([]byte, string, error) {
	key, err := jmapStateKey(accountID)
	if err != nil {
		return nil, "", err
	}
	path, err := s.objectPath(key)
	if err != nil {
		return nil, "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", jmapStateSaveError(key, err)
	}
	return data, fileStateRevision(data), nil
}

// SaveJMAPState implements JMAPStateStore. The check and the atomic replace are
// serialised within the process; across processes the content hash still
// detects a stale writer in the common case.
func (s *FileStorage) SaveJMAPState(_ context.Context, accountID string, data []byte, revision string) (string, error) {
	key, err := jmapStateKey(accountID)
	if err != nil {
		return "", err
	}
	path, err := s.objectPath(key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(path)
	switch {
	case err == nil && fileStateRevision(current) != revision:
		return "", ErrJMAPStateConflict
	case err != nil && !os.IsNotExist(err):
		return "", jmapStateSaveError(key, err)
	case os.IsNotExist(err) && revision != "":
		return "", ErrJMAPStateConflict
	}
	newRevision := fileStateRevision(data)
	if err := fsutil.WriteFileAtomically(path, data, 0600); err != nil {
		return "", jmapStateSaveError(key, err)
	}
	return newRevision, nil
}
