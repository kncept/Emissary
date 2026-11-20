// Package email implements the Email methods from RFC 8621 section 4.
package email

import (
	"context"
	"encoding/json"

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

// EmailFilter represents the JMAP Email filter structure.
type EmailFilter struct {
	InMailbox               *string  `json:"inMailbox,omitempty"`
	InMailboxOtherThan      []string `json:"inMailboxOtherThan,omitempty"`
	Before                  *string  `json:"before,omitempty"`
	After                   *string  `json:"after,omitempty"`
	MinSize                 *int     `json:"minSize,omitempty"`
	MaxSize                 *int     `json:"maxSize,omitempty"`
	AllInThreadHaveKeyword  *string  `json:"allInThreadHaveKeyword,omitempty"`
	SomeInThreadHaveKeyword *string  `json:"someInThreadHaveKeyword,omitempty"`
	NoneInThreadHaveKeyword *string  `json:"noneInThreadHaveKeyword,omitempty"`
	HasKeyword              *string  `json:"hasKeyword,omitempty"`
	NotKeyword              *string  `json:"notKeyword,omitempty"`
	HasAttachment           *bool    `json:"hasAttachment,omitempty"`
	Text                    *string  `json:"text,omitempty"`
	From                    *string  `json:"from,omitempty"`
	To                      *string  `json:"to,omitempty"`
	Cc                      *string  `json:"cc,omitempty"`
	Bcc                     *string  `json:"bcc,omitempty"`
	Subject                 *string  `json:"subject,omitempty"`
	Body                    *string  `json:"body,omitempty"`
	Header                  []string `json:"header,omitempty"`
}

// JMAPEmail represents a JMAP Email object.
type JMAPEmail struct {
	ID            string                    `json:"id"`
	BlobID        string                    `json:"blobId"`
	ThreadID      string                    `json:"threadId"`
	MailboxIDs    map[string]bool           `json:"mailboxIds"`
	Keywords      map[string]bool           `json:"keywords"`
	Size          int                       `json:"size"`
	ReceivedAt    string                    `json:"receivedAt"`
	MessageID     []string                  `json:"messageId,omitempty"`
	InReplyTo     []string                  `json:"inReplyTo,omitempty"`
	References    []string                  `json:"references,omitempty"`
	Sender        []model.EmailAddress      `json:"sender,omitempty"`
	From          []model.EmailAddress      `json:"from,omitempty"`
	To            []model.EmailAddress      `json:"to,omitempty"`
	Cc            []model.EmailAddress      `json:"cc,omitempty"`
	Bcc           []model.EmailAddress      `json:"bcc,omitempty"`
	ReplyTo       []model.EmailAddress      `json:"replyTo,omitempty"`
	Subject       string                    `json:"subject,omitempty"`
	SentAt        *string                   `json:"sentAt,omitempty"`
	HasAttachment bool                      `json:"hasAttachment"`
	Preview       string                    `json:"preview"`
	BodyStructure *EmailBodyPart            `json:"bodyStructure,omitempty"`
	BodyValues    map[string]EmailBodyValue `json:"bodyValues,omitempty"`
	TextBody      []EmailBodyPart           `json:"textBody,omitempty"`
	HTMLBody      []EmailBodyPart           `json:"htmlBody,omitempty"`
	Attachments   []EmailBodyPart           `json:"attachments,omitempty"`
	Headers       []EmailHeader             `json:"headers,omitempty"`
}

// EmailBodyPart represents a JMAP Email body part.
type EmailBodyPart struct {
	PartID      *string         `json:"partId,omitempty"`
	BlobID      *string         `json:"blobId,omitempty"`
	Type        string          `json:"type"`
	Size        int             `json:"size"`
	Name        *string         `json:"name,omitempty"`
	Charset     *string         `json:"charset,omitempty"`
	Disposition *string         `json:"disposition,omitempty"`
	CID         *string         `json:"cid,omitempty"`
	Language    []string        `json:"language,omitempty"`
	Location    *string         `json:"location,omitempty"`
	SubParts    []EmailBodyPart `json:"subParts,omitempty"`
	Headers     []EmailHeader   `json:"headers,omitempty"`
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
