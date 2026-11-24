// Package metadata stores the JMAP-mappable state of an email that is not
// derived from the message itself: its keywords, the server-owned receive
// time, the mailbox currently holding it and the message subject. JMAP
// represents read, flagged, answered, draft and spam state as keywords rather
// than as separate boolean fields (RFC 8621 section 4.1.1), so Emissary stores
// them that way too.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// System keywords supported by Emissary. The first four are defined by
// RFC 8621; the rest come from the IANA "IMAP and JMAP Keywords" registry.
const (
	KeywordDraft     = "$draft"
	KeywordSeen      = "$seen"
	KeywordFlagged   = "$flagged"
	KeywordAnswered  = "$answered"
	KeywordForwarded = "$forwarded"
	KeywordJunk      = "$junk"
	KeywordNotJunk   = "$notjunk"
	KeywordPhishing  = "$phishing"
)

// EmailMetadata is the per-email state tracked by a MetadataStore. It holds
// server-owned state plus a copy of two message properties:
//
//   - ReceivedAt is the time Emissary received the message. It is stamped when
//     the record is created and is never assignable or editable by JMAP
//     clients. It uses the same RFC 3339 UTC string shape as JMAP's
//     Email/receivedAt property.
//   - Keywords use the JMAP "String[Boolean]" shape: every present key has the
//     value true.
//   - Mailbox is the id of the mailbox currently holding the message. It is
//     set when the record is created and rewritten whenever the message moves
//     between mailboxes.
//   - Subject is the decoded RFC 5322 subject line of the message. It is
//     captured at creation; clients cannot edit it through metadata.
//   - Version is used for optimistic locking to prevent concurrent updates from
//     overwriting each other. It is incremented on every successful save.
//
// Records are keyed per user and per mail message: UserID is the owning user,
// ID is the mail id.
type EmailMetadata struct {
	UserID     string          `json:"userId,omitempty" dynamodbav:"userId"`
	ID         string          `json:"id" dynamodbav:"id"`
	ReceivedAt string          `json:"receivedAt,omitempty" dynamodbav:"receivedAt,omitempty"`
	Mailbox    string          `json:"mailbox,omitempty" dynamodbav:"mailbox,omitempty"`
	Subject    string          `json:"subject,omitempty" dynamodbav:"subject,omitempty"`
	Keywords   map[string]bool `json:"keywords,omitempty" dynamodbav:"keywords,omitempty"`
	Version    int             `json:"version,omitempty" dynamodbav:"version,omitempty"`
}

// FormatReceivedAt formats t as an RFC 3339 UTC timestamp for the metadata
// receivedAt field (and JMAP Email/receivedAt).
func FormatReceivedAt(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// EmptyEmailMetadata returns metadata for a user/mail pair with no stored
// record. It is deliberately distinct from NewEmailMetadata: a missing record
// must not fabricate a receive time, so callers can fall back to the message
// Date.
func EmptyEmailMetadata(userID, mailID string) EmailMetadata {
	return EmailMetadata{UserID: userID, ID: mailID, Keywords: make(map[string]bool)}
}

// NormalizeKeyword validates and lowercases a JMAP keyword. JMAP keywords are
// case-insensitive 1-255 character strings from the printable ASCII range,
// excluding IMAP atom specials; servers must return them in lowercase.
func NormalizeKeyword(keyword string) (string, bool) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if len(keyword) < 1 || len(keyword) > 255 {
		return "", false
	}
	for _, r := range keyword {
		if r < 0x21 || r > 0x7e {
			return "", false
		}
		switch r {
		case '(', ')', '{', ']', '%', '*', '"', '\\':
			return "", false
		}
	}
	return keyword, true
}

// NewEmailMetadata creates a metadata record for a newly stored email with the
// given keywords. It stamps receivedAt with the current UTC time; callers must
// not use it as a fallback for missing records (use EmptyEmailMetadata).
// Invalid keyword names are ignored. Callers with a stored message should use
// NewEmailMetadataForMessage so mailbox and subject are populated too.
func NewEmailMetadata(userID, mailID string, keywords ...string) EmailMetadata {
	m := EmailMetadata{
		UserID:     userID,
		ID:         mailID,
		ReceivedAt: FormatReceivedAt(time.Now()),
		Keywords:   make(map[string]bool),
	}
	for _, keyword := range keywords {
		if normalized, ok := NormalizeKeyword(keyword); ok {
			m.Keywords[normalized] = true
		}
	}
	return m
}

