package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

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

// FromValidator checks that a From address may be used for sending. Callers
// that have no identity source (unit tests, local development) may omit it.
type FromValidator interface {
	Validate(ctx context.Context, from string) error
}

// Handler implements MethodHandler by bridging to the existing storage,
// metadata, and user management layers.
type Handler struct {
	storage       storage.Storage
	metadata      metadata.MetadataStore
	userMgr       usermanager.UserManager
	sender        sender.Sender
	fromValidator FromValidator
}

// NewHandler creates a new JMAP method handler. An optional FromValidator
// restricts submissions to the installation's verified email identities.
func NewHandler(store storage.Storage, meta metadata.MetadataStore, userMgr usermanager.UserManager, mailSender sender.Sender, validators ...FromValidator) *Handler {
	h := &Handler{
		storage:  store,
		metadata: meta,
		userMgr:  userMgr,
		sender:   mailSender,
	}
	for _, validator := range validators {
		if validator != nil {
			h.fromValidator = validator
			break
		}
	}
	return h
}

// Ensure Handler implements MethodHandler
var _ MethodHandler = (*Handler)(nil)

// ============================================================================
// Core methods
// ============================================================================

func (h *Handler) CoreEcho(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// ============================================================================
// Mailbox methods
// ============================================================================

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

func (h *Handler) MailboxGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	mailboxes := h.getMailboxes(ctx, accountID)
	var list []Mailbox
	var notFound []string

	if len(req.IDs) > 0 {
		mailboxMap := make(map[string]Mailbox)
		for _, mb := range mailboxes {
			mailboxMap[mb.ID] = mb
		}
		for _, id := range req.IDs {
			if mb, ok := mailboxMap[id]; ok {
				list = append(list, mb)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = mailboxes
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.getState(ctx),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	// For now, mailboxes are static - return error for create/update/destroy
	var req struct {
		AccountID string                 `json:"accountId"`
		Create    map[string]interface{} `json:"create,omitempty"`
		Update    map[string]interface{} `json:"update,omitempty"`
		Destroy   []string               `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// All operations fail as mailboxes are static
	for id := range req.Create {
		notCreated[id] = map[string]string{"type": "forbidden", "description": "Cannot create mailboxes"}
	}
	for id := range req.Update {
		notUpdated[id] = map[string]string{"type": "forbidden", "description": "Cannot update mailboxes"}
	}
	for _, id := range req.Destroy {
		notDestroyed[id] = map[string]string{"type": "forbidden", "description": "Cannot destroy mailboxes"}
	}

	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     h.getState(ctx),
		"newState":     h.getState(ctx),
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// No changes since mailboxes are static
	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldState":       req.SinceState,
		"newState":       h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string `json:"accountId"`
		Filter    *struct {
			ParentID     *string `json:"parentId,omitempty"`
			Name         *string `json:"name,omitempty"`
			Role         *string `json:"role,omitempty"`
			HasAnyRole   *bool   `json:"hasAnyRole,omitempty"`
			IsSubscribed *bool   `json:"isSubscribed,omitempty"`
		} `json:"filter,omitempty"`
		Sort         []interface{} `json:"sort,omitempty"`
		Position     *int          `json:"position,omitempty"`
		Anchor       *string       `json:"anchor,omitempty"`
		AnchorOffset *int          `json:"anchorOffset,omitempty"`
		Limit        *int          `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	mailboxes := h.getMailboxes(ctx, accountID)

	// Apply filter if provided
	if req.Filter != nil {
		filtered := make([]Mailbox, 0)
		for _, mb := range mailboxes {
			match := true
			if req.Filter.ParentID != nil && (mb.ParentID == nil || *mb.ParentID != *req.Filter.ParentID) {
				match = false
			}
			if req.Filter.Name != nil && !strings.Contains(strings.ToLower(mb.Name), strings.ToLower(*req.Filter.Name)) {
				match = false
			}
			if req.Filter.Role != nil && (mb.Role == nil || *mb.Role != *req.Filter.Role) {
				match = false
			}
			if req.Filter.HasAnyRole != nil && *req.Filter.HasAnyRole && (mb.Role == nil || *mb.Role == "") {
				match = false
			}
			if req.Filter.IsSubscribed != nil && mb.IsSubscribed != *req.Filter.IsSubscribed {
				match = false
			}
			if match {
				filtered = append(filtered, mb)
			}
		}
		mailboxes = filtered
	}

	// Apply position/limit for pagination. Untrusted values are clamped so a
	// negative position or limit cannot produce an invalid slice.
	position := 0
	if req.Position != nil {
		position = *req.Position
	}
	if position < 0 {
		position = 0
	}
	if position > len(mailboxes) {
		position = len(mailboxes)
	}

	limit := len(mailboxes) - position
	if req.Limit != nil && *req.Limit < limit {
		limit = *req.Limit
	}
	if limit < 0 {
		limit = 0
	}

	result := mailboxes[position : position+limit]
	ids := make([]string, len(result))
	for i, mb := range result {
		ids[i] = mb.ID
	}

	resp := map[string]interface{}{
		"accountId":  accountID,
		"queryState": h.getState(ctx),
		"ids":        ids,
		"total":      len(mailboxes),
	}
	return json.Marshal(resp)
}

func (h *Handler) MailboxQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string  `json:"accountId"`
		SinceQueryState string  `json:"sinceQueryState"`
		MaxChanges      *int    `json:"maxChanges,omitempty"`
		UpToID          *string `json:"upToId,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldQueryState":  req.SinceQueryState,
		"newQueryState":  h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

// ============================================================================
// Thread methods
// ============================================================================

type Thread struct {
	ID       string   `json:"id"`
	EmailIDs []string `json:"emailIds"`
}

func (h *Handler) ThreadGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	threads := h.getThreads(ctx, accountID)
	var list []Thread
	var notFound []string

	if len(req.IDs) > 0 {
		threadMap := make(map[string]Thread)
		for _, t := range threads {
			threadMap[t.ID] = t
		}
		for _, id := range req.IDs {
			if t, ok := threadMap[id]; ok {
				list = append(list, t)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = threads
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.getState(ctx),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) ThreadChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldState":       req.SinceState,
		"newState":       h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

// ============================================================================
// Email methods
// ============================================================================

// lockedFrom returns the From address that mail created or submitted for
// accountID must use: the authenticated user's own address (the JWT sub), or
// the target account when a caller with EmissaryUserManagement masquerades as
// a different account. masquerading reports whether the target account
// differs from the caller's own. There is no configured From address and a
// client-supplied From is never honoured.
func lockedFrom(ctx context.Context, accountID string) (from string, masquerading bool) {
	claims := ClaimsFromContext(ctx)
	userAccount := accountID
	if claims != nil && claims.Subject != "" {
		userAccount = claims.Subject
	}
	masquerading = claims != nil && claims.EmissaryUserManagement && !strings.EqualFold(accountID, userAccount)
	from = userAccount
	if masquerading {
		from = accountID
	}
	return
}

// messageIDs renders a single Message-ID value as the JMAP messageId list.
func messageIDs(messageID string) []string {
	if messageID = strings.TrimSpace(messageID); messageID != "" {
		return []string{messageID}
	}
	return []string{}
}

// messageIDDomain extracts the domain part of an address for use as the
// Message-ID domain; an empty result makes GenerateMessageID fall back to a
// local domain.
func messageIDDomain(address string) string {
	if _, domain, ok := strings.Cut(address, "@"); ok {
		return strings.ToLower(strings.TrimSpace(domain))
	}
	return ""
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
	Sender        []EmailAddress            `json:"sender,omitempty"`
	From          []EmailAddress            `json:"from,omitempty"`
	To            []EmailAddress            `json:"to,omitempty"`
	Cc            []EmailAddress            `json:"cc,omitempty"`
	Bcc           []EmailAddress            `json:"bcc,omitempty"`
	ReplyTo       []EmailAddress            `json:"replyTo,omitempty"`
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

type EmailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

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
}

type EmailBodyValue struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem,omitempty"`
	IsTruncated       bool   `json:"isTruncated,omitempty"`
}

type EmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (m *JMAPEmail) setBodyValues(e *email.UserEmail, fetchText, fetchHTML bool) {
	values := map[string]EmailBodyValue{}
	if fetchText && e.Text != "" {
		values["text1"] = EmailBodyValue{Value: e.Text}
	}
	if fetchHTML && e.HTML != "" {
		values["html1"] = EmailBodyValue{Value: e.HTML}
	}
	if len(values) > 0 {
		m.BodyValues = values
	}
}

func (h *Handler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID           string   `json:"accountId"`
		IDs                 []string `json:"ids,omitempty"`
		Properties          []string `json:"properties,omitempty"`
		FetchTextBodyValues bool     `json:"fetchTextBodyValues"`
		FetchHTMLBodyValues bool     `json:"fetchHTMLBodyValues"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Email/get must only expose messages in the requested account. When a
	// user with EmissaryUserManagement masquerades, accountID is the target
	// account supplied in the call.
	emails, err := h.storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return nil, err
	}

	// Build email map
	emailMap := make(map[string]*email.ListedEmail)
	for _, e := range emails {
		emailMap[e.Ref.ID] = e
	}

	var list []JMAPEmail
	var notFound []string

	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			if e, ok := emailMap[id]; ok {
				jmapEmail, err := h.toJMAPEmail(ctx, e)
				if err != nil {
					return nil, err
				}
				jmapEmail.setBodyValues(e.Email, req.FetchTextBodyValues, req.FetchHTMLBodyValues)
				list = append(list, *jmapEmail)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		for _, e := range emails {
			jmapEmail, err := h.toJMAPEmail(ctx, e)
			if err != nil {
				return nil, err
			}
			jmapEmail.setBodyValues(e.Email, req.FetchTextBodyValues, req.FetchHTMLBodyValues)
			list = append(list, *jmapEmail)
		}
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.getState(ctx),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

// jmapAddressList converts a JMAP address property (to, cc, bcc) into internal
// addresses. Client-supplied values are untrusted, so malformed entries are
// reported as invalidArguments instead of panicking on a type assertion.
func jmapAddressList(createMap map[string]interface{}, property string) ([]email.Address, error) {
	entries, ok := createMap[property].([]interface{})
	if !ok {
		return nil, nil
	}
	addresses := make([]email.Address, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalidArguments: %s entries must be objects", property)
		}
		address, ok := object["email"].(string)
		if !ok {
			return nil, fmt.Errorf("invalidArguments: %s entry has a non-string email", property)
		}
		addresses = append(addresses, email.Address{Address: address})
	}
	return addresses, nil
}

func (h *Handler) EmailSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string                 `json:"accountId"`
		Create    map[string]interface{} `json:"create,omitempty"`
		Update    map[string]interface{} `json:"update,omitempty"`
		Destroy   []string               `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	created := make(map[string]interface{})
	updated := make(map[string]interface{})
	destroyed := []string{}
	notCreated := make(map[string]interface{})
	notUpdated := make(map[string]interface{})
	notDestroyed := make(map[string]interface{})

	// Handle destroy. Verify the message belongs to the requested account so
	// a crafted id cannot delete another user's mail.
	for _, id := range req.Destroy {
		listed, err := h.storage.GetEmail(ctx, id)
		if err != nil || listed.Ref == nil || !strings.EqualFold(listed.Ref.AccountID, accountID) {
			notDestroyed[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}
		// Delete the metadata record before the message. Metadata Delete is
		// idempotent, so if the storage delete fails a client retry can safely
		// repeat it; deleting the message first could leave metadata behind
		// that a retry can no longer reach. If metadata deletion fails, keep
		// the message so the whole destroy can be retried.
		if err := h.metadata.Delete(ctx, accountID, id); err != nil {
			slog.Error("JMAP: failed to delete email metadata", "account", accountID, "id", id, "err", err)
			notDestroyed[id] = map[string]string{"type": "serverFail", "description": "Failed to delete metadata: " + err.Error()}
			continue
		}
		if err := h.storage.DeleteEmail(ctx, id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				notDestroyed[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			} else {
				notDestroyed[id] = map[string]string{"type": "serverFail", "description": err.Error()}
			}
			continue
		}
		destroyed = append(destroyed, id)
	}

	// Handle create
	for createID, createData := range req.Create {
		createMap, ok := createData.(map[string]interface{})
		if !ok {
			notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid create data"}
			continue
		}

		// Extract email data. The From address is locked to the authenticated
		// user (or the masquerade target account), so a client-supplied
		// "from" header is ignored.
		from, _ := lockedFrom(ctx, accountID)

		to, err := jmapAddressList(createMap, "to")
		if err != nil {
			return nil, err
		}
		cc, err := jmapAddressList(createMap, "cc")
		if err != nil {
			return nil, err
		}
		bcc, err := jmapAddressList(createMap, "bcc")
		if err != nil {
			return nil, err
		}

		var replyTo []email.Address
		if replyToArr, ok := createMap["replyTo"].([]interface{}); ok {
			for _, r := range replyToArr {
				if rm, ok := r.(map[string]interface{}); ok {
					if address, ok := rm["email"].(string); ok {
						replyTo = append(replyTo, email.Address{Address: address})
					}
				}
			}
		}

		subject := ""
		if s, ok := createMap["subject"].(string); ok {
			subject = s
		}

		bodyValues := map[string]string{}
		if bvMap, ok := createMap["bodyValues"].(map[string]interface{}); ok {
			for partID, v := range bvMap {
				if bv, ok := v.(map[string]interface{}); ok {
					if val, ok := bv["value"].(string); ok {
						bodyValues[partID] = val
					}
				}
			}
		}

		var text string
		var html string
		var textBodySize int
		var htmlBodySize int
		if tbArr, ok := createMap["textBody"].([]interface{}); ok && len(tbArr) > 0 {
			if tb, ok := tbArr[0].(map[string]interface{}); ok {
				if size, ok := tb["size"].(float64); ok {
					textBodySize = int(size)
				}
				if partID, ok := tb["partId"].(string); ok {
					text = bodyValues[partID]
				}
			}
		}
		if hbArr, ok := createMap["htmlBody"].([]interface{}); ok && len(hbArr) > 0 {
			if hb, ok := hbArr[0].(map[string]interface{}); ok {
				if size, ok := hb["size"].(float64); ok {
					htmlBodySize = int(size)
				}
				if partID, ok := hb["partId"].(string); ok {
					html = bodyValues[partID]
				}
			}
		}
		// Clients that only send a single body value (without a matching part
		// id) still populate the text part.
		if text == "" && html == "" {
			for _, value := range bodyValues {
				text = value
				break
			}
		}

		keywords := map[string]bool{}
		if kwMap, ok := createMap["keywords"].(map[string]interface{}); ok {
			for k, v := range kwMap {
				if b, ok := v.(bool); ok && b {
					keywords[k] = true
				}
			}
		}

		var inReplyTo []string
		if irArr, ok := createMap["inReplyTo"].([]interface{}); ok {
			for _, v := range irArr {
				if s, ok := v.(string); ok {
					inReplyTo = append(inReplyTo, s)
				}
			}
		}

		var references []string
		if refArr, ok := createMap["references"].([]interface{}); ok {
			for _, v := range refArr {
				if s, ok := v.(string); ok {
					references = append(references, s)
				}
			}
		}

		// Created mail is stored in the account's inbox (AddEmail assigns the
		// mailbox itself). When a session id is available, use durable
		// per-session draft storage so the draft survives across Lambda
		// invocations (S3Storage cannot add to the delivered-mail tree).
		now := time.Now().UTC()
		newEmail := &email.UserEmail{
			From:       email.Address{Address: from},
			ReplyTo:    replyTo,
			To:         to,
			Cc:         cc,
			Bcc:        bcc,
			Subject:    subject,
			Date:       now,
			Text:       text,
			HTML:       html,
			Size:       int64(textBodySize + htmlBodySize),
			MessageID:  email.GenerateMessageID(messageIDDomain(from)),
			InReplyTo:  inReplyTo,
			References: references,
		}

		var newID string
		if claims := ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
			var err error
			newID, err = h.storage.PutDraft(ctx, claims.SID, createID, newEmail)
			if err != nil {
				notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save draft: " + err.Error()}
				continue
			}
		} else {
			var err error
			// Messages created through JMAP have no original wire bytes; the
			// store renders the .eml from the parsed fields in that case.
			newID, err = h.storage.AddEmail(ctx, accountID, newEmail, nil)
			if err != nil {
				notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save email: " + err.Error()}
				continue
			}
		}

		// Initialize metadata. Created emails carry their JMAP keywords, and
		// the constructor stamps a server-owned receivedAt. Deliveries get a
		// record from the receiver with no keywords. Any client-supplied
		// receivedAt property is intentionally ignored.
		if err := h.metadata.Save(ctx, metadata.NewEmailMetadataFromMap(accountID, newID, keywords)); err != nil {
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save metadata: " + err.Error()}
			continue
		}

		created[createID] = map[string]interface{}{
			"id": newID,
		}
	}

	// Handle keyword updates. JMAP /set.update is a patch: "keywords/<name>"
	// with true adds a keyword and null removes it (false is not a valid
	// keyword value). A whole "keywords" object replaces the keyword set.
	for id, patchData := range req.Update {
		patch, ok := patchData.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]string{"type": "invalidProperties", "description": "Invalid update patch"}
			continue
		}
		// A draft id is session-relative, so it is not returned by GetEmail.
		if !h.emailInAccount(ctx, accountID, id) {
			notUpdated[id] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}
		updatedMetadata, ok, errorType := applyKeywordPatch(h.emailMetadata(ctx, accountID, id), patch)
		if !ok {
			notUpdated[id] = map[string]string{"type": errorType, "description": "Invalid or unsupported keyword patch"}
			continue
		}
		if err := h.metadata.Save(ctx, updatedMetadata); err != nil {
			notUpdated[id] = map[string]string{"type": "serverFail", "description": "Failed to save metadata: " + err.Error()}
			continue
		}
		updated[id] = nil
	}

	resp := map[string]interface{}{
		"accountId":    accountID,
		"oldState":     h.getState(ctx),
		"newState":     h.getState(ctx),
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// For simplicity, return no changes
	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldState":       req.SinceState,
		"newState":       h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string        `json:"accountId"`
		Filter          *EmailFilter  `json:"filter,omitempty"`
		Sort            []interface{} `json:"sort,omitempty"`
		Position        *int          `json:"position,omitempty"`
		Anchor          *string       `json:"anchor,omitempty"`
		AnchorOffset    *int          `json:"anchorOffset,omitempty"`
		Limit           *int          `json:"limit,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
		CollapseThreads *bool         `json:"collapseThreads,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Read only from the requested account's mailbox. The accountId is the
	// authenticated user's id, or the target account when a user with
	// EmissaryUserManagement is masquerading. An inMailbox filter is pushed into
	// storage so only that JMAP mailbox is listed.
	mailboxID := ""
	if req.Filter != nil && req.Filter.InMailbox != nil {
		mailboxID = *req.Filter.InMailbox
	}
	emails, err := h.storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return nil, err
	}

	// Apply filter
	filtered := make([]*email.ListedEmail, 0)
	for _, e := range emails {
		if h.matchesFilter(e, req.Filter) {
			filtered = append(filtered, e)
		}
	}

	// JMAP defaults to receivedAt descending; honour an explicit receivedAt
	// sort direction from the client. Sorting uses the server-recorded receive
	// time (falling back to the message Date for legacy records) so it matches
	// the receivedAt value returned by Email/get.
	ascending := false
	for _, s := range req.Sort {
		sortSpec, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		property, _ := sortSpec["property"].(string)
		if property != "receivedAt" && property != "date" {
			continue
		}
		if v, ok := sortSpec["isAscending"].(bool); ok {
			ascending = v
		}
	}
	received := make(map[string]time.Time, len(filtered))
	for _, e := range filtered {
		received[e.Ref.ID] = receivedAtFromMetadata(h.emailMetadata(ctx, accountID, e.Ref.ID), e.Email.Date)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, right := received[filtered[i].Ref.ID], received[filtered[j].Ref.ID]
		if left.Equal(right) {
			return filtered[i].Ref.ID < filtered[j].Ref.ID
		}
		if ascending {
			return left.Before(right)
		}
		return left.After(right)
	})

	// Apply position/limit for pagination. Untrusted values are clamped so a
	// negative position or limit cannot produce an invalid slice.
	position := 0
	if req.Position != nil {
		position = *req.Position
	}
	if position < 0 {
		position = 0
	}
	if position > len(filtered) {
		position = len(filtered)
	}

	limit := len(filtered) - position
	if req.Limit != nil && *req.Limit < limit {
		limit = *req.Limit
	}
	if limit < 0 {
		limit = 0
	}

	result := filtered[position : position+limit]
	ids := make([]string, len(result))
	for i, e := range result {
		ids[i] = e.Ref.ID
	}

	total := len(filtered)
	if req.CalculateTotal != nil && !*req.CalculateTotal {
		total = -1 // Indicate total not calculated
	}

	resp := map[string]interface{}{
		"accountId":  accountID,
		"queryState": h.getState(ctx),
		"ids":        ids,
		"total":      total,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID       string        `json:"accountId"`
		SinceQueryState string        `json:"sinceQueryState"`
		Filter          interface{}   `json:"filter,omitempty"`
		Sort            []interface{} `json:"sort,omitempty"`
		MaxChanges      *int          `json:"maxChanges,omitempty"`
		UpToID          *string       `json:"upToId,omitempty"`
		CalculateTotal  *bool         `json:"calculateTotal,omitempty"`
		CollapseThreads *bool         `json:"collapseThreads,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldQueryState":  req.SinceQueryState,
		"newQueryState":  h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("Email/copy not implemented")
}

func (h *Handler) EmailImport(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("Email/import not implemented")
}

func (h *Handler) EmailParse(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("Email/parse not implemented")
}

// ============================================================================
// SearchSnippet methods
// ============================================================================

func (h *Handler) SearchSnippetGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("SearchSnippet/get not implemented")
}

// ============================================================================
// Identity methods
// ============================================================================

type Identity struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Email         string         `json:"email"`
	ReplyTo       []EmailAddress `json:"replyTo,omitempty"`
	Bcc           []EmailAddress `json:"bcc,omitempty"`
	TextSignature string         `json:"textSignature"`
	HTMLSignature string         `json:"htmlSignature"`
	MayDelete     bool           `json:"mayDelete"`
}

func (h *Handler) IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Get user info to build identity
	user, err := h.userMgr.Lookup(ctx, accountID)
	if err != nil {
		return nil, err
	}

	identity := Identity{
		ID:            "primary",
		Name:          user.Email,
		Email:         user.Email,
		TextSignature: "",
		HTMLSignature: "",
		MayDelete:     false,
	}

	var list []Identity
	var notFound []string

	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			if id == "primary" {
				list = append(list, identity)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, identity)
	}

	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     h.getState(ctx),
		"list":      list,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("Identity/set not implemented")
}

func (h *Handler) IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string `json:"accountId"`
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	resp := map[string]interface{}{
		"accountId":      accountID,
		"oldState":       req.SinceState,
		"newState":       h.getState(ctx),
		"hasMoreChanges": false,
		"created":        []string{},
		"updated":        []string{},
		"destroyed":      []string{},
	}
	return json.Marshal(resp)
}

// ============================================================================
// EmailSubmission methods
// ============================================================================

func (h *Handler) EmailSubmissionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("EmailSubmission/get not implemented")
}

func (h *Handler) EmailSubmissionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	slog.Debug("JMAP: submitting email", "account", accountID)
	var req struct {
		AccountID string `json:"accountId"`
		Create    map[string]struct {
			IdentityID string `json:"identityId"`
			EmailID    string `json:"emailId"`
			Envelope   *struct {
				MailFrom struct {
					Email string `json:"email"`
				} `json:"mailFrom"`
				RcptTo []struct {
					Email string `json:"email"`
				} `json:"rcptTo"`
			} `json:"envelope,omitempty"`
			SendAt *string `json:"sendAt,omitempty"`
		} `json:"create,omitempty"`
		OnSuccessUpdateEmail *struct {
			Keywords map[string]bool `json:"keywords"`
		} `json:"onSuccessUpdateEmail,omitempty"`
		OnSuccessDestroyEmail []string `json:"onSuccessDestroyEmail,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})

	// For each submission to create
	sessionID := ""
	if claims := ClaimsFromContext(ctx); claims != nil {
		sessionID = claims.SID
	}
	for createID, submission := range req.Create {
		// Get the email to send. Session drafts take precedence: they live in
		// per-session durable storage rather than the delivered-mail tree.
		// Fall back to regular stored mail for submissions that reference an
		// already delivered message. Only an email in the requested account
		// may be submitted, even when masquerading as that account.
		var emailMsg *email.UserEmail
		var ref *email.EmailRef
		var fromDraft bool
		var err error
		if sessionID != "" {
			emailMsg, err = h.storage.GetDraft(ctx, sessionID, submission.EmailID)
			if err == nil {
				// Drafts are scoped to the session, so their ref has no
				// account to compare against the requested one.
				draftRef := email.EmailRefFromID(submission.EmailID)
				ref, fromDraft = &draftRef, true
			} else if errors.Is(err, storage.ErrNotFound) {
				var listed *email.ListedEmail
				listed, err = h.storage.GetEmail(ctx, submission.EmailID)
				if listed != nil {
					emailMsg, ref = listed.Email, listed.Ref
				}
			}
		} else {
			var listed *email.ListedEmail
			listed, err = h.storage.GetEmail(ctx, submission.EmailID)
			if listed != nil {
				emailMsg, ref = listed.Email, listed.Ref
			}
		}
		// Delivered mail must carry an account matching the request. Drafts
		// have no account on their ref; their session scope is the check.
		if err != nil || emailMsg == nil || (!fromDraft && (ref == nil || !strings.EqualFold(ref.AccountID, accountID))) {
			notCreated[createID] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}

		// The From address is locked to the authenticated user: submissions
		// send as the caller's own account, or as the target account when a
		// User Manager (EmissaryUserManagement) masquerades as a different
		// accountId. A client-supplied sender (the envelope mailFrom or the
		// draft's From header) is ignored.
		from, masquerading := lockedFrom(ctx, accountID)
		if !strings.Contains(from, "@") {
			// An account without an email address (for example the built-in
			// "admin" account) cannot send mail.
			notCreated[createID] = map[string]string{
				"type":        "invalidProperties",
				"description": "From address must be a valid email address",
			}
			continue
		}
		parsed, parseErr := mail.ParseAddress(from)
		if parseErr != nil {
			notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid from address"}
			continue
		}
		from = parsed.Address

		// A locked From still has to belong to one of the account's verified
		// email identities. SES identities are the source of truth; rejecting
		// here keeps a misconfigured account from quietly sending as an
		// unverified address (or, with the log sender, disappearing entirely).
		if h.fromValidator != nil {
			if err := h.fromValidator.Validate(ctx, from); err != nil {
				if errors.Is(err, identity.ErrNotAllowed) {
					slog.Warn("JMAP: rejected From outside verified identities", "from", from, "err", err)
					notCreated[createID] = map[string]string{
						"type":        "forbidden",
						"description": "From address is not a verified email identity",
					}
				} else {
					slog.Error("JMAP: could not verify From identity", "from", from, "err", err)
					notCreated[createID] = map[string]string{
						"type":        "serverError",
						"description": "Could not verify From address",
					}
				}
				continue
			}
		}

		// When sending on behalf of another account, replies must go back to
		// the same address that appears in From. Default Reply-To to From when
		// the client did not set one, and reject a conflicting one.
		replyTo := append([]email.Address(nil), emailMsg.ReplyTo...)
		if masquerading {
			mismatch := false
			for _, addr := range replyTo {
				if !strings.EqualFold(addr.Address, from) {
					mismatch = true
					break
				}
			}
			if mismatch {
				notCreated[createID] = map[string]string{
					"type":        "invalidProperties",
					"description": "Reply-To must match From when masquerading",
				}
				continue
			}
			if len(replyTo) == 0 {
				replyTo = []email.Address{{Address: from}}
			}
		}

		// Determine recipients from the email's headers
		var toAddresses []email.Address
		var ccAddresses []email.Address
		var bccAddresses []email.Address
		for _, addr := range emailMsg.To {
			toAddresses = append(toAddresses, addr)
		}
		for _, addr := range emailMsg.Cc {
			ccAddresses = append(ccAddresses, addr)
		}
		for _, addr := range emailMsg.Bcc {
			bccAddresses = append(bccAddresses, addr)
		}

		// If envelope is provided, validate that all recipients in the email are covered by the envelope's RcptTo
		if submission.Envelope != nil && len(submission.Envelope.RcptTo) > 0 {
			envelopeRecipients := make(map[string]bool)
			envelopeInvalid := false
			for _, rcpt := range submission.Envelope.RcptTo {
				parsed, err := mail.ParseAddress(rcpt.Email)
				if err != nil {
					notCreated[createID] = map[string]string{"type": "invalidRecipients", "description": "Invalid recipient address in envelope"}
					envelopeInvalid = true
					continue
				}
				envelopeRecipients[parsed.Address] = true
			}
			// Check that all To, Cc, Bcc recipients are in the envelope
			for _, addr := range emailMsg.To {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Cc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			for _, addr := range emailMsg.Bcc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					envelopeInvalid = true
					continue
				}
			}
			// A `continue` in the loops above only skips that address; the
			// submission itself must be skipped so it is neither sent nor
			// reported as created.
			if envelopeInvalid {
				continue
			}
		}

		if len(toAddresses) == 0 && len(ccAddresses) == 0 && len(bccAddresses) == 0 {
			notCreated[createID] = map[string]string{"type": "noRecipients", "description": "No recipients"}
			continue
		}

		// Create the message to send. The thread headers (In-Reply-To and
		// References) and the stored Message-ID are carried into the delivered
		// message so replies and forwards stay linked to their thread.
		message := &email.UserEmail{
			From:       email.Address{Address: from},
			ReplyTo:    replyTo,
			To:         toAddresses,
			Cc:         ccAddresses,
			Bcc:        bccAddresses,
			Subject:    emailMsg.Subject,
			Date:       time.Now().UTC(),
			Text:       emailMsg.Text,
			HTML:       emailMsg.HTML,
			MessageID:  emailMsg.MessageID,
			InReplyTo:  emailMsg.InReplyTo,
			References: emailMsg.References,
		}

		// Send the email
		if err := h.sender.Send(ctx, message); err != nil {
			slog.Error("JMAP: failed to send email", "account", accountID, "create_id", createID, "err", err)
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to send email: " + err.Error()}
			continue
		}
		slog.Info("JMAP: email submitted", "account", accountID, "email_id", submission.EmailID)

		// A session draft has served its purpose once the message is sent.
		// Deletion failures must not turn a successful send into a client
		// error, since that would encourage a duplicate submission.
		if sessionID != "" {
			if err := h.storage.DeleteDraft(ctx, sessionID, submission.EmailID); err != nil {
				slog.Warn("JMAP: failed to delete sent draft", "email_id", submission.EmailID, "err", err)
			}
			// The draft's keyword record is tied to the draft id, so remove it
			// too. A missing record is not an error.
			if err := h.metadata.Delete(ctx, req.AccountID, submission.EmailID); err != nil {
				slog.Warn("JMAP: failed to delete sent draft metadata", "account", req.AccountID, "email_id", submission.EmailID, "err", err)
			}
		}

		// Success - create the submission object
		created[createID] = map[string]interface{}{
			"id":         submission.EmailID, // Use the email ID as submission ID
			"identityId": submission.IdentityID,
			"emailId":    submission.EmailID,
			"envelope": map[string]interface{}{
				"mailFrom": map[string]string{"email": from},
				"rcptTo":   toAddresses,
			},
		}
	}

	resp := map[string]interface{}{
		"accountId":  req.AccountID,
		"oldState":   h.getState(ctx),
		"newState":   h.getState(ctx),
		"created":    created,
		"notCreated": notCreated,
	}
	return json.Marshal(resp)
}

