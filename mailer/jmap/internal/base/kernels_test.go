package base

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kncept/Emissary/mailer/components/storage"
)

// fakeRecord is the type adapter used to exercise the generic kernels without
// depending on any real JMAP data type.
type fakeRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Hidden string `json:"hidden"`
}

func fakeRecords() []fakeRecord {
	return []fakeRecord{
		{ID: "a", Name: "Alpha", Hidden: "x"},
		{ID: "b", Name: "Beta", Hidden: "y"},
		{ID: "c", Name: "Gamma", Hidden: "z"},
	}
}

func fakeGetSpec(records []fakeRecord, state string) GetSpec[fakeRecord] {
	return GetSpec[fakeRecord]{
		Allowed: ArgumentSet("accountId", "ids", "properties"),
		Model:   fakeRecord{},
		Load:    func(context.Context) ([]fakeRecord, error) { return records, nil },
		ID:      func(r fakeRecord) string { return r.ID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, r fakeRecord, p *Selection, _ json.RawMessage) (json.RawMessage, error) {
			return p.Filter(r)
		},
	}
}

func decodeKernelResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return result
}

// TestHandleGetPaginationPropertiesAndNotFound verifies the /get kernel
// distinguishes omitted ids from an explicit empty array, honours properties
// (including the id-only case) and reports missing ids in request order.
func TestHandleGetPaginationPropertiesAndNotFound(t *testing.T) {
	ctx := context.Background()
	records := fakeRecords()
	spec := fakeGetSpec(records, "fake-1")

	cases := []struct {
		name      string
		args      string
		wantIDs   []any
		wantNames []any
		wantNF    []any
	}{
		{
			name:      "all records by default",
			args:      `{"ids":null}`,
			wantIDs:   []any{"a", "b", "c"},
			wantNames: []any{"Alpha", "Beta", "Gamma"},
			wantNF:    []any{},
		},
		{
			name:      "explicit empty array selects nothing",
			args:      `{"ids":[]}`,
			wantIDs:   []any{},
			wantNames: []any{},
			wantNF:    []any{},
		},
		{
			name:      "explicit ids keep request order and report gaps",
			args:      `{"ids":["c","missing","a"]}`,
			wantIDs:   []any{"c", "a"},
			wantNames: []any{"Gamma", "Alpha"},
			wantNF:    []any{"missing"},
		},
		{
			name:      "properties selection",
			args:      `{"ids":["a"],"properties":["id","name"]}`,
			wantIDs:   []any{"a"},
			wantNames: []any{"Alpha"},
			wantNF:    []any{},
		},
		{
			name:    "empty properties yields id only",
			args:    `{"ids":["a"],"properties":[]}`,
			wantIDs: []any{"a"},
			wantNF:  []any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := HandleGet(ctx, testAccountID, json.RawMessage(tc.args), spec)
			if err != nil {
				t.Fatalf("HandleGet() error = %v", err)
			}
			result := decodeKernelResult(t, raw)
			if result["accountId"] != testAccountID {
				t.Errorf("accountId = %v", result["accountId"])
			}
			if result["state"] != "fake-1" {
				t.Errorf("state = %v, want fake-1", result["state"])
			}
			list, _ := result["list"].([]any)
			if len(list) != len(tc.wantIDs) {
				t.Fatalf("list = %v, want %d entries", list, len(tc.wantIDs))
			}
			for i, entry := range list {
				obj, _ := entry.(map[string]any)
				if obj["id"] != tc.wantIDs[i] {
					t.Errorf("list[%d].id = %v, want %v", i, obj["id"], tc.wantIDs[i])
				}
			}
			if tc.wantNames != nil {
				for i, entry := range list {
					obj, _ := entry.(map[string]any)
					if obj["name"] != tc.wantNames[i] {
						t.Errorf("list[%d].name = %v, want %v", i, obj["name"], tc.wantNames[i])
					}
				}
			}
			if tc.name == "empty properties yields id only" {
				obj, _ := list[0].(map[string]any)
				if _, ok := obj["name"]; ok {
					t.Errorf("list[0] unexpectedly contains name: %v", obj)
				}
			}
			nf, _ := result["notFound"].([]any)
			if !reflect.DeepEqual(nf, tc.wantNF) {
				t.Errorf("notFound = %v, want %v", nf, tc.wantNF)
			}
		})
	}
}

