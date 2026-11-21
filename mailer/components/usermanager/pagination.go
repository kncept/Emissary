package usermanager

import (
	"errors"
	"strings"

	"github.com/kncept/Emissary/mailer/email"
)

const (
	defaultUserPageSize = 20
	maxUserPageSize     = 100
)

// pageParams clamps pageSize and decodes a resume cursor. trimCursor keeps the
// historical backend difference explicit: filesystem and S3 trim the cursor
// before decoding, DynamoDB does not. A whitespace-only cursor means "start
// over" for every backend.
func pageParams(cursor string, pageSize int, trimCursor bool) (after string, size int, err error) {
	if pageSize <= 0 {
		pageSize = defaultUserPageSize
	}
	if pageSize > maxUserPageSize {
		pageSize = maxUserPageSize
	}
	if strings.TrimSpace(cursor) == "" {
		return "", pageSize, nil
	}
	if trimCursor {
		cursor = strings.TrimSpace(cursor)
	}
	decoded, err := decodeUserCursor(cursor)
	if err != nil {
		return "", 0, err
	}
	return decoded, pageSize, nil
}

// User cursors share email.EncodeID's raw base64url representation.
func encodeUserCursor(id string) string {
	return email.EncodeID(id)
}

// decodeUserCursor keeps the cursor-specific validation: malformed base64 and
// decoded values that are only whitespace are both rejected with the same
// error, regardless of the underlying decoder's wording.
func decodeUserCursor(cursor string) (string, error) {
	decoded, err := email.DecodeID(cursor)
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "", errors.New("invalid cursor")
	}
	return decoded, nil
}
