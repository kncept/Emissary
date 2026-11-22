package base

import (
	"context"
	"encoding/json"
	"sort"
)

// GetSpec describes the type-specific parts of a standard /get method. The
// kernel handles argument validation, property selection, id lookup, response
// assembly and state sync so no data-type package has to build list/notFound
// by hand.
type GetSpec[T any] struct {
	// Allowed is the complete set of accepted argument names, including
	// accountId and properties.
	Allowed map[string]bool
	// Model is a zero value of the JMAP type, used to validate properties.
	Model any
	// DynamicPrefix permits dynamic property names such as "header:Subject".
	DynamicPrefix string
	// Defaults overrides the RFC 8620 "all properties" default with a type's
	// RFC-defined default array.
	Defaults []string
	// Load returns every record of this type in a stable order.
	Load func(ctx context.Context) ([]T, error)
	// LoadIDs is an optional optimisation for an explicit ids array. It must
	// return records in request order and report any ids it could not load.
	// When nil, Load is used and ids are resolved from the full list.
	LoadIDs func(ctx context.Context, ids []string) ([]T, []string, error)
	// ID returns a record's JMAP id.
	ID func(T) string
	// AccountLess omits accountId from the response, as required by RFC 8620
	// section 7 for account-less methods such as PushSubscription/get.
	AccountLess bool
	// State returns the type's state token. A nil callback omits the state
	// property, as required for account-less types such as PushSubscription.
	State func(ctx context.Context) (string, error)
	// Render converts a record into its final JSON object. The properties
	// selection and the original arguments are supplied for type-specific
	// handling (for example Email's bodyProperties and header injection).
	Render func(ctx context.Context, record T, properties *Selection, args json.RawMessage) (json.RawMessage, error)
}

// HandleGet implements RFC 8620 section 5.1 for any JMAP data type. An omitted
// or null ids argument selects every record; an explicit empty array selects
// none. Missing requested ids are reported in notFound in request order.
func HandleGet[T any](ctx context.Context, accountID string, args json.RawMessage, spec GetSpec[T]) (json.RawMessage, error) {
	if err := RejectUnknownArguments(args, spec.Allowed); err != nil {
		return nil, err
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, Errorf("invalidArguments", "ids must be an array of strings or null")
	}
	// A repeated id is returned (or reported missing) only once.
	req.IDs = dedupeStrings(req.IDs)
	properties, err := ParseSelectionWithDefault(args, "properties", spec.Model, spec.DynamicPrefix, spec.Defaults)
	if err != nil {
		return nil, err
	}
	var ordered []T
	var notFound []string
	if req.IDs == nil {
		ordered, err = spec.Load(ctx)
		if err != nil {
			return nil, err
		}
	} else if spec.LoadIDs != nil {
		ordered, notFound, err = spec.LoadIDs(ctx, req.IDs)
		if err != nil {
			return nil, err
		}
	} else {
		records, loadErr := spec.Load(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		byID := make(map[string]T, len(records))
		for _, record := range records {
			byID[spec.ID(record)] = record
		}
		ordered = make([]T, 0, len(req.IDs))
		for _, id := range req.IDs {
			record, ok := byID[id]
			if !ok {
				notFound = append(notFound, id)
				continue
			}
			ordered = append(ordered, record)
		}
	}

	list := make([]json.RawMessage, 0, len(ordered))
	for _, record := range ordered {
		rendered, err := spec.Render(ctx, record, properties, args)
		if err != nil {
			return nil, err
		}
		list = append(list, rendered)
	}

	result := GetResult{
		AccountID:   accountID,
		AccountLess: spec.AccountLess,
		List:        list,
		NotFound:    notFound,
	}
	if spec.State != nil {
		state, err := spec.State(ctx)
		if err != nil {
			return nil, err
		}
		result.State = &state
	}
	return result.Marshal()
}

// GetResult is the standard RFC 8620 section 5.1 response shape. Types whose
// id argument or record shape differs from the generic HandleGet (for example
// SearchSnippet/get, which uses emailIds and snippet records) still build this
// value and call Marshal so list/notFound assembly stays single-sourced.
type GetResult struct {
	AccountID   string
	AccountLess bool
	List        any
	NotFound    []string
	State       *string
}

// Marshal emits a /get response with arrays (never null) for list and notFound.
func (r GetResult) Marshal() (json.RawMessage, error) {
	resp := map[string]any{
		"list":     r.List,
		"notFound": append([]string{}, r.NotFound...),
	}
	if !r.AccountLess {
		resp["accountId"] = r.AccountID
	}
	if r.State != nil {
		resp["state"] = *r.State
	}
	return json.Marshal(resp)
}

// ChangesSpec describes a standard /changes method.
type ChangesSpec struct {
	Allowed map[string]bool
	// Sync refreshes the derived state before reading it. It is optional
	// because some types store their state directly in the change tracker.
	Sync func(ctx context.Context) error
	// Changes returns the already-assembled RFC 8620 section 5.2 response.
	Changes func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error)
}