func (h *Handler) EmailSubmissionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("EmailSubmission/changes not implemented")
}

func (h *Handler) EmailSubmissionQuery(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("EmailSubmission/query not implemented")
}

func (h *Handler) EmailSubmissionQueryChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("EmailSubmission/queryChanges not implemented")
}

// ============================================================================
// VacationResponse methods
// ============================================================================

func (h *Handler) VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("VacationResponse/get not implemented")
}

func (h *Handler) VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("VacationResponse/set not implemented")
}

func (h *Handler) VacationResponseChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("VacationResponse/changes not implemented")
}

// ============================================================================
// PushSubscription methods
// ============================================================================

func (h *Handler) PushSubscriptionGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("PushSubscription/get not implemented")
}

func (h *Handler) PushSubscriptionSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("PushSubscription/set not implemented")
}

func (h *Handler) PushSubscriptionChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("PushSubscription/changes not implemented")
}

// ============================================================================
// Helper methods
// ============================================================================

func (h *Handler) getMailboxes(ctx context.Context, accountID string) []Mailbox {
	// Return standard JMAP mailboxes. Counts are scoped to the requested
	// account so a masquerading user sees the target user's mailbox state.

	count := func(mailboxID string) int { return h.countEmailsInMailbox(ctx, accountID, mailboxID) }
	unread := func(mailboxID string) int { return h.countUnreadInMailbox(ctx, accountID, mailboxID) }
	threads := func(mailboxID string) int { return h.countThreadsInMailbox(ctx, accountID, mailboxID) }

	return []Mailbox{
		{
			ID:            "inbox",
			Name:          "Inbox",
			ParentID:      nil,
			Role:          stringPtr("inbox"),
			SortOrder:     0,
			TotalEmails:   count("inbox"),
			UnreadEmails:  unread("inbox"),
			TotalThreads:  threads("inbox"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "sent",
			Name:          "Sent",
			ParentID:      nil,
			Role:          stringPtr("sent"),
			SortOrder:     1,
			TotalEmails:   count("sent"),
			UnreadEmails:  0,
			TotalThreads:  threads("sent"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "drafts",
			Name:          "Drafts",
			ParentID:      nil,
			Role:          stringPtr("drafts"),
			SortOrder:     2,
			TotalEmails:   count("drafts"),
			UnreadEmails:  0,
			TotalThreads:  threads("drafts"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "trash",
			Name:          "Trash",
			ParentID:      nil,
			Role:          stringPtr("trash"),
			SortOrder:     3,
			TotalEmails:   count("trash"),
			UnreadEmails:  0,
			TotalThreads:  threads("trash"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      true,
				MaySubmit:      false,
			},
			IsSubscribed: true,
		},
		{
			ID:            "archive",
			Name:          "Archive",
			ParentID:      nil,
			Role:          stringPtr("archive"),
			SortOrder:     4,
			TotalEmails:   count("archive"),
			UnreadEmails:  unread("archive"),
			TotalThreads:  threads("archive"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      true,
			},
			IsSubscribed: true,
		},
		{
			ID:            "junk",
			Name:          "Junk",
			ParentID:      nil,
			Role:          stringPtr("junk"),
			SortOrder:     5,
			TotalEmails:   count("junk"),
			UnreadEmails:  unread("junk"),
			TotalThreads:  threads("junk"),
			UnreadThreads: 0,
			MyRights: MailboxRights{
				MayReadItems:   true,
				MayAddItems:    true,
				MayRemoveItems: true,
				MaySetSeen:     true,
				MaySetKeywords: true,
				MayCreateChild: true,
				MayRename:      false,
				MayDelete:      false,
				MaySubmit:      false,
			},
			IsSubscribed: true,
		},
	}
}

func (h *Handler) getThreads(ctx context.Context, accountID string) []Thread {
	// For now, each email is its own thread
	// A real implementation would group by thread ID (References/In-Reply-To headers)
	emails, err := h.storage.ListEmails(ctx, accountID, "")
	if err != nil {
		return []Thread{}
	}

	threads := make([]Thread, 0, len(emails))
	for _, e := range emails {
		threads = append(threads, Thread{
			ID:       e.Ref.ID,
			EmailIDs: []string{e.Ref.ID},
		})
	}
	return threads
}

func (h *Handler) getState(ctx context.Context) string {
	// Use a simple timestamp-based state
	// In a real implementation, this would be a proper state token
	return fmt.Sprintf("%d", time.Now().Unix())
}

func (h *Handler) countEmailsInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	return len(emails)
}

func (h *Handler) countUnreadInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.storage.ListEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range emails {
		meta := h.emailMetadata(ctx, accountID, e.Ref.ID)
		// JMAP unread means no $seen. Drafts are excluded from unread counts.
		if !meta.HasKeyword(metadata.KeywordSeen) && !meta.HasKeyword(metadata.KeywordDraft) {
			count++
		}
	}
	return count
}

func (h *Handler) countThreadsInMailbox(ctx context.Context, accountID, mailboxID string) int {
	// For now, each email is its own thread
	return h.countEmailsInMailbox(ctx, accountID, mailboxID)
}

func (h *Handler) toJMAPEmail(ctx context.Context, listed *email.ListedEmail) (*JMAPEmail, error) {
	e := listed.Email
	id := listed.Ref.ID

	// Stitch JMAP keywords from metadata into the returned object. A missing
	// metadata record is normal (older messages, receiver without metadata)
	// and simply yields an empty keyword set; it must not fail the request.
	meta := h.emailMetadata(ctx, listed.Ref.AccountID, id)
	keywords := make(map[string]bool, len(meta.Keywords))
	for keyword, enabled := range meta.Keywords {
		if enabled {
			keywords[keyword] = true
		}
	}

	// Determine mailbox IDs from the mailbox the message was delivered to.
	// Locally created messages are stored in the inbox by AddEmail.
	mailboxID := listed.Ref.MailboxID
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	mailboxIDs := map[string]bool{
		mailboxID: true,
	}

	// Convert addresses
	from := []EmailAddress{{Name: e.From.Name, Email: e.From.Address}}

	to := make([]EmailAddress, len(e.To))
	for i, addr := range e.To {
		to[i] = EmailAddress{Name: addr.Name, Email: addr.Address}
	}

	cc := make([]EmailAddress, len(e.Cc))
	for i, addr := range e.Cc {
		cc[i] = EmailAddress{Name: addr.Name, Email: addr.Address}
	}

	bcc := make([]EmailAddress, len(e.Bcc))
	for i, addr := range e.Bcc {
		bcc[i] = EmailAddress{Name: addr.Name, Email: addr.Address}
	}

	replyTo := make([]EmailAddress, len(e.ReplyTo))
	for i, addr := range e.ReplyTo {
		replyTo[i] = EmailAddress{Name: addr.Name, Email: addr.Address}
	}

	// Build preview from text body
	preview := e.Text
	if len(preview) > 200 {
		preview = preview[:200]
	}
	preview = strings.ReplaceAll(preview, "\n", " ")
	preview = strings.TrimSpace(preview)

	textPartID := "text1"
	htmlPartID := "html1"
	textBody := []EmailBodyPart{}
	htmlBody := []EmailBodyPart{}
	if e.Text != "" {
		textBody = append(textBody, EmailBodyPart{PartID: &textPartID, Type: "text/plain", Size: len(e.Text)})
	}
	if e.HTML != "" {
		htmlBody = append(htmlBody, EmailBodyPart{PartID: &htmlPartID, Type: "text/html", Size: len(e.HTML)})
	}

	// Format dates. receivedAt is server-managed metadata, not the Date
	// header (which is the message's sent time). Legacy records without a
	// receivedAt value fall back to the Date header.
	receivedAt := receivedAtFromMetadata(meta, e.Date).UTC().Format(time.RFC3339)
	sentAt := e.Date.UTC().Format(time.RFC3339)

	return &JMAPEmail{
		ID:            id,
		BlobID:        id, // Use ID as blobId for now
		ThreadID:      id, // Each email is its own thread for now
		MailboxIDs:    mailboxIDs,
		Keywords:      keywords,
		Size:          int(e.Size),
		ReceivedAt:    receivedAt,
		MessageID:     messageIDs(e.MessageID),
		InReplyTo:     e.InReplyTo,
		References:    e.References,
		Sender:        from, // Use From as Sender for simplicity
		From:          from,
		To:            to,
		Cc:            cc,
		Bcc:           bcc,
		ReplyTo:       replyTo,
		Subject:       e.Subject,
		SentAt:        &sentAt,
		HasAttachment: false, // Would need to parse from wire format
		Preview:       preview,
		BodyStructure: nil,
		BodyValues:    nil,
		TextBody:      textBody,
		HTMLBody:      htmlBody,
		Attachments:   []EmailBodyPart{},
		Headers:       []EmailHeader{},
	}, nil
}

func (h *Handler) matchesFilter(listed *email.ListedEmail, filter *EmailFilter) bool {
	if filter == nil {
		return true
	}
	e := listed.Email
	mailboxID := listed.Ref.MailboxID

	// InMailbox filter. Storage resolves this into a mailbox prefix; when the
	// mailbox is known on the message, verify it as well so a stale or
	// hand-crafted id cannot be used to read from another mailbox.
	if filter.InMailbox != nil && mailboxID != "" && !strings.EqualFold(mailboxID, *filter.InMailbox) {
		return false
	}

	// InMailboxOtherThan filter
	if mailboxID != "" {
		for _, mb := range filter.InMailboxOtherThan {
			if strings.EqualFold(mb, mailboxID) {
				return false
			}
		}
	}

	// Date filters
	if filter.Before != nil {
		before, err := time.Parse(time.RFC3339, *filter.Before)
		if err == nil && !e.Date.Before(before) {
			return false
		}
	}
	if filter.After != nil {
		after, err := time.Parse(time.RFC3339, *filter.After)
		if err == nil && !e.Date.After(after) {
			return false
		}
	}

	// Size filters
	if filter.MinSize != nil && int(e.Size) < *filter.MinSize {
		return false
	}
	if filter.MaxSize != nil && int(e.Size) > *filter.MaxSize {
		return false
	}

	// Text search (simple substring match on subject, from, to, text)
	if filter.Text != nil {
		text := strings.ToLower(*filter.Text)
		found := false
		if strings.Contains(strings.ToLower(e.Subject), text) {
			found = true
		}
		// Check From (single address)
		if strings.Contains(strings.ToLower(e.From.Address), text) || strings.Contains(strings.ToLower(e.From.Name), text) {
			found = true
		}
		for _, addr := range e.To {
			if strings.Contains(strings.ToLower(addr.Address), text) || strings.Contains(strings.ToLower(addr.Name), text) {
				found = true
				break
			}
		}
		if strings.Contains(strings.ToLower(e.Text), text) {
			found = true
		}
		if !found {
			return false
		}
	}

	// From filter
	if filter.From != nil {
		text := strings.ToLower(*filter.From)
		found := false
		if strings.Contains(strings.ToLower(e.From.Address), text) || strings.Contains(strings.ToLower(e.From.Name), text) {
			found = true
		}
		if !found {
			return false
		}
	}

	// To filter
	if filter.To != nil {
		text := strings.ToLower(*filter.To)
		found := false
		for _, addr := range e.To {
			if strings.Contains(strings.ToLower(addr.Address), text) || strings.Contains(strings.ToLower(addr.Name), text) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Subject filter
	if filter.Subject != nil {
		if !strings.Contains(strings.ToLower(e.Subject), strings.ToLower(*filter.Subject)) {
			return false
		}
	}

	// Body filter
	if filter.Body != nil {
		if !strings.Contains(strings.ToLower(e.Text), strings.ToLower(*filter.Body)) {
			return false
		}
	}

	return true
}

// emailMetadata returns the metadata for an email. A missing record (or a
// backend error) is treated as empty so JMAP responses always succeed even for
// messages stored before metadata existed; no receive time is fabricated.
func (h *Handler) emailMetadata(ctx context.Context, userID, mailID string) metadata.EmailMetadata {
	meta, err := h.metadata.Get(ctx, userID, mailID)
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

// receivedAtFromMetadata returns the server-recorded receive time, falling
// back to the message Date for records created before receivedAt existed.
func receivedAtFromMetadata(meta metadata.EmailMetadata, fallback time.Time) time.Time {
	if meta.ReceivedAt != "" {
		if received, err := time.Parse(time.RFC3339, meta.ReceivedAt); err == nil {
			return received.UTC()
		}
	}
	return fallback
}

// emailInAccount verifies that id belongs to accountID. Drafts use ids that
// are relative to the JMAP session, so they are looked up separately.
func (h *Handler) emailInAccount(ctx context.Context, accountID, id string) bool {
	listed, err := h.storage.GetEmail(ctx, id)
	if err == nil && listed != nil && listed.Ref != nil && strings.EqualFold(listed.Ref.AccountID, accountID) {
		return true
	}
	if claims := ClaimsFromContext(ctx); claims != nil && claims.SID != "" {
		draft, err := h.storage.GetDraft(ctx, claims.SID, id)
		return err == nil && draft != nil
	}
	return false
}

// applyKeywordPatch applies an Email/set update patch to a metadata record.
// Per RFC 8621, "keywords/<name>": true sets a keyword and null removes it;
// false is not a valid patch value. A full "keywords" object replaces the set.
// receivedAt is server-owned: it is preserved by Clone and never read from the
// patch, so clients cannot assign, edit or clear it. The returned string is a
// JMAP error type when ok is false.
func applyKeywordPatch(current metadata.EmailMetadata, patch map[string]interface{}) (metadata.EmailMetadata, bool, string) {
	updated := current.Clone()

	// Apply a whole-object replacement first, then any individual keyword
	// patches, so the result does not depend on Go map iteration order.
	if value, ok := patch["keywords"]; ok {
		keywords, ok := value.(map[string]interface{})
		if !ok {
			return updated, false, "invalidProperties"
		}
		replacement := make(map[string]bool)
		for name, enabled := range keywords {
			set, ok := enabled.(bool)
			if !ok {
				return updated, false, "invalidProperties"
			}
			normalized, valid := metadata.NormalizeKeyword(name)
			if !valid {
				return updated, false, "invalidProperties"
			}
			if set {
				replacement[normalized] = true
			}
		}
		updated.Keywords = replacement
	}

	for prop, value := range patch {
		if prop == "keywords" {
			continue
		}
		if !strings.HasPrefix(prop, "keywords/") {
			// Ignore properties we do not manage (for example mailboxIds)
			// rather than failing the whole update.
			continue
		}
		normalized, valid := metadata.NormalizeKeyword(strings.TrimPrefix(prop, "keywords/"))
		if !valid {
			return updated, false, "invalidProperties"
		}
		switch v := value.(type) {
		case nil:
			delete(updated.Keywords, normalized)
		case bool:
			if !v {
				return updated, false, "invalidProperties"
			}
			updated.Keywords[normalized] = true
		default:
			return updated, false, "invalidProperties"
		}
	}

	return updated, true, ""
}

func stringPtr(s string) *string {
	return &s
}
