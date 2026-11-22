// Package email implements the Email methods from RFC 8621 section 4.
package email

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

const (
	MethodGet          = "Email/get"
	MethodSet          = "Email/set"
	MethodChanges      = "Email/changes"
	MethodQuery        = "Email/query"
	MethodQueryChanges = "Email/queryChanges"
	MethodCopy         = "Email/copy"
	MethodImport       = "Email/import"
	MethodParse        = "Email/parse"
)

// Methods returns the method names implemented by this package.
func Methods() []string {
	return []string{
		MethodGet, MethodSet, MethodChanges, MethodQuery, MethodQueryChanges,
		MethodCopy, MethodImport, MethodParse,
	}
}

// MethodHandler is the contract the dispatcher expects from Email handlers.
type MethodHandler interface {
	EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Email methods on top of the shared JMAP services.
type Handler struct{ deps *base.Deps }

// New creates an Email method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// EmailFilter represents the JMAP Email filter conditions supported by the
// account-wide query kernel. Compound and header conditions are added by the
// shared filter parser in helpers.go; unknown conditions are rejected rather
// than silently matching everything.
type EmailFilter struct {
	// Operator and Conditions form an RFC 8620 section 5.5 FilterOperator.
	// A leaf FilterCondition leaves both empty.
	Operator   string         `json:"operator,omitempty"`
	Conditions []*EmailFilter `json:"conditions,omitempty"`

	InMailbox          *string  `json:"inMailbox,omitempty"`
	InMailboxOtherThan []string `json:"inMailboxOtherThan,omitempty"`
	Before             *string  `json:"before,omitempty"`
	After              *string  `json:"after,omitempty"`
	MinSize            *int     `json:"minSize,omitempty"`
	MaxSize            *int     `json:"maxSize,omitempty"`
	HasKeyword         *string  `json:"hasKeyword,omitempty"`
	NotKeyword         *string  `json:"notKeyword,omitempty"`
	// Thread-scoped keyword conditions are evaluated against the grouped
	// Thread, not just the candidate email (RFC 8621 section 4.4.1).
	AllInThreadHaveKeyword  *string `json:"allInThreadHaveKeyword,omitempty"`
	SomeInThreadHaveKeyword *string `json:"someInThreadHaveKeyword,omitempty"`
	NoneInThreadHaveKeyword *string `json:"noneInThreadHaveKeyword,omitempty"`
	ThreadID                *string `json:"threadId,omitempty"`
	HasAttachment           *bool   `json:"hasAttachment,omitempty"`
	Text                    *string `json:"text,omitempty"`
	From                    *string `json:"from,omitempty"`
	To                      *string `json:"to,omitempty"`
	Cc                      *string `json:"cc,omitempty"`
	Bcc                     *string `json:"bcc,omitempty"`
	Subject                 *string `json:"subject,omitempty"`
	Body                    *string `json:"body,omitempty"`
	// Header is the RFC 8621 header condition: [name] or [name, value].
	Header []string `json:"header,omitempty"`

	// before/after are parsed once by parseEmailFilter so a malformed date is
	// an invalidArguments error rather than a silently skipped condition.
	beforeTime *time.Time
	afterTime  *time.Time
}

// supportedEmailFilterFields is the allow-list used to reject filter
// conditions that are not declared by RFC 8621 section 4.4.1. operator and
// conditions are valid only in a FilterOperator, enforced by the parser.
var supportedEmailFilterFields = map[string]bool{
	"operator":                true,
	"conditions":              true,
	"inMailbox":               true,
	"inMailboxOtherThan":      true,
	"before":                  true,
	"after":                   true,
	"minSize":                 true,
	"maxSize":                 true,
	"allInThreadHaveKeyword":  true,
	"someInThreadHaveKeyword": true,
	"noneInThreadHaveKeyword": true,
	"hasKeyword":              true,
	"notKeyword":              true,
	"threadId":                true,
	"hasAttachment":           true,
	"text":                    true,
	"from":                    true,
	"to":                      true,
	"cc":                      true,
	"bcc":                     true,
	"subject":                 true,
	"body":                    true,
	"header":                  true,
}

// JMAPEmail represents a JMAP Email object. RFC 8621 section 1.1 requires a
// server that advertises mail to return every property (null where the RFC
// permits it), so the standard properties deliberately have no omitempty.
type JMAPEmail struct {
	ID            string                    `json:"id"`
	BlobID        string                    `json:"blobId"`
	ThreadID      string                    `json:"threadId"`
	MailboxIDs    map[string]bool           `json:"mailboxIds"`
	Keywords      map[string]bool           `json:"keywords"`
	Size          int                       `json:"size"`
	ReceivedAt    string                    `json:"receivedAt"`
	MessageID     []string                  `json:"messageId"`
	InReplyTo     []string                  `json:"inReplyTo"`
	References    []string                  `json:"references"`
	Sender        []model.EmailAddress      `json:"sender"`
	From          []model.EmailAddress      `json:"from"`
	To            []model.EmailAddress      `json:"to"`
	Cc            []model.EmailAddress      `json:"cc"`
	Bcc           []model.EmailAddress      `json:"bcc"`
	ReplyTo       []model.EmailAddress      `json:"replyTo"`
	Subject       string                    `json:"subject"`
	SentAt        *string                   `json:"sentAt"`
	HasAttachment bool                      `json:"hasAttachment"`
	Preview       string                    `json:"preview"`
	BodyStructure *EmailBodyPart            `json:"bodyStructure"`
	BodyValues    map[string]EmailBodyValue `json:"bodyValues"`
	TextBody      []EmailBodyPart           `json:"textBody"`
	HTMLBody      []EmailBodyPart           `json:"htmlBody"`
	Attachments   []EmailBodyPart           `json:"attachments"`
	Headers       []EmailHeader             `json:"headers"`

	// wire is the parsed RFC 5322 source of the derived properties. It is not
	// serialised; only explicitly selected JMAP properties are returned.
	wire *wireView
}

// EmailBodyPart represents a JMAP Email body part. RFC 8621 section 4.1.4 lists
// every field as present-and-nullable, so none use omitempty.
type EmailBodyPart struct {
	PartID      *string         `json:"partId"`
	BlobID      *string         `json:"blobId"`
	Type        string          `json:"type"`
	Size        int             `json:"size"`
	Name        *string         `json:"name"`
	Charset     *string         `json:"charset"`
	Disposition *string         `json:"disposition"`
	CID         *string         `json:"cid"`
	Language    []string        `json:"language"`
	Location    *string         `json:"location"`
	SubParts    []EmailBodyPart `json:"subParts"`
	Headers     []EmailHeader   `json:"headers"`
}

// EmailBodyValue represents the fetched value of a JMAP body part.
type EmailBodyValue struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem,omitempty"`
	IsTruncated       bool   `json:"isTruncated,omitempty"`
}

// EmailHeader represents a JMAP Email header.
type EmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var _ MethodHandler = (*Handler)(nil)