// HandleChanges implements RFC 8620 section 5.2 for any JMAP data type.
func HandleChanges(ctx context.Context, accountID string, args json.RawMessage, spec ChangesSpec) (json.RawMessage, error) {
	if err := RejectUnknownArguments(args, spec.Allowed); err != nil {
		return nil, err
	}
	var req struct {
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, Errorf("invalidArguments", "sinceState must be a string")
	}
	if spec.Sync != nil {
		if err := spec.Sync(ctx); err != nil {
			return nil, err
		}
	}
	result, err := spec.Changes(ctx, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// QuerySpec describes the type-specific parts of /query and /queryChanges.
type QuerySpec struct {
	Allowed        map[string]bool
	ChangesAllowed map[string]bool
	// SortProperties lists the properties this type can sort by. An empty
	// map rejects every sort, which is correct for types with storage-defined
	// ordering.
	SortProperties map[string]bool
	// ParseFilter decodes the type's FilterCondition.
	ParseFilter func(args json.RawMessage) (any, error)
	// AllIDs returns the complete ordered result set for a filter. The raw
	// arguments are included for type-specific sort handling.
	AllIDs func(ctx context.Context, accountID string, args json.RawMessage, filter any, ascending bool) ([]string, error)
	// Extra validates type-specific arguments such as collapseThreads.
	Extra func(args json.RawMessage) error
	// Tracker and Kind identify the query state namespace.
	Tracker *Tracker
	Kind    StateKind
}

type queryArgs struct {
	Sort           []interface{} `json:"sort"`
	Position       *int          `json:"position"`
	Anchor         *string       `json:"anchor"`
	AnchorOffset   *int          `json:"anchorOffset"`
	Limit          *int          `json:"limit"`
	CalculateTotal *bool         `json:"calculateTotal"`
}

func (s QuerySpec) resolve(ctx context.Context, accountID string, args json.RawMessage) ([]string, queryArgs, string, error) {
	var req queryArgs
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, req, "", Errorf("invalidArguments", "query arguments must be an object")
	}
	ascending, err := SortAscending(req.Sort, s.SortProperties)
	if err != nil {
		return nil, req, "", err
	}
	filter, err := s.ParseFilter(args)
	if err != nil {
		return nil, req, "", err
	}
	if s.Extra != nil {
		if err := s.Extra(args); err != nil {
			return nil, req, "", err
		}
	}
	queryKey, err := QueryKey(filter, req.Sort)
	if err != nil {
		return nil, req, "", err
	}
	ids, err := s.AllIDs(ctx, accountID, args, filter, ascending)
	if err != nil {
		return nil, req, "", err
	}
	return ids, req, queryKey, nil
}

// HandleQuery implements RFC 8620 section 5.5 for any JMAP data type.
func HandleQuery(ctx context.Context, accountID string, args json.RawMessage, spec QuerySpec) (json.RawMessage, error) {
	if err := RejectUnknownArguments(args, spec.Allowed); err != nil {
		return nil, err
	}
	allIDs, req, queryKey, err := spec.resolve(ctx, accountID, args)
	if err != nil {
		return nil, err
	}
	start, end, err := PageBoundsForIDs(allIDs, req.Position, req.Limit, req.Anchor, req.AnchorOffset)
	if err != nil {
		return nil, err
	}
	queryState, err := spec.Tracker.SyncQuery(ctx, accountID, spec.Kind, queryKey, allIDs)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"accountId":           accountID,
		"queryState":          queryState,
		"canCalculateChanges": true,
		"position":            start,
		"ids":                 append([]string{}, allIDs[start:end]...),
	}
	// RFC 8620 section 5.4: total MUST be omitted unless requested.
	if req.CalculateTotal != nil && *req.CalculateTotal {
		resp["total"] = len(allIDs)
	}
	return json.Marshal(resp)
}

