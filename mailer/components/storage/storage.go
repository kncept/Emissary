package storage

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/email"
)

// ErrNotFound is returned when an email or mailbox does not exist in the
// backing store.
var ErrNotFound = errors.New("not found")

// ErrMailboxExists is returned when creating a mailbox whose id already exists.
var ErrMailboxExists = errors.New("mailbox already exists")

// ErrMailboxNotEmpty is returned when destroying a mailbox that still contains
// messages or child mailboxes and the operation requires it to be empty.
var ErrMailboxNotEmpty = errors.New("mailbox not empty")

// Mailbox is a persisted JMAP mailbox record stored alongside the mailbox
// folder. It is the source of truth for mailbox properties; message counts and
// rights may be derived by higher layers.
type Mailbox struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ParentID     *string `json:"parentId,omitempty"`
	Role         *string `json:"role,omitempty"`
	SortOrder    int     `json:"sortOrder"`
	IsSubscribed bool    `json:"isSubscribed"`
}

// DefaultMailboxes returns the role mailboxes that must exist for an account.
// Inbox is always present; the others provide standard JMAP roles for
// move/trash/archive/drafts/sent behavior.
func DefaultMailboxes() []Mailbox {
	role := func(r string) *string { return &r }
	return []Mailbox{
		{ID: "inbox", Name: "Inbox", Role: role("inbox"), SortOrder: 0, IsSubscribed: true},
		{ID: "drafts", Name: "Drafts", Role: role("drafts"), SortOrder: 1, IsSubscribed: true},
		{ID: "sent", Name: "Sent", Role: role("sent"), SortOrder: 2, IsSubscribed: true},
		{ID: "archive", Name: "Archive", Role: role("archive"), SortOrder: 3, IsSubscribed: true},
		{ID: "junk", Name: "Junk", Role: role("junk"), SortOrder: 4, IsSubscribed: true},
		{ID: "trash", Name: "Trash", Role: role("trash"), SortOrder: 5, IsSubscribed: true},
	}
}

// NormalizeMailboxID canonicalizes a mailbox identifier used in storage paths.
func NormalizeMailboxID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// NormalizeEmailID canonicalizes an email identifier into the stable, opaque,
// URL-safe id exposed by the Storage API and to JMAP clients. It is the email
// counterpart of NormalizeMailboxID: callers may pass an opaque id, a legacy
// delivery token or a full delivered object key and always receive the
// canonical opaque id. Physical object keys remain an internal storage detail.
func NormalizeEmailID(id string) (string, error) {
	return email.NormalizeEmailID(id)
}

// SortMailboxes orders mailboxes by sortOrder then name then id for stable
// responses.
func SortMailboxes(mailboxes []Mailbox) {
	sort.Slice(mailboxes, func(i, j int) bool {
		if mailboxes[i].SortOrder != mailboxes[j].SortOrder {
			return mailboxes[i].SortOrder < mailboxes[j].SortOrder
		}
		ni := strings.ToLower(mailboxes[i].Name)
		nj := strings.ToLower(mailboxes[j].Name)
		if ni != nj {
			return ni < nj
		}
		return mailboxes[i].ID < mailboxes[j].ID
	})
}

// DraftStorage holds JMAP drafts in a per-session scratch area that survives
// across Lambda invocations (S3 prefix _storage/<sid>/ in production). Draft
// ids are relative to the session folder.
type DraftStorage interface {
	// PutDraft stores a draft for a session under creationID and returns its
	// session-relative id.
	PutDraft(ctx context.Context, sessionID, creationID string, message *email.UserEmail) (string, error)
	// GetDraft loads a draft by its session-relative id. It returns
	// ErrNotFound when the draft does not exist.
	GetDraft(ctx context.Context, sessionID, id string) (*email.UserEmail, error)
	// DeleteDraft removes a draft. It is idempotent: a missing draft is not
	// an error.
	DeleteDraft(ctx context.Context, sessionID, id string) error
}

// SessionStateStore persists small per-session JSON state, such as the JMAP
// creation-id map, in the same durable scratch area as drafts.
type SessionStateStore interface {
	LoadCreatedIDs(ctx context.Context, sessionID string) (map[string]string, error)
	SaveCreatedIDs(ctx context.Context, sessionID string, ids map[string]string) error
}

// Storage is the mail source. New providers can be added by implementing
// this interface and selecting them with STORAGE.
type Storage interface {
	Name() string

	// Mailboxes manages persisted mailbox records/folders for an account. The
	// inbox mailbox must always exist for an account that stores mail.
	ListMailboxes(ctx context.Context, accountID string) ([]Mailbox, error)
	GetMailbox(ctx context.Context, accountID, mailboxID string) (Mailbox, error)
	CreateMailbox(ctx context.Context, accountID string, mailbox Mailbox) (Mailbox, error)
	UpdateMailbox(ctx context.Context, accountID string, mailbox Mailbox) (Mailbox, error)
	DeleteMailbox(ctx context.Context, accountID, mailboxID string) error

	// ListEmails lists messages in a JMAP account's mailbox. accountID is the
	// account id (the user's email address) and mailboxID is the JMAP mailbox
	// id (for example "inbox"). An empty mailboxID lists every mailbox
	// belonging to the account. Implementations must never read outside the
	// account's own storage.
	ListEmails(ctx context.Context, accountID, mailboxID string) ([]*email.ListedEmail, error)
	// GetEmail returns a message by its stable, opaque id within the account,
	// together with the ref describing the mailbox that currently owns it.
	// Legacy delivery tokens and full object keys are accepted for backwards
	// compatibility; the returned ref always carries the canonical opaque id.
	GetEmail(ctx context.Context, accountID, id string) (*email.ListedEmail, error)
	// GetEmailRaw returns the original RFC 5322 message bytes for a message,
	// without parsing them. It returns ErrNotFound when no such message exists.
	// Stores that only keep a parsed representation return a best-effort
	// rendering.
	GetEmailRaw(ctx context.Context, accountID, id string) ([]byte, error)
	// AddEmail stores a new message in the specified mailbox (default inbox)
	// and returns the stable, opaque message id. The caller uses the returned id to
	// create its metadata. raw is the original RFC 5322 bytes read from the
	// wire (for example the SES staging object). When non-nil it must be
	// stored verbatim so that GetEmailRaw returns every header, MIME part and
	// transfer encoding unchanged. A nil raw asks the store to render a message
	// from the parsed fields, which is the case for messages created through
	// JMAP.
	AddEmail(ctx context.Context, accountID, mailboxID string, message *email.UserEmail, raw []byte) (string, error)
	// MoveEmail moves an existing message to destMailboxID and returns the
	// updated listing (with the same stable id). It returns ErrNotFound when
	// the message or destination mailbox does not exist.
	MoveEmail(ctx context.Context, accountID, id, destMailboxID string) (*email.ListedEmail, error)
	// DeleteEmail removes the message with the given stable id. It returns
	// ErrNotFound when no such message exists.
	DeleteEmail(ctx context.Context, accountID, id string) error

	DraftStorage
	SessionStateStore
	JMAPStateStore
}
