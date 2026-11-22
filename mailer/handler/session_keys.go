package handler

import (
	"sort"

	"github.com/kncept/Emissary/mailer/auth"
)

// JMAPSessionKeys returns the sorted top-level keys of the runtime Session
// object, including the "state" token that handleJMAPSession adds after
// buildJMAPSessionMap. The api/spec_test.go conformance test uses this to keep
// the Session schema's `required` list in lockstep with what the server
// actually returns, without duplicating the list in the spec test.
func JMAPSessionKeys() []string {
	claims := &auth.Claims{Subject: "spec-test"}
	session := buildJMAPSessionMap(claims, 1, "https://example.test", "https://example.test")
	session["state"] = "0"
	keys := make([]string, 0, len(session))
	for key := range session {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