// HandleQueryChanges implements RFC 8620 section 5.6 for any JMAP data type.
func HandleQueryChanges(ctx context.Context, accountID string, args json.RawMessage, spec QuerySpec) (json.RawMessage, error) {
	if err := RejectUnknownArguments(args, spec.ChangesAllowed); err != nil {
		return nil, err
	}
	var req struct {
		SinceQueryState string  `json:"sinceQueryState"`
		MaxChanges      *int    `json:"maxChanges"`
		UpToID          *string `json:"upToId"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, Errorf("invalidArguments", "sinceQueryState must be a string")
	}
	// RFC 8620 section 5.6 makes upToId an optimisation: the server SHOULD
	// omit later changes only when both filter and sort use immutable
	// properties, and MUST otherwise ignore the argument. This server's
	// queries can sort/filter on mutable properties, so accepting upToId and
	// returning the complete diff is the compliant behaviour.
	_ = req.UpToID
	allIDs, queryReq, queryKey, err := spec.resolve(ctx, accountID, args)
	if err != nil {
		return nil, err
	}
	result, err := spec.Tracker.QueryChangesFrom(ctx, accountID, spec.Kind, queryKey, req.SinceQueryState, req.MaxChanges, allIDs)
	if err != nil {
		return nil, err
	}
	if queryReq.CalculateTotal != nil && *queryReq.CalculateTotal {
		result["total"] = len(allIDs)
	}
	return json.Marshal(result)
}

// SetSpec describes a standard RFC 8620 section 5.3 /set method. The kernel
// owns argument validation, the ifInState check, deterministic iteration and
// the exact response shape; the callbacks own only type-specific semantics.
type SetSpec struct {
	Allowed map[string]bool
	// State returns the type's current state token. It is called before and
	// after the mutations so oldState/newState always describe what happened.
	State func(ctx context.Context) (string, error)
	// Create applies one record. It returns the properties to place in the
	// created entry (including the server-assigned id), or a SetError.
	Create func(ctx context.Context, createID string, record json.RawMessage) (json.RawMessage, *SetError, error)
	// Update applies one patch. It returns updatedProperties, or a SetError.
	Update func(ctx context.Context, id string, patch json.RawMessage) (json.RawMessage, *SetError, error)
	// Destroy removes one record, or returns a SetError explaining why not.
	Destroy func(ctx context.Context, id string) *SetError
	// Extra validates type-specific arguments such as onDestroyRemoveEmails.
	Extra func(args json.RawMessage) error
	// Finalize runs after all create/update/destroy callbacks but before
	// newState is read. It lets a type refresh a derived state snapshot once
	// per request instead of from inside every mutation callback.
	Finalize func(ctx context.Context) error
}

type setRequest struct {
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[string]json.RawMessage `json:"update"`
	Destroy   []string                   `json:"destroy"`
}

// SetResult is the standard RFC 8620 section 5.3 response shape. Types whose
// orchestration cannot use HandleSet (the account-less PushSubscription, and
// EmailSubmission with its implicit Email/set) build this value and call
// Marshal, so no type package has to assemble the map by hand.
type SetResult struct {
	AccountID    string
	AccountLess  bool
	OldState     *string
	NewState     *string
	Created      any
	Updated      any
	Destroyed    []string
	NotCreated   any
	NotUpdated   any
	NotDestroyed any
}

// Marshal emits the /set response with null maps for empty results and an
// array (never null) for destroyed.
func (r SetResult) Marshal() (json.RawMessage, error) {
	resp := map[string]any{
		"created":      r.Created,
		"updated":      r.Updated,
		"destroyed":    append([]string{}, r.Destroyed...),
		"notCreated":   r.NotCreated,
		"notUpdated":   r.NotUpdated,
		"notDestroyed": r.NotDestroyed,
	}
	if !r.AccountLess {
		resp["accountId"] = r.AccountID
	}
	if r.OldState != nil {
		resp["oldState"] = *r.OldState
	}
	if r.NewState != nil {
		resp["newState"] = *r.NewState
	}
	return json.Marshal(resp)
}

// CopyImportResult is the response shape shared by Email/copy and
// Email/import (RFC 8621 sections 4.8 and 4.9).
type CopyImportResult struct {
	AccountID     string
	FromAccountID string
	OldState      string
	NewState      string
	Created       any
	NotCreated    any
	NotDestroyed  any
}

// Marshal emits a copy/import response with null maps for empty results.
func (r CopyImportResult) Marshal() (json.RawMessage, error) {
	resp := map[string]any{
		"accountId":  r.AccountID,
		"oldState":   r.OldState,
		"newState":   r.NewState,
		"created":    r.Created,
		"notCreated": r.NotCreated,
	}
	if r.FromAccountID != "" {
		resp["fromAccountId"] = r.FromAccountID
	}
	if r.NotDestroyed != nil {
		resp["notDestroyed"] = r.NotDestroyed
	}
	return json.Marshal(resp)
}

// CheckIfInState returns a stateMismatch method error when ifInState was
// supplied and no longer matches. Every /set method uses this so the error is
// identical across types.
func CheckIfInState(ifInState *string, currentState string) error {
	if ifInState != nil && *ifInState != currentState {
		return Errorf("stateMismatch", "state mismatch")
	}
	return nil
}

func dedupeStrings(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// HandleSet implements RFC 8620 section 5.3 for any JMAP data type. Maps with
// no entries are emitted as null, exactly as the specification requires.
func HandleSet(ctx context.Context, accountID string, args json.RawMessage, spec SetSpec) (json.RawMessage, error) {
	if err := RejectUnknownArguments(args, spec.Allowed); err != nil {
		return nil, err
	}
	if spec.Extra != nil {
		if err := spec.Extra(args); err != nil {
			return nil, err
		}
	}
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, Errorf("invalidArguments", "set arguments must be an object")
	}
	oldState, err := spec.State(ctx)
	if err != nil {
		return nil, err
	}
	if err := CheckIfInState(req.IfInState, oldState); err != nil {
		return nil, err
	}

	var created, updated, notCreated, notUpdated, notDestroyed map[string]json.RawMessage
	// destroyed is an RFC 8620 array (not a nullable map), so always emit an
	// array even when nothing was destroyed.
	destroyed := []string{}

	for _, cid := range sortedKeys(req.Create) {
		record, setErr, err := spec.Create(ctx, cid, req.Create[cid])
		if err != nil {
			return nil, err
		}
		if setErr != nil {
			if notCreated == nil {
				notCreated = make(map[string]json.RawMessage)
			}
			encoded, _ := json.Marshal(setErr)
			notCreated[cid] = encoded
			continue
		}
		if created == nil {
			created = make(map[string]json.RawMessage)
		}
		created[cid] = record
	}
	for _, id := range sortedKeys(req.Update) {
		record, setErr, err := spec.Update(ctx, id, req.Update[id])
		if err != nil {
			return nil, err
		}
		if setErr != nil {
			if notUpdated == nil {
				notUpdated = make(map[string]json.RawMessage)
			}
			encoded, _ := json.Marshal(setErr)
			notUpdated[id] = encoded
			continue
		}
		if updated == nil {
			updated = make(map[string]json.RawMessage)
		}
		updated[id] = record
	}
	for _, id := range req.Destroy {
		setErr := spec.Destroy(ctx, id)
		if setErr != nil {
			if notDestroyed == nil {
				notDestroyed = make(map[string]json.RawMessage)
			}
			encoded, _ := json.Marshal(setErr)
			notDestroyed[id] = encoded
			continue
		}
		destroyed = append(destroyed, id)
	}
	if spec.Finalize != nil {
		if err := spec.Finalize(ctx); err != nil {
			return nil, err
		}
	}

	newState, err := spec.State(ctx)
	if err != nil {
		return nil, err
	}
	return SetResult{
		AccountID:    accountID,
		OldState:     &oldState,
		NewState:     &newState,
		Created:      created,
		Updated:      updated,
		Destroyed:    destroyed,
		NotCreated:   notCreated,
		NotUpdated:   notUpdated,
		NotDestroyed: notDestroyed,
	}.Marshal()
}
