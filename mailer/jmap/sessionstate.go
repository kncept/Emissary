package jmap

import (
	jmapbase "github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// SessionState returns the RFC 8620 section 2 Session state token for a
// complete Session object (without its "state" key). It re-exports the shared
// helper in internal/base for callers outside the jmap tree, such as the HTTP
// handler that builds the Session resource.
func SessionState(session map[string]any) (string, error) {
	return jmapbase.SessionState(session)
}
