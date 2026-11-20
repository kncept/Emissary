// Package mailbox implements the Mailbox methods from RFC 8621 section 2.
package mailbox

import (
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet          = "Mailbox/get"
	MethodSet          = "Mailbox/set"
	MethodChanges      = "Mailbox/changes"
	MethodQuery        = "Mailbox/query"
	MethodQueryChanges = "Mailbox/queryChanges"
)

// Methods returns the method names implemented by this package.
func Methods() []string {
	return []string{MethodGet, MethodSet, MethodChanges, MethodQuery, MethodQueryChanges}
}

// MethodHandler is the contract the dispatcher expects from Mailbox handlers.
type MethodHandler interface {
	MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Mailbox methods on top of the shared JMAP services.
type Handler struct{ deps *base.Deps }

// New creates a Mailbox method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// Mailbox represents a JMAP Mailbox object.
type Mailbox struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ParentID      *string       `json:"parentId,omitempty"`
	Role          *string       `json:"role,omitempty"`
	SortOrder     int           `json:"sortOrder"`
	TotalEmails   int           `json:"totalEmails"`
	UnreadEmails  int           `json:"unreadEmails"`
	TotalThreads  int           `json:"totalThreads"`
	UnreadThreads int           `json:"unreadThreads"`
	MyRights      MailboxRights `json:"myRights"`
	IsSubscribed  bool          `json:"isSubscribed"`
}

// MailboxRights describes the caller's rights on a Mailbox.
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

var _ MethodHandler = (*Handler)(nil)
