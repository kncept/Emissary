package base

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
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

	// maxChangeEntries bounds the in-memory delta log. Callers whose state is
	// older than the retained window get cannotCalculateChanges, which RFC
	// 8620 section 5.2 explicitly permits when intermediate states cannot be
	// produced.
	maxChangeEntries = 10000
)

// ChangeOp is the kind of modification applied to a record.
type ChangeOp uint8

const (
	ChangeCreate ChangeOp = iota
	ChangeUpdate
	ChangeDestroy
)

type changeEntry struct {
	seq uint64
	id  string
	op  ChangeOp
}

type accountChanges struct {
	versions     map[StateKind]uint64
	fingerprints map[StateKind]map[string]string
	logs         map[StateKind][]changeEntry
}

func newAccountChanges() *accountChanges {
	return &accountChanges{
		versions:     make(map[StateKind]uint64),
		fingerprints: make(map[StateKind]map[string]string),
		logs:         make(map[StateKind][]changeEntry),
	}
}

// Tracker records per-account, per-type change deltas. Deltas are derived by
// diffing snapshots of the underlying stores rather than by instrumenting
// writes, so changes made outside JMAP (inbound delivery, the support API) are
// visible to /changes too.
//
// The log is process-local. After a restart an old state token cannot be
// resolved and /changes returns cannotCalculateChanges, which tells the client
// to invalidate its cache and re-fetch. The spec allows this when a server
// cannot calculate an intermediate state.
type Tracker struct {
	mu       sync.Mutex
	accounts map[string]*accountChanges
}

// NewTracker creates an empty change tracker.
func NewTracker() *Tracker {
	return &Tracker{accounts: make(map[string]*accountChanges)}
}

func (t *Tracker) accountLocked(accountID string) *accountChanges {
	account, ok := t.accounts[accountID]
	if !ok {
		account = newAccountChanges()
		t.accounts[accountID] = account
	}
	return account
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

// State returns the current state token for an account and type.
func (t *Tracker) State(accountID string, kind StateKind) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return stateToken(kind, t.accountLocked(accountID).versions[kind])
}

// Sync compares new snapshots with the previous ones and records one change
// entry per differing record. The first call for a kind establishes the
// baseline without reporting changes, since there is no older state to diff
// against.
func (t *Tracker) Sync(accountID string, snapshots map[StateKind]map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	for kind, current := range snapshots {
		previous, seen := account.fingerprints[kind]
		if !seen {
			account.fingerprints[kind] = CopySnapshot(current)
			continue
		}
		diff := diffSnapshots(previous, current)
		if len(diff) == 0 {
			continue
		}
		for _, change := range diff {
			account.versions[kind]++
			account.appendChangeLocked(kind, changeEntry{
				seq: account.versions[kind],
				id:  change.id,
				op:  change.op,
			})
		}
		account.fingerprints[kind] = CopySnapshot(current)
	}
}

// Record appends an explicit change, used for data that has no snapshot to
// diff (for example a newly created EmailSubmission).
func (t *Tracker) Record(accountID string, kind StateKind, id string, op ChangeOp) {
	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	account.versions[kind]++
	account.appendChangeLocked(kind, changeEntry{seq: account.versions[kind], id: id, op: op})
}

func (a *accountChanges) appendChangeLocked(kind StateKind, entry changeEntry) {
	log := a.logs[kind]
	log = append(log, entry)
	if len(log) > maxChangeEntries {
		log = append([]changeEntry(nil), log[len(log)-maxChangeEntries:]...)
	}
	a.logs[kind] = log
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
			diff = append(diff, changeEntry{id: id, op: ChangeCreate})
		case old != fingerprint:
			diff = append(diff, changeEntry{id: id, op: ChangeUpdate})
		}
	}
	for id := range previous {
		if _, exists := current[id]; !exists {
			diff = append(diff, changeEntry{id: id, op: ChangeDestroy})
		}
	}
	sort.Slice(diff, func(i, j int) bool {
		if diff[i].id != diff[j].id {
			return diff[i].id < diff[j].id
		}
		return diff[i].op < diff[j].op
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
type aggregatedChange struct {
	id string
	op ChangeOp
}

// ChangesFrom builds a /changes response. It validates sinceState, coalesces
// repeated changes to the same id, honours maxChanges and returns an
// intermediate state token when more changes remain.
func (t *Tracker) ChangesFrom(accountID string, kind StateKind, sinceState string, maxChanges *int) (map[string]any, error) {
	if maxChanges != nil && *maxChanges <= 0 {
		return nil, Errorf("invalidArguments", "maxChanges must be greater than zero")
	}
	old, ok := parseStateToken(sinceState, kind)
	if !ok {
		return nil, Errorf("cannotCalculateChanges", "unrecognised state %q", sinceState)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	current := account.versions[kind]
	if old > current {
		return nil, Errorf("cannotCalculateChanges", "state %q is newer than the current state", sinceState)
	}
	log := account.logs[kind]
	if len(log) > 0 && old < log[0].seq-1 {
		return nil, Errorf("cannotCalculateChanges", "state %q is older than the retained change log", sinceState)
	}

	order := make([]aggregatedChange, 0, len(log))
	index := make(map[string]int, len(log))
	lastSeq := old
	hasMore := false

	for _, entry := range log {
		if entry.seq <= old {
			continue
		}
		idx, exists := index[entry.id]
		projected := len(order)
		if !exists {
			projected++
		} else if entry.op == ChangeDestroy && order[idx].op == ChangeCreate {
			// A create+destroy pair drops out of the result entirely. Other
			// repeated operations replace the existing entry, not add one.
			projected--
		}
		if maxChanges != nil && projected > *maxChanges {
			hasMore = true
			break
		}

		switch entry.op {
		case ChangeCreate:
			if exists {
				// Destroy followed by create is a new record.
				order[idx].op = ChangeCreate
			} else {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: ChangeCreate})
			}
		case ChangeUpdate:
			if !exists {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: ChangeUpdate})
			}
			// created+updated stays in created; destroyed+updated stays in
			// destroyed.
		case ChangeDestroy:
			if !exists {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: ChangeDestroy})
			} else if order[idx].op == ChangeCreate {
				// created+destroyed SHOULD be omitted entirely.
				order = append(order[:idx], order[idx+1:]...)
				delete(index, entry.id)
				for i := idx; i < len(order); i++ {
					index[order[i].id] = i
				}
			} else {
				// updated+destroyed appears only in destroyed.
				order[idx].op = ChangeDestroy
			}
		}
		lastSeq = entry.seq
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

	newState := stateToken(kind, current)
	if hasMore {
		newState = stateToken(kind, lastSeq)
	}
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
