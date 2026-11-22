package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// IdentityRecord holds the client-settable properties of the single Identity
// every account has. The email address itself comes from the user record and is
// never stored here, so an address change cannot leave a stale identity behind.
type IdentityRecord struct {
	Name          string            `json:"name"`
	ReplyTo       []IdentityAddress `json:"replyTo"`
	Bcc           []IdentityAddress `json:"bcc"`
	TextSignature string            `json:"textSignature"`
	HTMLSignature string            `json:"htmlSignature"`
}

// IdentityAddress is the storage view of a JMAP EmailAddress. Name is decoded
// by the JMAP layer; storage only carries the two RFC 5322 fields.
type IdentityAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ErrIdentityConflict reports a concurrent update to the singleton identity.
var ErrIdentityConflict = errors.New("identity state changed concurrently")

func identityStateKey(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	return "\x00identity:" + accountID, nil
}

// IdentityStore persists the account's singleton Identity as a revisioned JSON
// document. Like SubmissionStore it reuses JMAPStateStore, so file, S3 and
// memory backends all get the same conditional-write contract on Lambda.
type IdentityStore struct {
	states JMAPStateStore
}

func NewIdentityStore(states JMAPStateStore) *IdentityStore {
	return &IdentityStore{states: states}
}

// Load returns the stored identity overrides. An account that has never called
// Identity/set returns an empty record and an empty revision.
func (s *IdentityStore) Load(ctx context.Context, accountID string) (IdentityRecord, string, error) {
	key, err := identityStateKey(accountID)
	if err != nil {
		return IdentityRecord{}, "", err
	}
	data, revision, err := s.states.LoadJMAPState(ctx, key)
	if err != nil {
		return IdentityRecord{}, "", err
	}
	if len(data) == 0 {
		return IdentityRecord{}, "", nil
	}
	var record IdentityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return IdentityRecord{}, "", fmt.Errorf("decode identity for %q: %w", accountID, err)
	}
	return record, revision, nil
}

// Save replaces the stored overrides using compare-and-swap.
func (s *IdentityStore) Save(ctx context.Context, accountID string, record IdentityRecord, revision string) (string, error) {
	key, err := identityStateKey(accountID)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	next, err := s.states.SaveJMAPState(ctx, key, data, revision)
	if errors.Is(err, ErrJMAPStateConflict) {
		return "", ErrIdentityConflict
	}
	return next, err
}
