package base

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	mailemail "github.com/kncept/Emissary/mailer/email"
)

// MailboxRights is the RFC 8621 section 2.1 rights object. It is shared by
// Mailbox/get and the rights checks in Email/set so the advertised rights and
// the enforced rights cannot drift apart.
type MailboxRights struct {
	MayReadItems   bool `json:"mayReadItems"`
	MayAddItems    bool `json:"mayAddItems"`
	MayRemoveItems bool `json:"mayRemoveItems"`
	MaySetSeen     bool `json:"maySetSeen"`
	MaySetKeywords bool `json:"maySetKeywords"`
	MayCreateChild bool `json:"mayCreateChild"`
	MayRename      bool `json:"mayRename"`
	MayDelete      bool `json:"mayDelete"`
	MaySubmit      bool `json:"maySubmit"`
}

// RightsForRole derives the rights of a mailbox from its role. Custom
// mailboxes get the default full rights; system roles lose the operations that
// would remove the mailbox itself or submit from a non-submitting folder.
func RightsForRole(role string) MailboxRights {
	r := MailboxRights{
		MayReadItems:   true,
		MayAddItems:    true,
		MayRemoveItems: true,
		MaySetSeen:     true,
		MaySetKeywords: true,
		MayCreateChild: false,
		MayRename:      true,
		MayDelete:      true,
		MaySubmit:      true,
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "inbox", "drafts":
		r.MayRename, r.MayDelete = false, false
	case "sent":
		r.MayRename, r.MayDelete, r.MaySubmit = false, false, false
	case "trash", "junk":
		r.MayRename, r.MayDelete, r.MaySubmit = false, false, false
	case "archive":
		r.MayRename, r.MayDelete = false, false
	}
	return r
}

// ThreadID derives a stable thread identifier from the stored email's
// immutable identity and RFC 5322 threading headers. Emissary keeps 1:1
// email/thread grouping (documented in the OpenAPI spec), so the email id is
// part of the digest to keep threads distinct; the returned id is nevertheless
// a stable digest rather than an alias of the email id. When threading headers
// are absent, canonical content fields seed the digest instead.
func ThreadID(emailID string, msg *mailemail.UserEmail) string {
	parts := []string{"thread", NormalizeMessageID(emailID)}
	threadHeaders := 0
	if msg != nil {
		for _, value := range msg.References {
			if normalized := NormalizeMessageID(value); normalized != "" {
				parts = append(parts, "references", normalized)
				threadHeaders++
			}
		}
		for _, value := range msg.InReplyTo {
			if normalized := NormalizeMessageID(value); normalized != "" {
				parts = append(parts, "in-reply-to", normalized)
				threadHeaders++
			}
		}
		if normalized := NormalizeMessageID(msg.MessageID); normalized != "" {
			parts = append(parts, "message-id", normalized)
			threadHeaders++
		}
		if threadHeaders == 0 {
			parts = append(parts,
				"from", strings.ToLower(strings.TrimSpace(msg.From.Address)),
				"subject", strings.ToLower(strings.TrimSpace(msg.Subject)),
				"sentAt", msg.Date.UTC().Format(time.RFC3339Nano),
				"text", strings.TrimSpace(msg.Text),
				"html", strings.TrimSpace(msg.HTML),
			)
			for _, address := range msg.To {
				parts = append(parts, "to", strings.ToLower(strings.TrimSpace(address.Address)))
			}
			for _, address := range msg.Cc {
				parts = append(parts, "cc", strings.ToLower(strings.TrimSpace(address.Address)))
			}
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// NormalizeMessageID strips the angle brackets and surrounding whitespace
// from a Message-ID value before it participates in a thread key.
func NormalizeMessageID(value string) string {
	return strings.Trim(strings.TrimSpace(value), "<> \t\r\n")
}

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