// NewEmailMetadataForMessage creates a metadata record for a newly stored
// message, recording the mailbox that currently holds it and its (already
// decoded) subject alongside the server-owned receive time. Delivery, JMAP
// create/import/copy and the Sent copy all use it so every new record carries
// the same fields. Invalid keyword names are ignored.
func NewEmailMetadataForMessage(userID, mailID, mailboxID, subject string, keywords ...string) EmailMetadata {
	m := NewEmailMetadata(userID, mailID, keywords...)
	m.Mailbox = strings.ToLower(strings.TrimSpace(mailboxID))
	m.Subject = subject
	return m
}

// NewEmailMetadataFromMap creates a metadata record from a JMAP keyword map.
func NewEmailMetadataFromMap(userID, mailID string, keywords map[string]bool) EmailMetadata {
	m := NewEmailMetadata(userID, mailID)
	for keyword, enabled := range keywords {
		if !enabled {
			continue
		}
		if normalized, ok := NormalizeKeyword(keyword); ok {
			m.Keywords[normalized] = true
		}
	}
	return m
}

// HasKeyword reports whether the given keyword is set.
func (m EmailMetadata) HasKeyword(keyword string) bool {
	normalized, ok := NormalizeKeyword(keyword)
	if !ok {
		return false
	}
	return m.Keywords[normalized]
}

// IsRead reports whether the $seen keyword is set.
func (m EmailMetadata) IsRead() bool { return m.HasKeyword(KeywordSeen) }

// IsFlagged reports whether the $flagged keyword is set.
func (m EmailMetadata) IsFlagged() bool { return m.HasKeyword(KeywordFlagged) }

// Clone returns an independent copy, so callers can patch keywords without
// mutating a value held by a store implementation.
func (m EmailMetadata) Clone() EmailMetadata {
	copy := EmailMetadata{UserID: m.UserID, ID: m.ID, ReceivedAt: m.ReceivedAt, Mailbox: m.Mailbox, Subject: m.Subject, Version: m.Version, Keywords: make(map[string]bool, len(m.Keywords))}
	for keyword, enabled := range m.Keywords {
		if enabled {
			copy.Keywords[keyword] = true
		}
	}
	return copy
}

// Normalize returns a read result with identity fields derived from the lookup
// keys. Backends differ in whether an on-disk record repeats the user and mail
// ids; callers always see the id they asked for, with an independent keyword
// map and no false-valued keywords. An empty mailID keeps a stored id.
func (m EmailMetadata) Normalize(userID, mailID string) EmailMetadata {
	normalized := m.Clone()
	if normalized.UserID == "" {
		normalized.UserID = userID
	}
	if mailID != "" {
		normalized.ID = mailID
	}
	return normalized
}

// sortMetadata orders records by mail id, the single ordering every
// MetadataStore.List implementation returns.
func sortMetadata(entries []EmailMetadata) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}

// ErrVersionConflict is returned when a save fails due to a concurrent
// modification. Callers should re-read the metadata and retry the operation.
var ErrVersionConflict = errors.New("metadata version conflict: concurrent modification")

// MetadataStore stores JMAP-mappable per-email state. Records are keyed by
// user id and mail id. Get must return "not found" as an EmptyEmailMetadata
// with no error: JMAP responses include messages that predate the metadata
// store, and those must still be returned without a fabricated receive time.
type MetadataStore interface {
	// Name returns the name of the metadata store implementation.
	Name() string

	// Get retrieves the metadata for the given user and mail ID.
	Get(ctx context.Context, userID, mailID string) (EmailMetadata, error)

	// Save stores the complete metadata for the given record.
	Save(ctx context.Context, metadata EmailMetadata) error

	// Delete discards all state for the given user/mail pair. Deleting an
	// already absent record is not an error.
	Delete(ctx context.Context, userID, mailID string) error

	// List returns all stored metadata entries for the given user.
	List(ctx context.Context, userID string) ([]EmailMetadata, error)
}

// NewStore selects a MetadataStore implementation by name. tableName is
// expected to be fully resolved by the caller. The type must be explicit:
// memory is a supported implementation, not a fallback for a missing or
// unrecognised selector (set METADATA=memory to opt into it).
func NewStore(typeName, filePath, tableName, region string) (MetadataStore, error) {
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "memory":
		return NewMemoryMetadata(), nil
	case "file":
		return NewFileMetadata(filePath)
	case "ddb", "dynamodb":
		return NewDdbMetadata(tableName, region)
	default:
		return nil, fmt.Errorf("unknown metadata type %q (set METADATA explicitly)", typeName)
	}
}
