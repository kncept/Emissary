package storage

import (
	"fmt"
	"regexp"
	"strings"
)

// sessionRoot is the S3 prefix for per-session scratch state. It is kept
// separate from the users/ tree so session drafts are never mistaken for
// delivered mail by mailbox listings.
const sessionRoot = "_storage"

// createdIDsFile is the JSON object holding the session's JMAP creation-id
// map: client creation id -> server-assigned id.
const createdIDsFile = "created-ids.json"

// idPattern matches the characters JMAP allows in ids and creation ids. It is
// restrictive on purpose: these values become S3 key segments, so path
// separators and traversal sequences must never be accepted.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// validateSessionID validates a JWT session id before it is used as a key
// segment. Emissary session ids are base64url, which fits this pattern.
func validateSessionID(sessionID string) error {
	if !idPattern.MatchString(sessionID) {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	return nil
}

// draftKey returns the session-relative key for a draft creation id.
func draftKey(creationID string) (string, error) {
	if !idPattern.MatchString(creationID) {
		return "", fmt.Errorf("invalid draft creation id %q", creationID)
	}
	return "drafts/" + creationID + ".json", nil
}

// sessionObjectKey joins a validated session id and a session-relative key
// into a full object key. Relative keys produced by draftKey are accepted, as
// are full keys already rooted at _storage/<sid>/ (for clients echoing back an
// id from a previous response).
func sessionObjectKey(sessionID, relative string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	prefix := sessionRoot + "/" + sessionID + "/"
	relative = strings.TrimPrefix(relative, "/")
	if strings.Contains(relative, "..") || strings.Contains(relative, "\\") {
		return "", fmt.Errorf("invalid session-relative key %q", relative)
	}
	if strings.HasPrefix(relative, prefix) {
		return relative, nil
	}
	return prefix + relative, nil
}

// sessionCreatedIDsKey returns the object key for the session's creation-id map.
func sessionCreatedIDsKey(sessionID string) (string, error) {
	return sessionObjectKey(sessionID, createdIDsFile)
}
