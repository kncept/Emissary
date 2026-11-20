package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
)

// stateKind is one independently versioned set of JMAP records. RFC 8620
// section 5.1 requires a type's state string to change whenever any record of
// that type changes and to stay the same when it does not, so each kind has its
// own monotonic counter rather than a shared wall-clock timestamp.
type stateKind string

const (
	stateEmail        stateKind = "email"
	stateEmailQuery   stateKind = "email-query"
	stateMailbox      stateKind = "mailbox"
	stateMailboxQuery stateKind = "mailbox-query"
	stateThread       stateKind = "thread"
	stateIdentity     stateKind = "identity"
	stateSubmission   stateKind = "submission"
	stateVacation     stateKind = "vacation"

	// maxChangeEntries bounds the in-memory delta log. Callers whose state is
	// older than the retained window get cannotCalculateChanges, which RFC
	// 8620 section 5.2 explicitly permits when intermediate states cannot be
	// produced.
	maxChangeEntries = 10000
)

type changeOp uint8

const (
	changeCreate changeOp = iota
	changeUpdate
	changeDestroy
)

type changeEntry struct {
	seq uint64
	id  string
	op  changeOp
}

type accountChanges struct {
	versions     map[stateKind]uint64
	fingerprints map[stateKind]map[string]string
	logs         map[stateKind][]changeEntry
}

func newAccountChanges() *accountChanges {
	return &accountChanges{
		versions:     make(map[stateKind]uint64),
		fingerprints: make(map[stateKind]map[string]string),
		logs:         make(map[stateKind][]changeEntry),
	}
}

// changeTracker records per-account, per-type change deltas. Deltas are
// derived by diffing snapshots of the underlying stores rather than by
// instrumenting writes, so changes made outside JMAP (inbound delivery, the
// support API) are visible to /changes too.
//
// The log is process-local. After a restart an old state token cannot be
// resolved and /changes returns cannotCalculateChanges, which tells the client
// to invalidate its cache and re-fetch. The spec allows this when a server
// cannot calculate an intermediate state.
type changeTracker struct {
	mu       sync.Mutex
	accounts map[string]*accountChanges
}

func newChangeTracker() *changeTracker {
	return &changeTracker{accounts: make(map[string]*accountChanges)}
}

func (t *changeTracker) accountLocked(accountID string) *accountChanges {
	account, ok := t.accounts[accountID]
	if !ok {
		account = newAccountChanges()
		t.accounts[accountID] = account
	}
	return account
}

func stateToken(kind stateKind, version uint64) string {
	return fmt.Sprintf("%s-%d", kind, version)
}

func parseStateToken(state string, kind stateKind) (uint64, bool) {
	prefix := string(kind) + "-"
	if !strings.HasPrefix(state, prefix) {
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimPrefix(state, prefix), 10, 64)
	return version, err == nil
}

func (t *changeTracker) state(accountID string, kind stateKind) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return stateToken(kind, t.accountLocked(accountID).versions[kind])
}

