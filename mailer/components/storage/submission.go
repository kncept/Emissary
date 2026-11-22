package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SubmissionAddress is the storage view of the JMAP Envelope Address object.
// Parameters are optional and individual values may be null.
type SubmissionAddress struct {
	Email      string             `json:"email"`
	Parameters map[string]*string `json:"parameters"`
}

// SubmissionEnvelope is the SMTP envelope recorded for a submission. Unlike the
// message headers, rcptTo includes every To, Cc and Bcc recipient.
type SubmissionEnvelope struct {
	MailFrom SubmissionAddress   `json:"mailFrom"`
	RcptTo   []SubmissionAddress `json:"rcptTo"`
}

// SubmissionDelivery is the per-recipient delivery status of a submission. The
// values are the RFC 8621 enums ("queued"/"yes"/"no"/"unknown" and
// "unknown"/"yes"), kept as strings so the storage layer is not coupled to a
// JMAP schema package.
type SubmissionDelivery struct {
	SMTPReply string `json:"smtpReply"`
	Delivered string `json:"delivered"`
	Displayed string `json:"displayed"`
}

// SubmissionRecord holds every server-set EmailSubmission property. It is
// written before the message is handed to the sender, so a state-write failure
// can never turn a successful send into an error reported to the client.
type SubmissionRecord struct {
	ID             string                        `json:"id"`
	AccountID      string                        `json:"accountId"`
	IdentityID     string                        `json:"identityId"`
	EmailID        string                        `json:"emailId"`
	ThreadID       string                        `json:"threadId"`
	Envelope       SubmissionEnvelope            `json:"envelope"`
	SendAt         string                        `json:"sendAt"`
	UndoStatus     string                        `json:"undoStatus"`
	DeliveryStatus map[string]SubmissionDelivery `json:"deliveryStatus"`
	DSNBlobIDs     []string                      `json:"dsnBlobIds"`
	MDNBlobIDs     []string                      `json:"mdnBlobIds"`

	// Relayed is an internal send-completion marker, not a JMAP property. A
	// record written before Send() is retried if the process dies mid-send;
	// once Relayed is true a repeated creation id is an idempotent success.
	Relayed bool `json:"relayed,omitempty"`
}

// ErrSubmissionConflict reports that another invocation changed the account's
// submission collection between a load and the matching save. Callers reload,
// re-apply their change and retry, just as they do for JMAP state tokens.
var ErrSubmissionConflict = errors.New("submission state changed concurrently")

// submissionStateKey namespaces submission records away from the change
// tracker's own state blob. EncodeID keeps the account id a single safe object
// key component even for vendor ids containing slashes.
func submissionStateKey(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	return "\x00submission:" + accountID, nil
}

// SubmissionStore persists the submission objects of one account as a single
// revisioned JSON document. It reuses JMAPStateStore, so the file, S3 and
// memory backends all inherit the same conditional-write contract and Lambda
// cold-start safety without a second concurrency scheme.
type SubmissionStore struct {
	states JMAPStateStore
}

func NewSubmissionStore(states JMAPStateStore) *SubmissionStore {
	return &SubmissionStore{states: states}
}

type submissionCollection struct {
	Records map[string]SubmissionRecord `json:"records"`
}

// Load returns all submissions keyed by id plus the revision needed to save a
// change. An account with no submissions returns an empty map and an empty
// revision.
func (s *SubmissionStore) Load(ctx context.Context, accountID string) (map[string]SubmissionRecord, string, error) {
	key, err := submissionStateKey(accountID)
	if err != nil {
		return nil, "", err
	}
	data, revision, err := s.states.LoadJMAPState(ctx, key)
	if err != nil {
		return nil, "", err
	}
	records := make(map[string]SubmissionRecord)
	if len(data) == 0 {
		return records, "", nil
	}
	var stored submissionCollection
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, "", fmt.Errorf("decode submissions for %q: %w", accountID, err)
	}
	for id, record := range stored.Records {
		records[id] = record
	}
	return records, revision, nil
}

// Save replaces the account's whole collection using compare-and-swap. Every
// record must carry the requested account id and an id matching its map key so
// a caller cannot write a submission into another account's namespace.
func (s *SubmissionStore) Save(ctx context.Context, accountID string, records map[string]SubmissionRecord, revision string) (string, error) {
	key, err := submissionStateKey(accountID)
	if err != nil {
		return "", err
	}
	for id, record := range records {
		if record.ID != id {
			return "", fmt.Errorf("submission id %q does not match record id %q", id, record.ID)
		}
		if !strings.EqualFold(record.AccountID, accountID) {
			return "", fmt.Errorf("submission %q belongs to account %q, not %q", id, record.AccountID, accountID)
		}
	}
	data, err := json.Marshal(submissionCollection{Records: records})
	if err != nil {
		return "", err
	}
	newRevision, err := s.states.SaveJMAPState(ctx, key, data, revision)
	if errors.Is(err, ErrJMAPStateConflict) {
		return "", ErrSubmissionConflict
	}
	return newRevision, err
}
