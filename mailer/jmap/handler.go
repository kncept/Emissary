package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/email"
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

// Handler implements MethodHandler by bridging to the existing storage,
// metadata, and user management layers.
type Handler struct {
	storage  storage.Storage
	metadata metadata.MetadataStore
	userMgr  usermanager.UserManager
	sender   sender.Sender
}

// NewHandler creates a new JMAP method handler.
func NewHandler(store storage.Storage, meta metadata.MetadataStore, userMgr usermanager.UserManager, mailSender sender.Sender) *Handler {
	return &Handler{
		storage:  store,
		metadata: meta,
		userMgr:  userMgr,
		sender:   mailSender,
	}
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

	// Apply position/limit for pagination
	position := 0
	if req.Position != nil {
		position = *req.Position
	}
	if position > len(mailboxes) {
		position = len(mailboxes)
	}

	limit := len(mailboxes) - position
	if req.Limit != nil && *req.Limit < limit {
		limit = *req.Limit
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

func (h *Handler) EmailGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		AccountID  string   `json:"accountId"`
		IDs        []string `json:"ids,omitempty"`
		Properties []string `json:"properties,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	// Email/get must only expose messages in the requested account. When a
	// user with EmissaryUserManagement masquerades, accountID is the target
	// account supplied in the call.
	emails, err := h.storage.ListAccountEmails(ctx, accountID, "")
	if err != nil {
		return nil, err
	}

	// Build email map
	emailMap := make(map[string]*email.UserEmail)
	for _, e := range emails {
		emailMap[e.ID] = e
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
		msg, err := h.storage.GetEmail(ctx, id)
		if err != nil || !strings.EqualFold(msg.AccountID, accountID) {
			notDestroyed[id] = map[string]string{"type": "notFound", "description": "Email not found"}
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
		h.metadata.Delete(ctx, id)
		destroyed = append(destroyed, id)
	}

	// Handle create
	for createID, createData := range req.Create {
		createMap, ok := createData.(map[string]interface{})
		if !ok {
			notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid create data"}
			continue
		}

		// Extract email data
		from := ""
		if fromArr, ok := createMap["from"].([]interface{}); ok && len(fromArr) > 0 {
			if f, ok := fromArr[0].(map[string]interface{}); ok {
				from = f["email"].(string)
			}
		}

		var to []email.Address
		if toArr, ok := createMap["to"].([]interface{}); ok {
			for _, t := range toArr {
				if tm, ok := t.(map[string]interface{}); ok {
					to = append(to, email.Address{Address: tm["email"].(string)})
				}
			}
		}

		var cc []email.Address
		if ccArr, ok := createMap["cc"].([]interface{}); ok {
			for _, c := range ccArr {
				if cm, ok := c.(map[string]interface{}); ok {
					cc = append(cc, email.Address{Address: cm["email"].(string)})
				}
			}
		}

		var bcc []email.Address
		if bccArr, ok := createMap["bcc"].([]interface{}); ok {
			for _, b := range bccArr {
				if bm, ok := b.(map[string]interface{}); ok {
					bcc = append(bcc, email.Address{Address: bm["email"].(string)})
				}
			}
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

		var text string
		var textBodySize int
		if tbArr, ok := createMap["textBody"].([]interface{}); ok && len(tbArr) > 0 {
			if tb, ok := tbArr[0].(map[string]interface{}); ok {
				textBodySize = int(tb["size"].(float64))
			}
		}
		if bvMap, ok := createMap["bodyValues"].(map[string]interface{}); ok {
			for _, v := range bvMap {
				if bv, ok := v.(map[string]interface{}); ok {
					if val, ok := bv["value"].(string); ok {
						text = val
						break
					}
				}
			}
		}

		mailboxIDs := map[string]bool{}
		if mbMap, ok := createMap["mailboxIds"].(map[string]interface{}); ok {
			for k, v := range mbMap {
				if b, ok := v.(bool); ok && b {
					mailboxIDs[k] = true
				}
			}
		}
		if len(mailboxIDs) == 0 {
			mailboxIDs["inbox"] = true
		}
		// A message belongs to one JMAP mailbox for storage purposes. Pick
		// deterministically so the account/mailbox scoping is unambiguous.
		mailboxID := "inbox"
		for mb := range mailboxIDs {
			if mailboxIDs[mb] && (mailboxID == "inbox" || mb < mailboxID) {
				mailboxID = mb
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

		// Created mail is stored in the requested account's mailbox. When a
		// session id is available, use durable per-session draft storage so
		// the draft survives across Lambda invocations (S3Storage cannot add
		// messages to the delivered-mail tree).
		now := time.Now().UTC()
		newEmail := &email.UserEmail{
			ID:         createID,
			AccountID:  accountID,
			MailboxID:  mailboxID,
			From:       email.Address{Address: from},
			ReplyTo:    replyTo,
			To:         to,
			Cc:         cc,
			Bcc:        bcc,
			Subject:    subject,
			Date:       now,
			Text:       text,
			Size:       int64(textBodySize),
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
			newID = generateEmailID()
			newEmail.ID = newID
			if err := h.storage.AddEmail(ctx, newEmail); err != nil {
				notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save email: " + err.Error()}
				continue
			}
		}
		newEmail.ID = newID

		// Initialize metadata
		if err := h.metadata.Save(ctx, metadata.EmailMetadata{
			ID:      newID,
			Read:    keywords["$seen"],
			Flagged: keywords["$flagged"],
		}); err != nil {
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to save metadata: " + err.Error()}
			continue
		}

		created[createID] = map[string]interface{}{
			"id": newID,
		}
	}

	// Update is not implemented
	for id := range req.Update {
		notUpdated[id] = map[string]string{"type": "forbidden", "description": "Email update not supported"}
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
	emails, err := h.storage.ListAccountEmails(ctx, accountID, mailboxID)
	if err != nil {
		return nil, err
	}

	// Apply filter
	filtered := make([]*email.UserEmail, 0)
	for _, e := range emails {
		if h.matchesFilter(e, req.Filter) {
			filtered = append(filtered, e)
		}
	}

	// JMAP defaults to receivedAt descending; honour an explicit receivedAt
	// sort direction from the client.
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
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Date.Equal(filtered[j].Date) {
			return filtered[i].ID < filtered[j].ID
		}
		if ascending {
			return filtered[i].Date.Before(filtered[j].Date)
		}
		return filtered[i].Date.After(filtered[j].Date)
	})

	// Apply position/limit for pagination
	position := 0
	if req.Position != nil {
		position = *req.Position
	}
	if position > len(filtered) {
		position = len(filtered)
	}

	limit := len(filtered) - position
	if req.Limit != nil && *req.Limit < limit {
		limit = *req.Limit
	}

	result := filtered[position : position+limit]
	ids := make([]string, len(result))
	for i, e := range result {
		ids[i] = e.ID
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

	// The caller's identity, not the account being accessed, decides which
	// From addresses are allowed. A User Manager (EmissaryUserManagement) can
	// masquerade as another account and therefore send with another From.
	claims := ClaimsFromContext(ctx)
	canMasquerade := claims != nil && claims.EmissaryUserManagement
	userAccount := accountID
	if claims != nil && claims.Subject != "" {
		userAccount = claims.Subject
	}

	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})

	// For each submission to create
	sessionID := ""
	if claims != nil {
		sessionID = claims.SID
	}
	for createID, submission := range req.Create {
		// Get the email to send. Session drafts take precedence: they live in
		// per-session durable storage rather than the delivered-mail tree.
		// Fall back to regular stored mail for submissions that reference an
		// already delivered message. Only an email in the requested account
		// may be submitted, even when masquerading as that account.
		var emailMsg *email.UserEmail
		var err error
		if sessionID != "" {
			emailMsg, err = h.storage.GetDraft(ctx, sessionID, submission.EmailID)
			if errors.Is(err, storage.ErrNotFound) {
				emailMsg, err = h.storage.GetEmail(ctx, submission.EmailID)
			}
		} else {
			emailMsg, err = h.storage.GetEmail(ctx, submission.EmailID)
		}
		if err != nil || !strings.EqualFold(emailMsg.AccountID, accountID) {
			notCreated[createID] = map[string]string{"type": "notFound", "description": "Email not found"}
			continue
		}

		// Resolve the requested From address. The explicit envelope sender
		// wins, followed by the draft's From header. For replies with no
		// explicit sender, default to the original recipient.
		var from string
		if submission.Envelope != nil {
			from = submission.Envelope.MailFrom.Email
		}
		if from == "" {
			from = emailMsg.From.Address
		}
		if from == "" {
			var inReplyTo string
			if len(emailMsg.InReplyTo) > 0 {
				inReplyTo = emailMsg.InReplyTo[0]
			} else if len(emailMsg.References) > 0 {
				inReplyTo = emailMsg.References[0]
			}
			if inReplyTo != "" {
				if original, err := h.storage.GetEmail(ctx, inReplyTo); err == nil && len(original.To) > 0 {
					from = original.To[0].Address
				}
			}
		}
		// A JMAP account is normally the user's email address. Fall back to
		// the configured sender only when the account is not an address (for
		// example the built-in "admin" account).
		if from == "" && strings.Contains(accountID, "@") {
			from = accountID
		}
		if from == "" {
			from = h.sender.From()
		}

		// Validate the From address syntax.
		if from != "" {
			parsed, err := mail.ParseAddress(from)
			if err != nil {
				notCreated[createID] = map[string]string{"type": "invalidProperties", "description": "Invalid from address"}
				continue
			}
			from = parsed.Address
		}

		// A submission may only use the authenticated user's own address,
		// unless that user is allowed to masquerade as another account.
		masquerading := from != "" && !strings.EqualFold(from, userAccount)
		if masquerading && !canMasquerade {
			notCreated[createID] = map[string]string{
				"type":        "forbidden",
				"description": "From address does not match the authenticated user",
			}
			continue
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
			for _, rcpt := range submission.Envelope.RcptTo {
				parsed, err := mail.ParseAddress(rcpt.Email)
				if err != nil {
					notCreated[createID] = map[string]string{"type": "invalidRecipients", "description": "Invalid recipient address in envelope"}
					continue
				}
				envelopeRecipients[parsed.Address] = true
			}
			// Check that all To, Cc, Bcc recipients are in the envelope
			for _, addr := range emailMsg.To {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					continue
				}
			}
			for _, addr := range emailMsg.Cc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					continue
				}
			}
			for _, addr := range emailMsg.Bcc {
				if !envelopeRecipients[addr.Address] {
					notCreated[createID] = map[string]string{"type": "forbidden", "description": "Recipient not in envelope: " + addr.Address}
					continue
				}
			}
		}

		if len(toAddresses) == 0 && len(ccAddresses) == 0 && len(bccAddresses) == 0 {
			notCreated[createID] = map[string]string{"type": "noRecipients", "description": "No recipients"}
			continue
		}

		// Create the message to send
		message := &email.UserEmail{
			From:    email.Address{Address: from},
			ReplyTo: replyTo,
			To:      toAddresses,
			Cc:      ccAddresses,
			Bcc:     bccAddresses,
			Subject: emailMsg.Subject,
			Date:    time.Now().UTC(),
			Text:    emailMsg.Text,
		}

		// Send the email
		if err := h.sender.Send(ctx, message); err != nil {
			notCreated[createID] = map[string]string{"type": "serverFail", "description": "Failed to send email: " + err.Error()}
			continue
		}

		// A session draft has served its purpose once the message is sent.
		// Deletion failures must not turn a successful send into a client
		// error, since that would encourage a duplicate submission.
		if sessionID != "" {
			if err := h.storage.DeleteDraft(ctx, sessionID, submission.EmailID); err != nil {
				log.Printf("failed to delete JMAP draft %q: %v", submission.EmailID, err)
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
	emails, err := h.storage.ListAccountEmails(ctx, accountID, "")
	if err != nil {
		return []Thread{}
	}

	threads := make([]Thread, 0, len(emails))
	for _, e := range emails {
		threads = append(threads, Thread{
			ID:       e.ID,
			EmailIDs: []string{e.ID},
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
	emails, err := h.storage.ListAccountEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	return len(emails)
}

func (h *Handler) countUnreadInMailbox(ctx context.Context, accountID, mailboxID string) int {
	emails, err := h.storage.ListAccountEmails(ctx, accountID, mailboxID)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range emails {
		meta, err := h.metadata.Get(ctx, e.ID)
		if err == nil && !meta.Read {
			count++
		}
	}
	return count
}

func (h *Handler) countThreadsInMailbox(ctx context.Context, accountID, mailboxID string) int {
	// For now, each email is its own thread
	return h.countEmailsInMailbox(ctx, accountID, mailboxID)
}

func (h *Handler) toJMAPEmail(ctx context.Context, e *email.UserEmail) (*JMAPEmail, error) {
	// Get metadata for read/flagged status
	meta, err := h.metadata.Get(ctx, e.ID)
	if err != nil {
		meta = metadata.EmailMetadata{ID: e.ID}
	}

	keywords := map[string]bool{}
	if meta.Read {
		keywords["$seen"] = true
	}
	if meta.Flagged {
		keywords["$flagged"] = true
	}

	// Determine mailbox IDs from the mailbox the message was delivered to.
	// The delivered key records it (users/<account>/<mailbox>/<file>.eml);
	// locally created messages use their requested mailbox.
	mailboxID := e.MailboxID
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

	// Format dates
	receivedAt := e.Date.UTC().Format(time.RFC3339)
	sentAt := e.Date.UTC().Format(time.RFC3339)

	return &JMAPEmail{
		ID:            e.ID,
		BlobID:        e.ID, // Use ID as blobId for now
		ThreadID:      e.ID, // Each email is its own thread for now
		MailboxIDs:    mailboxIDs,
		Keywords:      keywords,
		Size:          int(e.Size),
		ReceivedAt:    receivedAt,
		MessageID:     []string{}, // Would need to parse from headers
		InReplyTo:     []string{},
		References:    []string{},
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
		TextBody:      []EmailBodyPart{},
		HTMLBody:      []EmailBodyPart{},
		Attachments:   []EmailBodyPart{},
		Headers:       []EmailHeader{},
	}, nil
}

func (h *Handler) matchesFilter(e *email.UserEmail, filter *EmailFilter) bool {
	if filter == nil {
		return true
	}

	// InMailbox filter. Storage resolves this into a mailbox prefix; when the
	// mailbox is known on the message, verify it as well so a stale or
	// hand-crafted id cannot be used to read from another mailbox.
	if filter.InMailbox != nil && e.MailboxID != "" && !strings.EqualFold(e.MailboxID, *filter.InMailbox) {
		return false
	}

	// InMailboxOtherThan filter
	if e.MailboxID != "" {
		for _, mb := range filter.InMailboxOtherThan {
			if strings.EqualFold(mb, e.MailboxID) {
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

func stringPtr(s string) *string {
	return &s
}

func generateEmailID() string {
	// Generate a simple unique ID using timestamp and random bytes
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x%d", b, time.Now().UnixNano())
}
