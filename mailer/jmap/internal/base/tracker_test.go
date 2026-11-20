package base

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/storage"
)

const testAccountID = "user@example.com"

func stateVersion(t *testing.T, state string, kind StateKind) uint64 {
	t.Helper()
	prefix := string(kind) + "-"
	if !strings.HasPrefix(state, prefix) {
		t.Fatalf("state %q does not start with %q", state, prefix)
	}
	version, err := strconv.ParseUint(strings.TrimPrefix(state, prefix), 10, 64)
	if err != nil {
		t.Fatalf("state %q has an invalid version: %v", state, err)
	}
	return version
}

func TestTrackerStateIsDurableAndMonotonic(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	tracker := NewTracker(store)

	snapshot := map[string]string{"one": "v1"}
	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	initial, err := tracker.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}

	// Repeating the same snapshot must not create a change.
	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	stable, err := tracker.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if stable != initial {
		t.Fatalf("state changed without data changing: %q -> %q", initial, stable)
	}

	snapshot["one"] = "v2"
	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	changed, err := tracker.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if stateVersion(t, changed, StateEmail) <= stateVersion(t, initial, StateEmail) {
		t.Fatalf("state did not advance: %q -> %q", initial, changed)
	}

	// A fresh tracker represents a new Lambda invocation or process restart.
	restarted := NewTracker(store)
	resumed, err := restarted.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() after restart error = %v", err)
	}
	if resumed != changed {
		t.Fatalf("state after restart = %q, want %q", resumed, changed)
	}

	// Replaying an already-observed snapshot must not roll the counter back.
	if err := restarted.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
		t.Fatalf("Sync() after restart error = %v", err)
	}
	replayed, err := restarted.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() after replay error = %v", err)
	}
	if replayed != changed {
		t.Fatalf("state regressed after replay: %q -> %q", changed, replayed)
	}
}

func TestTrackerRetriesConflictsWithoutRegressingState(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	first := NewTracker(store)
	second := NewTracker(store)

	// Both invocations cache the same initial revision.
	if _, err := first.State(ctx, testAccountID, StateEmail); err != nil {
		t.Fatalf("first State() error = %v", err)
	}
	if _, err := second.State(ctx, testAccountID, StateEmail); err != nil {
		t.Fatalf("second State() error = %v", err)
	}

	if err := first.Record(ctx, testAccountID, StateEmail, "one", ChangeCreate); err != nil {
		t.Fatalf("first Record() error = %v", err)
	}
	// The second invocation has a stale copy; the conflict retry must reload
	// and append at the newer counter instead of overwriting it.
	if err := second.Record(ctx, testAccountID, StateEmail, "two", ChangeCreate); err != nil {
		t.Fatalf("second Record() error = %v", err)
	}

	state, err := second.State(ctx, testAccountID, StateEmail)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if want := stateToken(StateEmail, 2); state != want {
		t.Fatalf("state = %q, want %q", state, want)
	}
	response, err := second.ChangesFrom(ctx, testAccountID, StateEmail, stateToken(StateEmail, 0), nil)
	if err != nil {
		t.Fatalf("ChangesFrom() error = %v", err)
	}
	if created := response["created"].([]string); len(created) != 2 || created[0] != "one" || created[1] != "two" {
		t.Fatalf("created = %v, want [one two]", created)
	}
}

