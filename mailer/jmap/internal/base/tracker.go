package base

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/components/storage"
)

// StateKind is one independently versioned set of JMAP records. RFC 8620
// section 5.1 requires a type's state string to change whenever any record of
// that type changes and to stay the same when it does not, so each kind has its
// own monotonic counter rather than a shared wall-clock timestamp.
type StateKind string

const (
	StateEmail        StateKind = "email"
	StateEmailQuery   StateKind = "email-query"
	StateMailbox      StateKind = "mailbox"
	StateMailboxQuery StateKind = "mailbox-query"
	StateThread       StateKind = "thread"
	StateIdentity     StateKind = "identity"
	StateSubmission   StateKind = "submission"
	StateVacation     StateKind = "vacation"

	// maxChangeEntries bounds the persisted delta log. Callers whose state is
	// older than the retained window get cannotCalculateChanges, which RFC
	// 8620 section 5.2 explicitly permits when intermediate states cannot be
	// produced.
	maxChangeEntries = 10000

	// maxQueryStates bounds how many historical query result sets are kept so
	// a client can call queryChanges from a recently returned queryState.
	maxQueryStates = 256

	// stateSaveRetries bounds optimistic-concurrency retries. Each Lambda
	// invocation caches the state it loaded, so a conflict means another
	// invocation changed the same account; reloading and retrying keeps the
	// counter monotonic instead of letting a stale writer win.
	stateSaveRetries = 8
)

// ChangeOp is the kind of modification applied to a record.
type ChangeOp uint8

const (
	ChangeCreate ChangeOp = iota
	ChangeUpdate
	ChangeDestroy
)

type changeEntry struct {
	Seq uint64   `json:"seq"`
	ID  string   `json:"id"`
	Op  ChangeOp `json:"op"`
}

// queryTracker is the durable state for one query signature (filter plus
// sort). Each signature has its own counter so two queries over the same type
// can evolve independently. Snapshots hold the ordered result set for every
// recently issued queryState token.
type queryTracker struct {
	Version   uint64              `json:"version"`
	Snapshots map[uint64][]string `json:"snapshots"`
}

func newQueryTracker() queryTracker {
	return queryTracker{Snapshots: make(map[uint64][]string)}
}

func (q *queryTracker) normalize() {
	if q.Snapshots == nil {
		q.Snapshots = make(map[uint64][]string)
	}
}

// trackerState is the durable representation of an account's change history.
// Fingerprints are persisted with the counters so a restart can still tell
// whether data changed while the server was down.
type trackerState struct {
	Versions     map[StateKind]uint64                  `json:"versions"`
	Fingerprints map[StateKind]map[string]string       `json:"fingerprints"`
	Logs         map[StateKind][]changeEntry           `json:"logs"`
	Queries      map[StateKind]map[string]queryTracker `json:"queries,omitempty"`
}

func newTrackerState() trackerState {
	return trackerState{
		Versions:     make(map[StateKind]uint64),
		Fingerprints: make(map[StateKind]map[string]string),
		Logs:         make(map[StateKind][]changeEntry),
		Queries:      make(map[StateKind]map[string]queryTracker),
	}
}

func (s *trackerState) normalize() {
	if s.Versions == nil {
		s.Versions = make(map[StateKind]uint64)
	}
	if s.Fingerprints == nil {
		s.Fingerprints = make(map[StateKind]map[string]string)
	}
	if s.Logs == nil {
		s.Logs = make(map[StateKind][]changeEntry)
	}
	if s.Queries == nil {
		s.Queries = make(map[StateKind]map[string]queryTracker)
	}
	for kind, queries := range s.Queries {
		for key, query := range queries {
			query.normalize()
			queries[key] = query
			s.Queries[kind] = queries
		}
	}
}

func cloneTrackerState(source trackerState) trackerState {
	copy := newTrackerState()
	for kind, version := range source.Versions {
		copy.Versions[kind] = version
	}
	for kind, fingerprints := range source.Fingerprints {
		copy.Fingerprints[kind] = CopySnapshot(fingerprints)
	}
	for kind, log := range source.Logs {
		copy.Logs[kind] = append([]changeEntry(nil), log...)
	}
	for kind, queries := range source.Queries {
		copy.Queries[kind] = make(map[string]queryTracker, len(queries))
		for key, query := range queries {
			cloned := newQueryTracker()
			cloned.Version = query.Version
			for version, ids := range query.Snapshots {
				cloned.Snapshots[version] = append([]string(nil), ids...)
			}
			copy.Queries[kind][key] = cloned
		}
	}
	return copy
}

