package base

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
)

// EmailMetadata returns the metadata for an email. A missing record (or a
// backend error) is treated as empty so JMAP responses always succeed even for
// messages stored before metadata existed; no receive time is fabricated.
func EmailMetadata(ctx context.Context, deps *Deps, userID, mailID string) metadata.EmailMetadata {
	meta, err := deps.Metadata.Get(ctx, userID, mailID)
	if err != nil {
		return metadata.EmptyEmailMetadata(userID, mailID)
	}
	meta.UserID = userID
	meta.ID = mailID
	if meta.Keywords == nil {
		meta.Keywords = make(map[string]bool)
	}
	return meta
}

// ReceivedAtFromMetadata returns the server-recorded receive time, falling
// back to the message Date for records created before receivedAt existed.
func ReceivedAtFromMetadata(meta metadata.EmailMetadata, fallback time.Time) time.Time {
	if meta.ReceivedAt != "" {
		if received, err := time.Parse(time.RFC3339, meta.ReceivedAt); err == nil {
			return received.UTC()
		}
	}
	return fallback
}

// QueryKey derives a short, stable signature from a query's filter and sort
// arguments. Query state tokens are scoped to this key so two queries with
// different result sets can maintain independent counters.
func QueryKey(filter, sort any) (string, error) {
	if filter == nil {
		filter = map[string]any{}
	}
	if sort == nil {
		sort = []any{}
	}
	data, err := json.Marshal(struct {
		Filter any `json:"filter"`
		Sort   any `json:"sort"`
	}{Filter: filter, Sort: sort})
	if err != nil {
		return "", fmt.Errorf("encode query key: %w", err)
	}
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:12]), nil
}

// PageBounds clamps untrusted JMAP pagination arguments and returns the
// half-open slice bounds for a result set of the given size.
func PageBounds(total int, position, limit *int) (int, int) {
	start := 0
	if position != nil {
		start = *position
	}
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}

	end := total
	if limit != nil && *limit < end-start {
		end = start + *limit
	}
	if end < start {
		end = start
	}
	return start, end
}

// NotImplemented produces the standard error for methods that are advertised
// but not yet backed by an implementation.
func NotImplemented(method string) (json.RawMessage, error) {
	return nil, fmt.Errorf("%s not implemented", method)
}