// TestHandleGetAccountLess verifies that account-less methods omit accountId.
func TestHandleGetAccountLess(t *testing.T) {
	spec := fakeGetSpec(fakeRecords(), "")
	spec.AccountLess = true
	spec.State = nil
	raw, err := HandleGet(context.Background(), "", json.RawMessage(`{"ids":["a"]}`), spec)
	if err != nil {
		t.Fatalf("HandleGet() error = %v", err)
	}
	result := decodeKernelResult(t, raw)
	if _, ok := result["accountId"]; ok {
		t.Errorf("account-less response must not contain accountId: %v", result)
	}
	if _, ok := result["state"]; ok {
		t.Errorf("account-less response must not contain state: %v", result)
	}
}

// TestHandleGetLoadIDs honours the explicit-ids fast path, including its
// reported missing ids.
func TestHandleGetLoadIDs(t *testing.T) {
	spec := fakeGetSpec(fakeRecords(), "fake-1")
	spec.LoadIDs = func(_ context.Context, ids []string) ([]fakeRecord, []string, error) {
		byID := map[string]fakeRecord{}
		for _, r := range fakeRecords() {
			byID[r.ID] = r
		}
		var found []fakeRecord
		var missing []string
		for _, id := range ids {
			if r, ok := byID[id]; ok {
				found = append(found, r)
			} else {
				missing = append(missing, id)
			}
		}
		return found, missing, nil
	}
	raw, err := HandleGet(context.Background(), testAccountID, json.RawMessage(`{"ids":["b","gone"]}`), spec)
	if err != nil {
		t.Fatalf("HandleGet() error = %v", err)
	}
	result := decodeKernelResult(t, raw)
	nf, _ := result["notFound"].([]any)
	if !reflect.DeepEqual(nf, []any{"gone"}) {
		t.Errorf("notFound = %v, want [gone]", nf)
	}
	list, _ := result["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %v, want one entry", list)
	}
}

// TestHandleQueryPagination verifies that /query slices the ordered id set,
// includes total only when requested, and publishes a query state.
func TestHandleQueryPagination(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	tracker := NewTracker(store)
	spec := QuerySpec{
		Allowed:        ArgumentSet("accountId", "filter", "sort", "position", "anchor", "anchorOffset", "limit", "calculateTotal", "collapseThreads"),
		SortProperties: map[string]bool{"name": true},
		ParseFilter:    func(json.RawMessage) (any, error) { return nil, nil },
		AllIDs: func(context.Context, string, json.RawMessage, any, bool) ([]string, error) {
			return []string{"a", "b", "c"}, nil
		},
		Tracker: tracker,
		Kind:    StateEmail,
	}
	cases := []struct {
		name       string
		args       string
		wantIDs    []any
		wantPos    float64
		wantTotal  bool
		wantTotalV float64
	}{
		{"default", `{}`, []any{"a", "b", "c"}, 0, false, 0},
		{"position and limit", `{"position":1,"limit":1}`, []any{"b"}, 1, false, 0},
		{"calculateTotal", `{"position":2,"calculateTotal":true}`, []any{"c"}, 2, true, 3},
		{"empty limit", `{"limit":0}`, []any{}, 0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := HandleQuery(ctx, testAccountID, json.RawMessage(tc.args), spec)
			if err != nil {
				t.Fatalf("HandleQuery() error = %v", err)
			}
			result := decodeKernelResult(t, raw)
			ids, _ := result["ids"].([]any)
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
			}
			if result["position"] != tc.wantPos {
				t.Errorf("position = %v, want %v", result["position"], tc.wantPos)
			}
			if result["canCalculateChanges"] != true {
				t.Errorf("canCalculateChanges = %v, want true", result["canCalculateChanges"])
			}
			if result["queryState"] == nil || result["queryState"] == "" {
				t.Errorf("queryState missing from %v", result)
			}
			total, present := result["total"]
			if present != tc.wantTotal {
				t.Fatalf("total presence = %v, want %v (%v)", present, tc.wantTotal, result)
			}
			if present && total != tc.wantTotalV {
				t.Errorf("total = %v, want %v", total, tc.wantTotalV)
			}
		})
	}
}