func TestTrackerChangesFromCoalescesDuplicateOperations(t *testing.T) {
	ctx := context.Background()
	tracker := NewTracker(storage.NewMemoryStorage())

	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {},
	}); err != nil {
		t.Fatalf("Sync() baseline error = %v", err)
	}
	since := stateToken(StateEmail, 0)

	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {"a": "v1"},
	}); err != nil {
		t.Fatalf("Sync() create error = %v", err)
	}
	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {"a": "v2"},
	}); err != nil {
		t.Fatalf("Sync() update error = %v", err)
	}

	response, err := tracker.ChangesFrom(ctx, testAccountID, StateEmail, since, nil)
	if err != nil {
		t.Fatalf("ChangesFrom() error = %v", err)
	}
	created, ok := response["created"].([]string)
	if !ok || len(created) != 1 || created[0] != "a" {
		t.Fatalf("created = %v, want [a]", response["created"])
	}
	if updated := response["updated"].([]string); len(updated) != 0 {
		t.Fatalf("updated = %v, want empty", updated)
	}

	// created + destroyed SHOULD be omitted entirely.
	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {},
	}); err != nil {
		t.Fatalf("Sync() destroy error = %v", err)
	}
	response, err = tracker.ChangesFrom(ctx, testAccountID, StateEmail, since, nil)
	if err != nil {
		t.Fatalf("ChangesFrom() after destroy error = %v", err)
	}
	for _, field := range []string{"created", "updated", "destroyed"} {
		if changes := response[field].([]string); len(changes) != 0 {
			t.Fatalf("%s = %v, want empty", field, changes)
		}
	}
}

func TestTrackerChangesFromMaxChangesUsesCoalescedCount(t *testing.T) {
	ctx := context.Background()
	tracker := NewTracker(storage.NewMemoryStorage())

	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {},
	}); err != nil {
		t.Fatalf("Sync() baseline error = %v", err)
	}
	since := stateToken(StateEmail, 0)

	// The intermediate create of "gone" temporarily makes two changes, but
	// it is destroyed before the response is built, leaving only one.
	for _, snapshot := range []map[string]string{
		{"gone": "v1"},
		{"gone": "v1", "kept": "v1"},
		{"kept": "v1"},
	} {
		if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}

	maxChanges := 1
	response, err := tracker.ChangesFrom(ctx, testAccountID, StateEmail, since, &maxChanges)
	if err != nil {
		t.Fatalf("ChangesFrom() error = %v", err)
	}
	if response["hasMoreChanges"] != false {
		t.Fatalf("hasMoreChanges = %v, want false", response["hasMoreChanges"])
	}
	if created := response["created"].([]string); len(created) != 1 || created[0] != "kept" {
		t.Fatalf("created = %v, want [kept]", created)
	}
}

func TestTrackerChangesFromPagesByFinalChangeSequence(t *testing.T) {
	ctx := context.Background()
	tracker := NewTracker(storage.NewMemoryStorage())

	if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{
		StateEmail: {"second": "v1"},
	}); err != nil {
		t.Fatalf("Sync() baseline error = %v", err)
	}
	since := stateToken(StateEmail, 0)

	// "first" is created, then updated; the intermediate state token must
	// include the update or the client would see it again as a duplicate.
	for _, snapshot := range []map[string]string{
		{"first": "v1", "second": "v1"},
		{"first": "v2", "second": "v1"},
		{"first": "v2"},
	} {
		if err := tracker.Sync(ctx, testAccountID, map[StateKind]map[string]string{StateEmail: snapshot}); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}

	maxChanges := 1
	firstPage, err := tracker.ChangesFrom(ctx, testAccountID, StateEmail, since, &maxChanges)
	if err != nil {
		t.Fatalf("ChangesFrom() first page error = %v", err)
	}
	if firstPage["hasMoreChanges"] != true {
		t.Fatalf("hasMoreChanges = %v, want true", firstPage["hasMoreChanges"])
	}
	if created := firstPage["created"].([]string); len(created) != 1 || created[0] != "first" {
		t.Fatalf("created = %v, want [first]", created)
	}
	intermediate, ok := firstPage["newState"].(string)
	if !ok || intermediate != stateToken(StateEmail, 2) {
		t.Fatalf("newState = %v, want %q", firstPage["newState"], stateToken(StateEmail, 2))
	}

	secondPage, err := tracker.ChangesFrom(ctx, testAccountID, StateEmail, intermediate, nil)
	if err != nil {
		t.Fatalf("ChangesFrom() second page error = %v", err)
	}
	if secondPage["hasMoreChanges"] != false {
		t.Fatalf("hasMoreChanges = %v, want false", secondPage["hasMoreChanges"])
	}
	if destroyed := secondPage["destroyed"].([]string); len(destroyed) != 1 || destroyed[0] != "second" {
		t.Fatalf("destroyed = %v, want [second]", destroyed)
	}
}