// trackerAccount caches the loaded state for the lifetime of a process (one
// Lambda invocation, or a long-running server instance).
type trackerAccount struct {
	state    trackerState
	revision string
	loaded   bool
}

// Tracker records per-account, per-type change deltas in a durable store.
// Deltas are derived by diffing snapshots of the underlying stores rather
// than by instrumenting writes, so changes made outside JMAP (inbound
// delivery, the support API) are visible to /changes too.
//
// State counters, fingerprints, logs and query result sets are persisted via
// storage.JMAPStateStore. A new process therefore resumes the same tokens,
// and optimistic-concurrency retries prevent a stale writer from regressing
// a counter when several invocations handle one account at once.
type Tracker struct {
	mu       sync.Mutex
	store    storage.JMAPStateStore
	accounts map[string]*trackerAccount
}

// NewTracker creates a change tracker backed by store.
func NewTracker(store storage.JMAPStateStore) *Tracker {
	return &Tracker{store: store, accounts: make(map[string]*trackerAccount)}
}

func (t *Tracker) accountLocked(accountID string) *trackerAccount {
	account, ok := t.accounts[accountID]
	if !ok {
		account = &trackerAccount{}
		t.accounts[accountID] = account
	}
	return account
}

// loadLocked reads an account's state once per process. It must be called
// with t.mu held.
func (t *Tracker) loadLocked(ctx context.Context, accountID string) (*trackerAccount, error) {
	account := t.accountLocked(accountID)
	if account.loaded {
		return account, nil
	}
	if t.store == nil {
		return nil, errors.New("JMAP state store is not configured")
	}
	data, revision, err := t.store.LoadJMAPState(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("load JMAP state for %q: %w", accountID, err)
	}
	state := newTrackerState()
	if len(data) > 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("decode JMAP state for %q: %w", accountID, err)
		}
	}
	state.normalize()
	account.state = state
	account.revision = revision
	account.loaded = true
	return account, nil
}

// saveLocked attempts an optimistic write of one account's state. It must be
// called with t.mu held. On a conflict the cached state is invalidated so the
// caller reloads the newer version before retrying.
func (t *Tracker) saveLocked(ctx context.Context, accountID string, account *trackerAccount, next trackerState) error {
	data, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode JMAP state for %q: %w", accountID, err)
	}
	revision, err := t.store.SaveJMAPState(ctx, accountID, data, account.revision)
	if err != nil {
		if errors.Is(err, storage.ErrJMAPStateConflict) {
			account.loaded = false
		}
		return err
	}
	account.state = next
	account.revision = revision
	account.loaded = true
	return nil
}

// mutate applies a state change with optimistic retries. mutators must be
// pure with respect to the supplied state: a failed write discards it.
func (t *Tracker) mutate(ctx context.Context, accountID string, mutate func(*trackerState) (bool, error)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < stateSaveRetries; attempt++ {
		account, err := t.loadLocked(ctx, accountID)
		if err != nil {
			return err
		}
		next := cloneTrackerState(account.state)
		changed, err := mutate(&next)
		if err != nil || !changed {
			return err
		}
		lastErr = t.saveLocked(ctx, accountID, account, next)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, storage.ErrJMAPStateConflict) {
			return lastErr
		}
	}
	return fmt.Errorf("persist JMAP state for %q after %d retries: %w", accountID, stateSaveRetries, lastErr)
}

func stateToken(kind StateKind, version uint64) string {
	return fmt.Sprintf("%s-%d", kind, version)
}

func parseStateToken(state string, kind StateKind) (uint64, bool) {
	prefix := string(kind) + "-"
	if !strings.HasPrefix(state, prefix) {
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimPrefix(state, prefix), 10, 64)
	return version, err == nil
}

// queryStateToken includes the query signature hash so a token issued for one
// filter/sort combination can never be mistaken for another query's token.
func queryStateToken(kind StateKind, queryKey string, version uint64) string {
	if queryKey == "" {
		return stateToken(kind, version)
	}
	return fmt.Sprintf("%s-%d.%s", kind, version, queryKey)
}

