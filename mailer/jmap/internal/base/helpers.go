package base

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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

// ThreadGroup is the account-local grouping of emails into a JMAP Thread.
// EmailIDs are ordered by receivedAt (oldest first) with an id tie-breaker.
type ThreadGroup struct {
	ID       string   `json:"id"`
	EmailIDs []string `json:"emailIds"`
}

// threadPrefixPattern strips the automatically added prefixes named by RFC
// 8621 section 3: Re:, Fwd:/FW:, Re[2]: and [List-Tag].
var threadPrefixPattern = regexp.MustCompile(`(?i)^(?:(?:re|fw|fwd)(?:\s*\[\d+\])?\s*:\s*|\[[^\]]+\]\s*)+`)

// NormalizeSubject implements the subject comparison suggested by RFC 8621
// section 3: strip reply/forward/list prefixes, lowercase, and ignore white
// space. It is shared by thread grouping and thread-scoped search filters.
func NormalizeSubject(value string) string {
	text := mailemail.DecodeHeader(value)
	for {
		trimmed := threadPrefixPattern.ReplaceAllString(strings.TrimSpace(text), "")
		if trimmed == text {
			break
		}
		text = trimmed
	}
	return strings.Join(strings.Fields(strings.ToLower(text)), "")
}

// ThreadHeaderIDs returns the sorted, normalized Message-ID values contributed
// by an email's Message-ID, In-Reply-To and References headers.
func ThreadHeaderIDs(msg *mailemail.UserEmail) []string {
	if msg == nil {
		return nil
	}
	set := make(map[string]bool)
	add := func(value string) {
		if normalized := NormalizeMessageID(value); normalized != "" {
			set[normalized] = true
		}
	}
	add(msg.MessageID)
	for _, value := range msg.InReplyTo {
		add(value)
	}
	for _, value := range msg.References {
		add(value)
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ThreadID is the fallback identifier for contexts that have only one email
// available (drafts awaiting storage, submission records created before the
// message is synced). Normal loaded emails use GroupThreads so replies share
// the same id.
func ThreadID(emailID string, msg *mailemail.UserEmail) string {
	return threadDigest(threadFallbackKey(emailID, msg))
}

func threadFallbackKey(emailID string, msg *mailemail.UserEmail) string {
	parts := []string{"email", NormalizeMessageID(emailID), "subject", ""}
	if msg == nil {
		return strings.Join(parts, "\x00")
	}
	parts[3] = NormalizeSubject(msg.Subject)
	ids := ThreadHeaderIDs(msg)
	for _, id := range ids {
		parts = append(parts, "id", id)
	}
	if len(ids) == 0 {
		parts = append(parts,
			"from", strings.ToLower(strings.TrimSpace(msg.From.Address)),
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
	return strings.Join(parts, "\x00")
}

func threadDigest(key string) string {
	sum := sha256.Sum256([]byte("thread\x00" + key))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// GroupThreads implements the threading model suggested by RFC 8621 section 3.
// Two emails join the same thread when they share a normalized Message-ID in
// any of Message-ID/In-Reply-To/References and their subjects match after
// NormalizeSubject. Transitive links are followed, so a reply-to-reply joins
// the original conversation. Emails without matching headers keep a distinct
// digest derived from their immutable email id and content.
func GroupThreads(ctx context.Context, deps *Deps, accountID string, emails []*mailemail.ListedEmail) ([]ThreadGroup, map[string]string, error) {
	type node struct {
		id         string
		subject    string
		fallback   string
		headerIDs  []string
		receivedAt time.Time
	}

	nodes := make([]node, 0, len(emails))
	for _, listed := range emails {
		if listed == nil || listed.Ref == nil || listed.Email == nil || listed.Ref.ID == "" {
			continue
		}
		receivedAt := listed.Email.Date
		if deps != nil && deps.Metadata != nil {
			receivedAt = ReceivedAtFromMetadata(EmailMetadata(ctx, deps, accountID, listed.Ref.ID), receivedAt)
		}
		nodes = append(nodes, node{
			id:         listed.Ref.ID,
			subject:    NormalizeSubject(listed.Email.Subject),
			fallback:   threadFallbackKey(listed.Ref.ID, listed.Email),
			headerIDs:  ThreadHeaderIDs(listed.Email),
			receivedAt: receivedAt.UTC(),
		})
	}

	parent := make([]int, len(nodes))
	size := make([]int, len(nodes))
	for i := range parent {
		parent[i], size[i] = i, 1
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if size[ra] < size[rb] {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		size[ra] += size[rb]
	}

	firstByKey := make(map[string]int)
	for i, current := range nodes {
		for _, id := range current.headerIDs {
			key := current.subject + "\x00" + id
			if prior, ok := firstByKey[key]; ok {
				union(prior, i)
				continue
			}
			firstByKey[key] = i
		}
	}

	components := make(map[int][]int)
	for i := range nodes {
		root := find(i)
		components[root] = append(components[root], i)
	}

	groups := make([]ThreadGroup, 0, len(components))
	byEmail := make(map[string]string, len(nodes))
	for _, indexes := range components {
		canonical := ""
		hasHeader := false
		emailIDs := make([]string, 0, len(indexes))
		for _, i := range indexes {
			current := nodes[i]
			emailIDs = append(emailIDs, current.id)
			for _, id := range current.headerIDs {
				if !hasHeader || id < canonical {
					canonical, hasHeader = id, true
				}
			}
		}
		if !hasHeader {
			for _, i := range indexes {
				if canonical == "" || nodes[i].fallback < canonical {
					canonical = nodes[i].fallback
				}
			}
		}
		id := threadDigest("group\x00" + nodes[indexes[0]].subject + "\x00" + canonical)
		type orderedEmail struct {
			id         string
			receivedAt time.Time
		}
		ordered := make([]orderedEmail, 0, len(indexes))
		for _, i := range indexes {
			ordered = append(ordered, orderedEmail{id: nodes[i].id, receivedAt: nodes[i].receivedAt})
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			if !ordered[i].receivedAt.Equal(ordered[j].receivedAt) {
				return ordered[i].receivedAt.Before(ordered[j].receivedAt)
			}
			return ordered[i].id < ordered[j].id
		})
		emailIDs = emailIDs[:0]
		for _, item := range ordered {
			emailIDs = append(emailIDs, item.id)
		}
		groups = append(groups, ThreadGroup{ID: id, EmailIDs: emailIDs})
		for _, emailID := range emailIDs {
			byEmail[emailID] = id
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, byEmail, nil
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