// TestHandleChangesCannotCalculate verifies the /changes kernel delegates the
// cannotCalculateChanges decision to the type adapter without reshaping it.
func TestHandleChangesCannotCalculate(t *testing.T) {
	called := false
	spec := ChangesSpec{
		Allowed: ArgumentSet("accountId", "sinceState", "maxChanges"),
		Changes: func(context.Context, string, *int) (map[string]any, error) {
			called = true
			return map[string]any{
				"accountId": testAccountID,
				"oldState":  "old",
				"newState":  "new",
				"error":     map[string]any{"type": "cannotCalculateChanges"},
			}, nil
		},
	}
	raw, err := HandleChanges(context.Background(), testAccountID,
		json.RawMessage(`{"sinceState":"old"}`), spec)
	if err != nil {
		t.Fatalf("HandleChanges() error = %v", err)
	}
	if !called {
		t.Fatal("Changes callback was not called")
	}
	result := decodeKernelResult(t, raw)
	errObj, _ := result["error"].(map[string]any)
	if errObj["type"] != "cannotCalculateChanges" {
		t.Errorf("error type = %v, want cannotCalculateChanges", errObj["type"])
	}
}

// TestHandleSetStateMismatchAndShape verifies the /set kernel rejects a stale
// ifInState with stateMismatch and always emits the RFC 8620 section 5.3
// response fields, with null for empty result maps.
func TestHandleSetStateMismatchAndShape(t *testing.T) {
	state := "fake-1"
	mutated := false
	spec := SetSpec{
		Allowed: ArgumentSet("accountId", "ifInState", "create", "update", "destroy"),
		State:   func(context.Context) (string, error) { return state, nil },
		Create: func(_ context.Context, _ string, record json.RawMessage) (json.RawMessage, *SetError, error) {
			mutated = true
			return json.RawMessage(`{"id":"new"}`), nil, nil
		},
		Update: func(context.Context, string, json.RawMessage) (json.RawMessage, *SetError, error) {
			mutated = true
			return json.RawMessage(`{}`), nil, nil
		},
		Destroy: func(context.Context, string) *SetError {
			mutated = true
			return nil
		},
	}

	// A stale ifInState aborts the whole request before any callback runs.
	_, err := HandleSet(context.Background(), testAccountID,
		json.RawMessage(`{"ifInState":"stale","create":{"c1":{"name":"New"}}}`), spec)
	if err == nil {
		t.Fatal("HandleSet() with stale ifInState returned no error")
	}
	methodErr, ok := err.(*methodError)
	if !ok {
		t.Fatalf("error = %v (%T), want a JMAP methodError", err, err)
	}
	if methodErr.typ != "stateMismatch" {
		t.Errorf("error type = %q, want stateMismatch", methodErr.typ)
	}
	if mutated {
		t.Fatal("callbacks ran despite stale ifInState")
	}

	raw, err := HandleSet(context.Background(), testAccountID,
		json.RawMessage(`{"create":{"c1":{"name":"New"}},"destroy":["a"]}`), spec)
	if err != nil {
		t.Fatalf("HandleSet() error = %v", err)
	}
	result := decodeKernelResult(t, raw)
	for _, key := range []string{"created", "updated", "destroyed", "notCreated", "notUpdated", "notDestroyed"} {
		if _, ok := result[key]; !ok {
			t.Errorf("response is missing %q: %v", key, result)
		}
	}
	if result["oldState"] != "fake-1" || result["newState"] != "fake-1" {
		t.Errorf("oldState/newState = %v/%v", result["oldState"], result["newState"])
	}
	created, _ := result["created"].(map[string]any)
	if len(created) != 1 {
		t.Errorf("created = %v, want one entry", created)
	}
	if result["updated"] != nil {
		t.Errorf("updated = %v, want null", result["updated"])
	}
	destroyed, _ := result["destroyed"].([]any)
	if !reflect.DeepEqual(destroyed, []any{"a"}) {
		t.Errorf("destroyed = %v, want [a]", destroyed)
	}
}