// parseQueryStateToken accepts tokens with and without a query signature. A
// token without one is a legacy process-local token; its result set was never
// persisted, so queryChanges reports canCalculateChanges=false for it.
func parseQueryStateToken(state string, kind StateKind) (uint64, string, bool) {
	prefix := string(kind) + "-"
	if !strings.HasPrefix(state, prefix) {
		return 0, "", false
	}
	versionPart, queryKey, _ := strings.Cut(strings.TrimPrefix(state, prefix), ".")
	version, err := strconv.ParseUint(versionPart, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return version, queryKey, true
}

func queryTrackerFor(state *trackerState, kind StateKind, queryKey string) queryTracker {
	if state.Queries[kind] == nil {
		state.Queries[kind] = make(map[string]queryTracker)
	}
	query, ok := state.Queries[kind][queryKey]
	if !ok {
		query = newQueryTracker()
	}
	query.normalize()
	return query
}

// recordQuerySnapshot stores currentIDs, advancing the query version only when
// the result set changed. The first observation establishes a baseline at the
// current version without advancing it.
func recordQuerySnapshot(query *queryTracker, ids []string) (uint64, bool) {
	version := query.Version
	if previous, ok := query.Snapshots[version]; ok && slices.Equal(previous, ids) {
		return version, false
	}
	if _, seen := query.Snapshots[version]; seen {
		version++
	}
	query.Version = version
	query.Snapshots[version] = append([]string(nil), ids...)
	trimQuerySnapshots(query)
	return version, true
}

func trimQuerySnapshots(query *queryTracker) {
	versions := make([]uint64, 0, len(query.Snapshots))
	for version := range query.Snapshots {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for len(versions) > maxQueryStates {
		delete(query.Snapshots, versions[0])
		versions = versions[1:]
	}
}

// State returns the current state token for an account and type.
func (t *Tracker) State(ctx context.Context, accountID string, kind StateKind) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	account, err := t.loadLocked(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateToken(kind, account.state.Versions[kind]), nil
}

// Sync compares new snapshots with the previous ones and records one change
// entry per differing record. The first call for a kind establishes the
// baseline without reporting changes, since there is no older state to diff
// against.
func (t *Tracker) Sync(ctx context.Context, accountID string, snapshots map[StateKind]map[string]string) error {
	return t.mutate(ctx, accountID, func(state *trackerState) (bool, error) {
		changed := false
		for kind, current := range snapshots {
			previous, seen := state.Fingerprints[kind]
			if !seen {
				// Persist the baseline so the next observation can be diffed,
				// but do not advance the counter: no data change was observed.
				state.Fingerprints[kind] = CopySnapshot(current)
				changed = true
				continue
			}
			diff := diffSnapshots(previous, current)
			if len(diff) == 0 {
				continue
			}
			for _, change := range diff {
				state.Versions[kind]++
				appendChange(state, kind, changeEntry{
					Seq: state.Versions[kind],
					ID:  change.ID,
					Op:  change.Op,
				})
			}
			state.Fingerprints[kind] = CopySnapshot(current)
			changed = true
		}
		return changed, nil
	})
}

// Record appends an explicit change, used for data that has no snapshot to
// diff (for example a newly created EmailSubmission).
func (t *Tracker) Record(ctx context.Context, accountID string, kind StateKind, id string, op ChangeOp) error {
	return t.mutate(ctx, accountID, func(state *trackerState) (bool, error) {
		state.Versions[kind]++
		appendChange(state, kind, changeEntry{Seq: state.Versions[kind], ID: id, Op: op})
		return true, nil
	})
}

// SyncQuery records the ordered result set of one query signature. The query
// state only changes when that result set changes, so an update to a record
// excluded by the filter does not alter this query's state.
func (t *Tracker) SyncQuery(ctx context.Context, accountID string, kind StateKind, queryKey string, ids []string) (string, error) {
	var token string
	err := t.mutate(ctx, accountID, func(state *trackerState) (bool, error) {
		query := queryTrackerFor(state, kind, queryKey)
		version, changed := recordQuerySnapshot(&query, ids)
		if changed {
			state.Queries[kind][queryKey] = query
		}
		token = queryStateToken(kind, queryKey, version)
		return changed, nil
	})
	return token, err
}

func appendChange(state *trackerState, kind StateKind, entry changeEntry) {
	log := append(state.Logs[kind], entry)
	if len(log) > maxChangeEntries {
		log = append([]changeEntry(nil), log[len(log)-maxChangeEntries:]...)
	}
	state.Logs[kind] = log
}

// CopySnapshot makes an independent copy of a fingerprint map so later
// mutations of the source do not corrupt the tracker's history.
func CopySnapshot(snapshot map[string]string) map[string]string {
	copied := make(map[string]string, len(snapshot))
	for id, fingerprint := range snapshot {
		copied[id] = fingerprint
	}
	return copied
}

func diffSnapshots(previous, current map[string]string) []changeEntry {
	var diff []changeEntry
	for id, fingerprint := range current {
		old, exists := previous[id]
		switch {
		case !exists:
			diff = append(diff, changeEntry{ID: id, Op: ChangeCreate})
		case old != fingerprint:
			diff = append(diff, changeEntry{ID: id, Op: ChangeUpdate})
		}
	}
	for id := range previous {
		if _, exists := current[id]; !exists {
			diff = append(diff, changeEntry{ID: id, Op: ChangeDestroy})
		}
	}
	sort.Slice(diff, func(i, j int) bool {
		if diff[i].ID != diff[j].ID {
			return diff[i].ID < diff[j].ID
		}
		return diff[i].Op < diff[j].Op
	})
	return diff
}

type methodError struct {
	typ    string
	detail string
}

func (e *methodError) Error() string { return e.typ + ": " + e.detail }

// Errorf builds a JMAP method error whose type is the prefix used by the
// dispatcher to select the error response type.
func Errorf(typ, format string, args ...any) error {
	return &methodError{typ: typ, detail: fmt.Sprintf(format, args...)}
}

// aggregatedChange is the effective change for a record after applying RFC
// 8620 section 5.2's coalescing rules to every entry in the delta window.
// finalSeq is the sequence of the last raw change that contributed to it, so
// a paginated /changes response can return an intermediate state without
// losing later changes to the same record.
type aggregatedChange struct {
	id       string
	op       ChangeOp
	finalSeq uint64
}

// coalesceChanges folds the delta window into one effective change per id.
// Coalescing must finish before maxChanges is applied: a created record that
// is later destroyed contributes no change at all, even if an unrelated
// create temporarily pushed the projected count above the limit.
func coalesceChanges(log []changeEntry, since uint64) []aggregatedChange {
	byID := make(map[string]aggregatedChange)
	for _, entry := range log {
		if entry.Seq <= since {
			continue
		}
		current, exists := byID[entry.ID]
		if !exists {
			byID[entry.ID] = aggregatedChange{id: entry.ID, op: entry.Op, finalSeq: entry.Seq}
			continue
		}
		if entry.Op == ChangeDestroy && current.op == ChangeCreate {
			// created+destroyed SHOULD be omitted entirely.
			delete(byID, entry.ID)
			continue
		}
		switch entry.Op {
		case ChangeCreate:
			// destroyed+created is a new record; other prior ops stay as-is.
			if current.op == ChangeDestroy {
				current.op = ChangeCreate
			}
		case ChangeDestroy:
			// updated+destroyed appears only in destroyed.
			current.op = ChangeDestroy
		}
		current.finalSeq = entry.Seq
		byID[entry.ID] = current
	}

	changes := make([]aggregatedChange, 0, len(byID))
	for _, change := range byID {
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].finalSeq != changes[j].finalSeq {
			return changes[i].finalSeq < changes[j].finalSeq
		}
		return changes[i].id < changes[j].id
	})
	return changes
}