// sync compares the new snapshots with the previous ones and records one
// change entry per differing record. The first call for a kind establishes the
// baseline without reporting changes, since there is no older state to diff
// against.
func (t *changeTracker) sync(accountID string, snapshots map[stateKind]map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	for kind, current := range snapshots {
		previous, seen := account.fingerprints[kind]
		if !seen {
			account.fingerprints[kind] = copySnapshot(current)
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
		account.fingerprints[kind] = copySnapshot(current)
	}
}

// record appends an explicit change, used for data that has no snapshot to
// diff (for example a newly created EmailSubmission).
func (t *changeTracker) record(accountID string, kind stateKind, id string, op changeOp) {
	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	account.versions[kind]++
	account.appendChangeLocked(kind, changeEntry{seq: account.versions[kind], id: id, op: op})
}

func (a *accountChanges) appendChangeLocked(kind stateKind, entry changeEntry) {
	log := a.logs[kind]
	log = append(log, entry)
	if len(log) > maxChangeEntries {
		log = append([]changeEntry(nil), log[len(log)-maxChangeEntries:]...)
	}
	a.logs[kind] = log
}

func copySnapshot(snapshot map[string]string) map[string]string {
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
			diff = append(diff, changeEntry{id: id, op: changeCreate})
		case old != fingerprint:
			diff = append(diff, changeEntry{id: id, op: changeUpdate})
		}
	}
	for id := range previous {
		if _, exists := current[id]; !exists {
			diff = append(diff, changeEntry{id: id, op: changeDestroy})
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

type jmapMethodError struct {
	typ    string
	detail string
}

func (e *jmapMethodError) Error() string { return e.typ + ": " + e.detail }

func jmapErrorf(typ, format string, args ...any) error {
	return &jmapMethodError{typ: typ, detail: fmt.Sprintf(format, args...)}
}

// aggregatedChange is the effective change for a record after applying RFC
// 8620 section 5.2's coalescing rules to every entry in the delta window.
type aggregatedChange struct {
	id string
	op changeOp
}

// changesFrom builds a /changes response. It validates sinceState, coalesces
// repeated changes to the same id, honours maxChanges and returns an
// intermediate state token when more changes remain.
func (t *changeTracker) changesFrom(accountID string, kind stateKind, sinceState string, maxChanges *int) (map[string]any, error) {
	if maxChanges != nil && *maxChanges <= 0 {
		return nil, jmapErrorf("invalidArguments", "maxChanges must be greater than zero")
	}
	old, ok := parseStateToken(sinceState, kind)
	if !ok {
		return nil, jmapErrorf("cannotCalculateChanges", "unrecognised state %q", sinceState)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	account := t.accountLocked(accountID)
	current := account.versions[kind]
	if old > current {
		return nil, jmapErrorf("cannotCalculateChanges", "state %q is newer than the current state", sinceState)
	}
	log := account.logs[kind]
	if len(log) > 0 && old < log[0].seq-1 {
		return nil, jmapErrorf("cannotCalculateChanges", "state %q is older than the retained change log", sinceState)
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
		} else if entry.op == changeDestroy && order[idx].op == changeCreate {
			// A create+destroy pair drops out of the result entirely. Other
			// repeated operations replace the existing entry, not add one.
			projected--
		}
		if maxChanges != nil && projected > *maxChanges {
			hasMore = true
			break
		}

		switch entry.op {
		case changeCreate:
			if exists {
				// Destroy followed by create is a new record.
				order[idx].op = changeCreate
			} else {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: changeCreate})
			}
		case changeUpdate:
			if !exists {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: changeUpdate})
			}
			// created+updated stays in created; destroyed+updated stays in
			// destroyed.
		case changeDestroy:
			if !exists {
				index[entry.id] = len(order)
				order = append(order, aggregatedChange{id: entry.id, op: changeDestroy})
			} else if order[idx].op == changeCreate {
				// created+destroyed SHOULD be omitted entirely.
				order = append(order[:idx], order[idx+1:]...)
				delete(index, entry.id)
				for i := idx; i < len(order); i++ {
					index[order[i].id] = i
				}
			} else {
				// updated+destroyed appears only in destroyed.
				order[idx].op = changeDestroy
			}
		}
		lastSeq = entry.seq
	}

	created, updated, destroyed := []string{}, []string{}, []string{}
	for _, change := range order {
		switch change.op {
		case changeCreate:
			created = append(created, change.id)
		case changeUpdate:
			updated = append(updated, change.id)
		case changeDestroy:
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

// Snapshot helpers live with the tracker so every state token is produced
// from a consistent view of the underlying stores.

func (h *Handler) tracker() *changeTracker {
	if h.changes == nil {
		h.changes = newChangeTracker()
	}
	return h.changes
}

func (h *Handler) state(accountID string, kind stateKind) string {
	return h.tracker().state(accountID, kind)
}

func (h *Handler) syncEmailState(ctx context.Context, accountID string) string {
	listed, err := h.storage.ListEmails(ctx, accountID, "")
	if err != nil {
		// Keep the last known state; a storage failure must not invent a
		// change or erase the delta history.
		return h.state(accountID, stateEmail)
	}
	return h.syncEmailStateFromListed(ctx, accountID, listed)
}

// syncEmailStateFromListed avoids a second storage listing when the caller has
// already loaded the account's full mailbox (Email/get does this).
func (h *Handler) syncEmailStateFromListed(ctx context.Context, accountID string, listed []*email.ListedEmail) string {
	snapshot := make(map[string]string, len(listed))
	for _, item := range listed {
		if item == nil || item.Ref == nil || item.Email == nil {
			continue
		}
		snapshot[item.Ref.ID] = emailFingerprint(item, h.emailMetadata(ctx, accountID, item.Ref.ID))
	}
	h.tracker().sync(accountID, map[stateKind]map[string]string{
		stateEmail:      snapshot,
		stateEmailQuery: snapshot,
	})
	return h.state(accountID, stateEmail)
}

func (h *Handler) syncMailboxState(_ context.Context, accountID string, mailboxes []Mailbox) string {
	snapshot := make(map[string]string, len(mailboxes))
	for _, mailbox := range mailboxes {
		fingerprint, err := json.Marshal(mailbox)
		if err != nil {
			continue
		}
		snapshot[mailbox.ID] = string(fingerprint)
	}
	h.tracker().sync(accountID, map[stateKind]map[string]string{
		stateMailbox:      snapshot,
		stateMailboxQuery: snapshot,
	})
	return h.state(accountID, stateMailbox)
}

func (h *Handler) syncThreadState(_ context.Context, accountID string, threads []Thread) string {
	snapshot := make(map[string]string, len(threads))
	for _, thread := range threads {
		snapshot[thread.ID] = strings.Join(thread.EmailIDs, ",")
	}
	h.tracker().sync(accountID, map[stateKind]map[string]string{stateThread: snapshot})
	return h.state(accountID, stateThread)
}

// emailFingerprint incorporates every property Emissary can change on an
// Email: its mailbox, server-owned metadata and the rendered content fields.
func emailFingerprint(listed *email.ListedEmail, meta metadata.EmailMetadata) string {
	keywords := make([]string, 0, len(meta.Keywords))
	for keyword, enabled := range meta.Keywords {
		if enabled {
			keywords = append(keywords, keyword)
		}
	}
	sort.Strings(keywords)
	mailboxID := listed.Ref.MailboxID
	if mailboxID == "" {
		mailboxID = "inbox"
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s", mailboxID, meta.ReceivedAt,
		strings.Join(keywords, ","), listed.Email.Size, listed.Email.Subject, listed.Email.MessageID)
}
