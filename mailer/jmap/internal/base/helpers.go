package base

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kncept/Emissary/mailer/metadata"
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

// QueryChangesResponse renames the generic /changes state fields to the
// *QueryState names required by RFC 8620 section 5.5.
func QueryChangesResponse(result map[string]any) map[string]any {
	result["oldQueryState"] = result["oldState"]
	result["newQueryState"] = result["newState"]
	delete(result, "oldState")
	delete(result, "newState")
	return result
}

// NotImplemented produces the standard error for methods that are advertised
// but not yet backed by an implementation.
func NotImplemented(method string) (json.RawMessage, error) {
	return nil, fmt.Errorf("%s not implemented", method)
}
