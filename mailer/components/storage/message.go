package storage

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/email"
)

// emailIDCandidateSet is the pure result of expanding a caller-supplied email
// identifier into the forms a backend may probe. Keys are physical delivered
// object keys (used by the filesystem and S3 stores); Tokens are ids used by
// the in-memory store. objectKey, when supplied, applies a backend-specific
// physical-key transformation such as the S3 bucket prefix.
type emailIDCandidateSet struct {
	AccountID string
	Canonical string
	Keys      []string
	Tokens    []string
}

// emailIDCandidates expands accountID/id into every identifier and physical key
// a backend may use. The canonical opaque id is always included first so the
// in-memory store can resolve ids returned by ListEmails. Legacy tokens and
// full delivered object keys are also accepted, with the physical token
// recovered from the canonical id so existing data remains addressable.
// mailboxes must already be in the backend's preferred (deterministic) order.
func emailIDCandidates(accountID, id string, objectKey func(string) string, mailboxes []string) (emailIDCandidateSet, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return emailIDCandidateSet{}, ErrNotFound
	}

	canonical, err := email.NormalizeEmailID(id)
	if err != nil {
		return emailIDCandidateSet{}, ErrNotFound
	}
	storageToken, err := email.EmailStorageToken(canonical)
	if err != nil {
		return emailIDCandidateSet{}, ErrNotFound
	}

	candidates := emailIDCandidateSet{AccountID: accountID, Canonical: canonical}
	seenTokens := make(map[string]struct{})
	addToken := func(token string) {
		token = strings.Trim(token, "/")
		if token == "" {
			return
		}
		if _, ok := seenTokens[token]; ok {
			return
		}
		seenTokens[token] = struct{}{}
		candidates.Tokens = append(candidates.Tokens, token)
	}
	addToken(canonical)
	addToken(storageToken)
	// Local development and tests may register ids directly with MemoryStorage
	// using their own (already URL-safe) identifiers.
	addToken(strings.Trim(strings.TrimSpace(id), "/"))

	// A full delivered key is a direct physical key and must belong to the
	// requested account. Canonical ids encode only the filename token, so the
	// mailbox scan below is used for those.
	original := strings.Trim(strings.TrimSpace(id), "/")
	if email.IsDeliveredMessageKey(original) {
		owner, _, ok := email.MailboxFromID(original)
		if !ok || !strings.EqualFold(owner, accountID) {
			return emailIDCandidateSet{}, ErrNotFound
		}
		candidates.Keys = append(candidates.Keys, original)
		if objectKey != nil {
			if prefixed := objectKey(original); prefixed != original {
				candidates.Keys = append(candidates.Keys, prefixed)
			}
		}
		return candidates, nil
	}

	for _, mailboxID := range mailboxes {
		key := email.DeliveredMessageKey(accountID, mailboxID, storageToken)
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
// string before handing it to the parser. email.Parse ignores its id argument;
// the ref below is the only place identity is attached.
func listedEmailFromRaw(id string, raw []byte) (*email.ListedEmail, error) {
	parsed, err := email.Parse(id, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse email %q: %w", id, err)
	}
	ref := email.EmailRefFromID(id)
	return &email.ListedEmail{Ref: &ref, Email: parsed}, nil
}

// newDeliveredToken builds the stable physical token used by filesystem and S3
// deliveries and returns the bytes that should be stored for the message. The
// token includes its .eml suffix but is encoded to an opaque id before it is
// returned to callers; nothing about the storage layout reaches clients.
func newDeliveredToken(message *email.UserEmail, raw []byte) (string, []byte, error) {
	token, err := newMessageToken()
	if err != nil {
		return "", nil, err
	}
	if raw == nil {
		raw = email.RawFromUserEmail(message)
	}
	return token, raw, nil
}

// newMessageToken returns a stable physical token for a stored message. It is
// a plain string (not yet an opaque id) so EncodeID/DecodeID can reversibly map
// it to the URL-safe id exposed to clients. The .eml suffix is part of the
// physical filename and never appears in a JMAP id.
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