// ChangesFrom builds a /changes response. It validates sinceState, coalesces
// repeated changes to the same id, honours maxChanges and returns an
// intermediate state token when more changes remain.
func (t *Tracker) ChangesFrom(ctx context.Context, accountID string, kind StateKind, sinceState string, maxChanges *int) (map[string]any, error) {
	if maxChanges != nil && *maxChanges <= 0 {
		return nil, Errorf("invalidArguments", "maxChanges must be greater than zero")
	}
	old, ok := parseStateToken(sinceState, kind)
	if !ok {
		return nil, Errorf("cannotCalculateChanges", "unrecognised state %q", sinceState)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	account, err := t.loadLocked(ctx, accountID)
	if err != nil {
		return nil, err
	}
	state := account.state
	current := state.Versions[kind]
	if old > current {
		return nil, Errorf("cannotCalculateChanges", "state %q is newer than the current state", sinceState)
	}
	log := state.Logs[kind]
	if len(log) > 0 && old < log[0].Seq-1 {
		return nil, Errorf("cannotCalculateChanges", "state %q is older than the retained change log", sinceState)
	}

	order := coalesceChanges(log, old)
	hasMore := false
	newState := stateToken(kind, current)
	if maxChanges != nil && len(order) > *maxChanges {
		hasMore = true
		order = order[:*maxChanges]
		newState = stateToken(kind, order[len(order)-1].finalSeq)
	}

	created, updated, destroyed := []string{}, []string{}, []string{}
	for _, change := range order {
		switch change.op {
		case ChangeCreate:
			created = append(created, change.id)
		case ChangeUpdate:
			updated = append(updated, change.id)
		case ChangeDestroy:
			destroyed = append(destroyed, change.id)
		}
	}
	sort.Strings(created)
	sort.Strings(updated)
	sort.Strings(destroyed)

	return map[string]any{
		"accountId":      accountID,
		"oldState":       sinceState,
		"newState":       newState,
		"hasMoreChanges": hasMore,
		"created":        created,
		"updated":        updated,
		"destroyed":      destroyed,
	}, nil
}

// QueryChangesFrom compares the current ordered result set with the one
// captured by sinceQueryState. RFC 8620 section 5.5 requires added (with
// indices) and removed, not the created/updated/destroyed sets of /changes.
//
// queryKey is the signature of the query being diffed. A token issued for a
// different signature, or a legacy token with no signature, cannot be diffed;
// the response asks the client to refetch instead of guessing.
func (t *Tracker) QueryChangesFrom(ctx context.Context, accountID string, kind StateKind, queryKey string, sinceState string, maxChanges *int, currentIDs []string) (map[string]any, error) {
	if maxChanges != nil && *maxChanges <= 0 {
		return nil, Errorf("invalidArguments", "maxChanges must be greater than zero")
	}
	old, tokenKey, ok := parseQueryStateToken(sinceState, kind)
	if !ok {
		return nil, Errorf("cannotCalculateChanges", "unrecognised state %q", sinceState)
	}

	var result map[string]any
	err := t.mutate(ctx, accountID, func(state *trackerState) (bool, error) {
		query := queryTrackerFor(state, kind, queryKey)
		if old > query.Version {
			return false, Errorf("cannotCalculateChanges", "state %q is newer than the current state", sinceState)
		}

		removed := []string{}
		added := []map[string]any{}
		canCalculate := tokenKey == queryKey
		if canCalculate {
			previous, ok := query.Snapshots[old]
			if !ok {
				// The result set for this version is no longer retained.
				canCalculate = false
			} else {
				currentSet := make(map[string]bool, len(currentIDs))
				for _, id := range currentIDs {
					currentSet[id] = true
				}
				previousSet := make(map[string]bool, len(previous))
				for _, id := range previous {
					previousSet[id] = true
					if !currentSet[id] {
						removed = append(removed, id)
					}
				}
				for index, id := range currentIDs {
					if !previousSet[id] {
						added = append(added, map[string]any{"index": index, "id": id})
					}
				}
				sort.Strings(removed)
				if maxChanges != nil && len(added)+len(removed) > *maxChanges {
					// There is no intermediate query state to resume from, so
					// ask the client to refetch rather than return a partial
					// diff.
					canCalculate = false
					removed = []string{}
					added = []map[string]any{}
				}
			}
		}

		version, changed := recordQuerySnapshot(&query, currentIDs)
		if changed {
			state.Queries[kind][queryKey] = query
		}
		result = map[string]any{
			"accountId":           accountID,
			"oldQueryState":       sinceState,
			"newQueryState":       queryStateToken(kind, queryKey, version),
			"canCalculateChanges": canCalculate,
			"removed":             removed,
			"added":               added,
		}
		return changed, nil
	})
	return result, err
}
