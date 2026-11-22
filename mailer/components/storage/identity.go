package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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

// IdentityEntry is one JMAP Identity held by the account-wide collection.
// Unlike the legacy IdentityRecord it carries its immutable id and email and
// the server-owned mayDelete flag, so Identity/{get,set,changes} can be served
// without deriving a singleton from the user record on every request.
type IdentityEntry struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Email         string            `json:"email"`
	ReplyTo       []IdentityAddress `json:"replyTo"`
	Bcc           []IdentityAddress `json:"bcc"`
	TextSignature string            `json:"textSignature"`
	HTMLSignature string            `json:"htmlSignature"`
	MayDelete     bool              `json:"mayDelete"`
}

// NewIdentityID returns a stable, opaque JMAP id for a newly created Identity.
func NewIdentityID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return "id-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("id-%d", time.Now().UnixNano())
}

func identityCollectionKey(accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", errors.New("accountID is required")
	}
	return "\x00identities:" + accountID, nil
}

// IdentityCollectionStore persists an account's whole Identity collection as
// one revisioned JSON document. A single compare-and-swap therefore covers
// create, update and destroy, including the duplicate-email check, even when
// several Lambda invocations race.
type IdentityCollectionStore struct {
	states JMAPStateStore
	legacy *IdentityStore
}

// NewIdentityCollectionStore builds the collection store on top of the same
// durable state backend used by the other JMAP stateful stores.
func NewIdentityCollectionStore(states JMAPStateStore) *IdentityCollectionStore {
	return &IdentityCollectionStore{states: states, legacy: NewIdentityStore(states)}
}

// LoadLegacy reads the pre-multi-identity singleton overrides. It is used once
// to seed the "primary" entry for accounts that predate the collection.
func (s *IdentityCollectionStore) LoadLegacy(ctx context.Context, accountID string) (IdentityRecord, string, error) {
	return s.legacy.Load(ctx, accountID)
}

// Load returns the collection and its CAS revision. An account that has never
// migrated returns an empty map and an empty revision; callers seed it from
// LoadLegacy and the user record before the first Save.
func (s *IdentityCollectionStore) Load(ctx context.Context, accountID string) (map[string]IdentityEntry, string, error) {
	key, err := identityCollectionKey(accountID)
	if err != nil {
		return nil, "", err
	}
	data, revision, err := s.states.LoadJMAPState(ctx, key)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return map[string]IdentityEntry{}, "", nil
	}
	var entries map[string]IdentityEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, "", fmt.Errorf("decode identities for %q: %w", accountID, err)
	}
	if entries == nil {
		entries = map[string]IdentityEntry{}
	}
	return entries, revision, nil
}

// Save replaces the collection using compare-and-swap. The caller must have
// applied its mutation to the map returned by the immediately preceding Load.
func (s *IdentityCollectionStore) Save(ctx context.Context, accountID string, entries map[string]IdentityEntry, revision string) (string, error) {
	key, err := identityCollectionKey(accountID)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	next, err := s.states.SaveJMAPState(ctx, key, data, revision)
	if errors.Is(err, ErrJMAPStateConflict) {
		return "", ErrIdentityConflict
	}
	return next, err
}
