package storage

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

// emailIDCandidateSet is the pure result of expanding a caller-supplied email
// identifier into the forms a backend may probe. Keys are physical delivered
// object keys (used by the filesystem and S3 stores); tokens are stable ids
// (used by the in-memory store). objectKey, when supplied, applies a
// backend-specific physical-key transformation such as the S3 bucket prefix.
type emailIDCandidateSet struct {
	AccountID string
	Keys      []string
	Tokens    []string
}

// emailIDCandidates expands accountID/id into every candidate the backends
// historically accepted: an unprefixed full delivered key, the same key under
// the store prefix, or a stable token searched through the supplied mailboxes.
// mailboxes must already be in the backend's preferred (deterministic) order.
func emailIDCandidates(accountID, id string, objectKey func(string) string, mailboxes []string) (emailIDCandidateSet, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return emailIDCandidateSet{}, ErrNotFound
	}
	id = strings.TrimPrefix(strings.TrimSpace(id), "/")
	if id == "" {
		return emailIDCandidateSet{}, ErrNotFound
	}

	candidates := emailIDCandidateSet{AccountID: accountID}
	if email.IsDeliveredMessageKey(id) {
		owner, _, ok := email.MailboxFromID(id)
		if !ok || !strings.EqualFold(owner, accountID) {
			return emailIDCandidateSet{}, ErrNotFound
		}
		candidates.Keys = append(candidates.Keys, id)
		if objectKey != nil {
			if prefixed := objectKey(id); prefixed != id {
				candidates.Keys = append(candidates.Keys, prefixed)
			}
		}
		if token, ok := email.DeliveredMessageToken(id); ok && token != "" {
			candidates.Tokens = append(candidates.Tokens, token)
		}
		return candidates, nil
	}

	// A stable token is always a valid in-memory key, even when it does not
	// follow the delivered-message naming scheme.
	candidates.Tokens = append(candidates.Tokens, id)
	for _, mailboxID := range mailboxes {
		key := email.DeliveredMessageKey(accountID, mailboxID, id)
		if key == "" {
			continue
		}
		if objectKey != nil {
			key = objectKey(key)
		}
		candidates.Keys = append(candidates.Keys, key)
	}
	return candidates, nil
}

// listedEmailFromRaw parses stored wire bytes into the listed form returned by
// every storage backend. Using bytes.NewReader avoids copying raw into a
// string before handing it to the parser.
func listedEmailFromRaw(id string, raw []byte) (*email.ListedEmail, error) {
	parsed, err := email.Parse(id, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email %q: %w", id, err)
	}
	ref := email.EmailRefFromID(id)
	return &email.ListedEmail{Ref: &ref, Email: parsed}, nil
}

// newDeliveredToken builds the stable token used by filesystem and S3
// deliveries and returns the bytes that should be stored for the message.
func newDeliveredToken(message *email.UserEmail, raw []byte) (string, []byte, error) {
	idToken, err := newMessageToken()
	if err != nil {
		return "", nil, err
	}
	timestamp := message.Date
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	token := fmt.Sprintf("%s_%s", email.FormatDeliveredTimestamp(timestamp), idToken)
	if raw == nil {
		raw = email.RawFromUserEmail(message)
	}
	return token, raw, nil
}

// newMessageToken returns a stable id token for a stored message.
func newMessageToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate email id: %w", err)
	}
	return fmt.Sprintf("%x.eml", b), nil
}

// SortListedEmails orders messages by Email.Date descending, with Ref.ID
// descending as the deterministic tie-breaker. All storage backends use this
// contract; the filesystem store previously used object-key order.
func SortListedEmails(emails []*email.ListedEmail) {
	sort.Slice(emails, func(i, j int) bool {
		if emails[i].Email.Date.Equal(emails[j].Email.Date) {
			return emails[i].Ref.ID > emails[j].Ref.ID
		}
		return emails[i].Email.Date.After(emails[j].Email.Date)
	})
}
