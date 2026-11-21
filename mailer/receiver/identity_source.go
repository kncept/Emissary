package receiver

import "sync/atomic"

// MailIdentityConfigSource supplies the currently published receiver rules.
// It is deliberately read-only so a staged-message processor cannot mutate the
// rules while the support API is saving them.
type MailIdentityConfigSource interface {
	MailIdentityConfig() MailIdentityConfig
}

// MutableMailIdentityConfigSource is a concurrency-safe holder for an immutable
// rules snapshot. Publishing a new map never mutates one already being read.
type MutableMailIdentityConfigSource struct {
	current atomic.Pointer[MailIdentityConfig]
}

var _ MailIdentityConfigSource = (*MutableMailIdentityConfigSource)(nil)

// NewMailIdentityConfigSource creates a source holding initial. A nil initial
// value is treated as "not configured"; readers then receive nil.
func NewMailIdentityConfigSource(initial MailIdentityConfig) *MutableMailIdentityConfigSource {
	source := &MutableMailIdentityConfigSource{}
	source.Store(initial)
	return source
}

// Store publishes a new rules snapshot. Nil values are ignored so a failed
// load cannot erase the last known-good rules.
func (s *MutableMailIdentityConfigSource) Store(config MailIdentityConfig) {
	if config == nil {
		return
	}
	s.current.Store(&config)
}

// MailIdentityConfig returns the currently published snapshot. Callers that
// need to modify it must clone it first.
func (s *MutableMailIdentityConfigSource) MailIdentityConfig() MailIdentityConfig {
	current := s.current.Load()
	if current == nil {
		return nil
	}
	return *current
}
