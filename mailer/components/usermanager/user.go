package usermanager

import "strings"

// normalizeUserID is the stable id derived from a supplied address: the
// normalised email when it parses as one, otherwise the lowercased trimmed
// value. Create rejects values that are not valid addresses; Get/Save/Delete
// accept them so legacy records remain reachable.
func normalizeUserID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if address, ok := normalizeAddress(value); ok {
		return address
	}
	return strings.ToLower(value)
}