func TestTrackerQueryStatesAreScopedAndDurablyDiffed(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	tracker := NewTracker(store)

	inboxKey, err := QueryKey(map[string]any{"inMailbox": "inbox"}, nil)
	if err != nil {
		t.Fatalf("QueryKey() error = %v", err)
	}
	sentKey, err := QueryKey(map[string]any{"inMailbox": "sent"}, nil)
	if err != nil {
		t.Fatalf("QueryKey() error = %v", err)
	}

	inboxState, err := tracker.SyncQuery(ctx, testAccountID, StateEmailQuery, inboxKey, []string{"one", "two"})
	if err != nil {
		t.Fatalf("SyncQuery() error = %v", err)
	}
	sentState, err := tracker.SyncQuery(ctx, testAccountID, StateEmailQuery, sentKey, []string{"one"})
	if err != nil {
		t.Fatalf("SyncQuery() error = %v", err)
	}
	if inboxState == sentState {
		t.Fatal("different query signatures share a queryState token")
	}

	// A change to the inbox result set must not affect the sent query.
	nextInbox, err := tracker.SyncQuery(ctx, testAccountID, StateEmailQuery, inboxKey, []string{"two", "three"})
	if err != nil {
		t.Fatalf("SyncQuery() error = %v", err)
	}
	unchangedSent, err := tracker.SyncQuery(ctx, testAccountID, StateEmailQuery, sentKey, []string{"one"})
	if err != nil {
		t.Fatalf("SyncQuery() error = %v", err)
	}
	if nextInbox == inboxState {
		t.Fatal("queryState did not change when the result set changed")
	}
	if unchangedSent != sentState {
		t.Fatalf("unrelated query state changed: %q -> %q", sentState, unchangedSent)
	}

	response, err := tracker.QueryChangesFrom(ctx, testAccountID, StateEmailQuery, inboxKey, inboxState, nil, []string{"two", "three"})
	if err != nil {
		t.Fatalf("QueryChangesFrom() error = %v", err)
	}
	if response["canCalculateChanges"] != true {
		t.Fatalf("canCalculateChanges = %v, want true", response["canCalculateChanges"])
	}
	if removed := response["removed"].([]string); len(removed) != 1 || removed[0] != "one" {
		t.Fatalf("removed = %v, want [one]", response["removed"])
	}
	added, ok := response["added"].([]map[string]any)
	if !ok || len(added) != 1 || added[0]["id"] != "three" || added[0]["index"] != 1 {
		t.Fatalf("added = %v, want [{index 1 id three}]", response["added"])
	}

	// A token from a different query cannot be diffed.
	mismatch, err := tracker.QueryChangesFrom(ctx, testAccountID, StateEmailQuery, sentKey, inboxState, nil, []string{"one"})
	if err != nil {
		t.Fatalf("QueryChangesFrom() mismatch error = %v", err)
	}
	if mismatch["canCalculateChanges"] != false {
		t.Fatalf("canCalculateChanges = %v, want false", mismatch["canCalculateChanges"])
	}

	// The new token is usable after a restart, and queryChanges records the
	// current result set so a follow-up call needs no refetch.
	restarted := NewTracker(store)
	followUp, err := restarted.QueryChangesFrom(ctx, testAccountID, StateEmailQuery, inboxKey, nextInbox, nil, []string{"two", "three"})
	if err != nil {
		t.Fatalf("QueryChangesFrom() after restart error = %v", err)
	}
	if followUp["canCalculateChanges"] != true {
		t.Fatalf("canCalculateChanges = %v, want true", followUp["canCalculateChanges"])
	}
	if len(followUp["removed"].([]string)) != 0 || len(followUp["added"].([]map[string]any)) != 0 {
		t.Fatalf("follow-up changes = %v / %v, want empty", followUp["added"], followUp["removed"])
	}
}
