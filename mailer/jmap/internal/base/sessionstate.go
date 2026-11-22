package base

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// SessionState computes the RFC 8620 section 2 Session `state` token from a
// complete Session object (the caller must omit the "state" key itself). It is
// a deterministic function of the advertised configuration, so every Lambda
// invocation with the same configuration computes the same token without any
// shared in-process state, and any change to capabilities, accounts, account
// capabilities, URLs, limits or the username changes the token.
func SessionState(session map[string]any) (string, error) {
	clone := make(map[string]any, len(session))
	for key, value := range session {
		clone[key] = value
	}
	delete(clone, "state")
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "s-" + base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}
