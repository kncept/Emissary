package usermanager

import (
	"context"
	"strings"
	"sync"
	"time"
)

// VacationResponseInterval is the RFC 3834 recommended minimum interval
// between automatic replies to the same sender.
const VacationResponseInterval = 7 * 24 * time.Hour

// VacationResponse holds the JMAP VacationResponse singleton settings for a
// user together with the small amount of state needed to avoid auto-reply
// loops. It is embedded directly in EmissaryUser, and therefore in user.json,
// so file, S3 and DynamoDB backends all persist the settings with the account
// itself rather than in a second record.
type VacationResponse struct {
	IsEnabled bool       `json:"IsEnabled,omitempty"`
	FromDate  *time.Time `json:"FromDate,omitempty"`
	ToDate    *time.Time `json:"ToDate,omitempty"`
	Subject   *string    `json:"Subject,omitempty"`
	TextBody  *string    `json:"TextBody,omitempty"`
	HTMLBody  *string    `json:"HTMLBody,omitempty"`

	// Responded records when a reply was last sent to a given sender. Keys are
	// normalised (lower-case) addresses. It is deliberately not a JMAP
	// property: clients only ever see the six settings above.
	Responded map[string]time.Time `json:"Responded,omitempty"`
}

// clone returns an independent copy so callers cannot mutate stored state by
// editing a pointer or map returned from a backend.
func (v VacationResponse) clone() VacationResponse {
	copy := v
	if v.FromDate != nil {
		t := *v.FromDate
		copy.FromDate = &t
	}
	if v.ToDate != nil {
		t := *v.ToDate
		copy.ToDate = &t
	}
	if v.Subject != nil {
		s := *v.Subject
		copy.Subject = &s
	}
	if v.TextBody != nil {
		s := *v.TextBody
		copy.TextBody = &s
	}
	if v.HTMLBody != nil {
		s := *v.HTMLBody
		copy.HTMLBody = &s
	}
	if v.Responded != nil {
		copy.Responded = make(map[string]time.Time, len(v.Responded))
		for address, when := range v.Responded {
			copy.Responded[address] = when
		}
	}
	return copy
}

// ActiveAt reports whether the vacation response is enabled and now falls in
// the configured [fromDate, toDate) window.
func (v VacationResponse) ActiveAt(now time.Time) bool {
	if !v.IsEnabled {
		return false
	}
	if v.FromDate != nil && now.Before(*v.FromDate) {
		return false
	}
	if v.ToDate != nil && !now.Before(*v.ToDate) {
		return false
	}
	return true
}

// RecentlyResponded reports whether an automatic reply was sent to sender
// within interval. Stale entries are ignored (and removed) so the persisted
// map cannot grow without bound for one-off correspondents.
func (v *VacationResponse) RecentlyResponded(sender string, now time.Time, interval time.Duration) bool {
	address := NormalizeVacationSender(sender)
	if address == "" || len(v.Responded) == 0 {
		return false
	}
	when, ok := v.Responded[address]
	if !ok {
		return false
	}
	if now.Sub(when) >= interval {
		delete(v.Responded, address)
		return false
	}
	return true
}

// MarkResponded records an automatic reply to sender at now.
func (v *VacationResponse) MarkResponded(sender string, now time.Time) {
	address := NormalizeVacationSender(sender)
	if address == "" {
		return
	}
	if v.Responded == nil {
		v.Responded = make(map[string]time.Time)
	}
	v.Responded[address] = now.UTC()
}

// NormalizeVacationSender reduces a reply destination to a comparable address.
func NormalizeVacationSender(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	if parsed, ok := normalizeAddress(address); ok {
		return parsed
	}
	return address
}

// VacationStore is the storage seam used by JMAP VacationResponse methods and
// by the inbound receiver. Emissary-mastered managers implement it by reading
// and writing the account's user.json record.
type VacationStore interface {
	GetVacationResponse(ctx context.Context, email string) (VacationResponse, error)
	SaveVacationResponse(ctx context.Context, email string, vacation VacationResponse) error
}

var (
	_ VacationStore = (*FileManager)(nil)
	_ VacationStore = (*S3Manager)(nil)
	_ VacationStore = (*DdbManager)(nil)
	_ VacationStore = (*MemoryVacationStore)(nil)
)

// VacationStoreFor returns the durable store for manager. Managers that do not
// own a user store (the administrator-only "none" backend, LDAP and test
// doubles) get a process-local store so the singleton still behaves correctly;
// settings cannot survive a restart for those backends.
func VacationStoreFor(manager UserManager) VacationStore {
	if manager == nil {
		return NewMemoryVacationStore()
	}
	if store, ok := manager.(VacationStore); ok {
		return store
	}
	return NewMemoryVacationStore()
}

func (m *FileManager) GetVacationResponse(ctx context.Context, email string) (VacationResponse, error) {
	user, err := m.Get(ctx, email)
	if err != nil {
		return VacationResponse{}, err
	}
	return user.VacationResponse.clone(), nil
}

func (m *FileManager) SaveVacationResponse(ctx context.Context, email string, vacation VacationResponse) error {
	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	user.VacationResponse = vacation.clone()
	return m.Save(ctx, user)
}

func (m *S3Manager) GetVacationResponse(ctx context.Context, email string) (VacationResponse, error) {
	user, err := m.Get(ctx, email)
	if err != nil {
		return VacationResponse{}, err
	}
	return user.VacationResponse.clone(), nil
}

func (m *S3Manager) SaveVacationResponse(ctx context.Context, email string, vacation VacationResponse) error {
	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	user.VacationResponse = vacation.clone()
	return m.Save(ctx, user)
}

func (m *DdbManager) GetVacationResponse(ctx context.Context, email string) (VacationResponse, error) {
	user, err := m.Get(ctx, email)
	if err != nil {
		return VacationResponse{}, err
	}
	return user.VacationResponse.clone(), nil
}

func (m *DdbManager) SaveVacationResponse(ctx context.Context, email string, vacation VacationResponse) error {
	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	user.VacationResponse = vacation.clone()
	return m.Save(ctx, user)
}

// MemoryVacationStore is a process-local VacationStore. It is used for user
// managers without an Emissary-mastered store and in tests.
type MemoryVacationStore struct {
	mu      sync.RWMutex
	entries map[string]VacationResponse
}

func NewMemoryVacationStore() *MemoryVacationStore {
	return &MemoryVacationStore{entries: make(map[string]VacationResponse)}
}

func (s *MemoryVacationStore) GetVacationResponse(_ context.Context, email string) (VacationResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := NormalizeVacationSender(email)
	return s.entries[key].clone(), nil
}

func (s *MemoryVacationStore) SaveVacationResponse(_ context.Context, email string, vacation VacationResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]VacationResponse)
	}
	s.entries[NormalizeVacationSender(email)] = vacation.clone()
	return nil
}
