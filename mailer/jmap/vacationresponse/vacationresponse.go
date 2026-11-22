// Package vacationresponse implements the VacationResponse methods from RFC
// 8621 section 8.
package vacationresponse

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const (
	MethodGet = "VacationResponse/get"
	MethodSet = "VacationResponse/set"

	// SingletonID is the only valid id for a VacationResponse object.
	SingletonID = "singleton"
)

// Methods returns the method names implemented by this package. RFC 8621
// defines only /get and /set; there is no /changes method.
func Methods() []string { return []string{MethodGet, MethodSet} }

// MethodHandler is the contract the dispatcher expects from VacationResponse
// handlers.
type MethodHandler interface {
	VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the VacationResponse methods. It reads the store from
// deps on every call so the composition root can replace it with the same
// instance the receiver uses.
type Handler struct{ deps *base.Deps }

// New creates a VacationResponse method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

func (h *Handler) store() usermanager.VacationStore { return h.deps.VacationStore }

var _ MethodHandler = (*Handler)(nil)

// VacationResponse is the JMAP representation of the singleton. All fields
// except id are nullable so an unset value is returned as null rather than
// being omitted, as required by RFC 8621 section 8.
type VacationResponse struct {
	ID        string     `json:"id"`
	IsEnabled bool       `json:"isEnabled"`
	FromDate  *time.Time `json:"fromDate"`
	ToDate    *time.Time `json:"toDate"`
	Subject   *string    `json:"subject"`
	TextBody  *string    `json:"textBody"`
	HTMLBody  *string    `json:"htmlBody"`
}

func fromStored(v usermanager.VacationResponse) VacationResponse {
	return VacationResponse{
		ID:        SingletonID,
		IsEnabled: v.IsEnabled,
		FromDate:  v.FromDate,
		ToDate:    v.ToDate,
		Subject:   v.Subject,
		TextBody:  v.TextBody,
		HTMLBody:  v.HTMLBody,
	}
}

// updatePatch is the set of mutable properties accepted by /set. Pointer
// fields carry a supplied non-null value; cleared records the properties whose
// PatchObject value was explicitly null. JSON unmarshalling alone cannot tell
// an absent property from a null one, because both yield a nil pointer.
type updatePatch struct {
	IsEnabled *bool      `json:"isEnabled,omitempty"`
	FromDate  *time.Time `json:"fromDate,omitempty"`
	ToDate    *time.Time `json:"toDate,omitempty"`
	Subject   *string    `json:"subject,omitempty"`
	TextBody  *string    `json:"textBody,omitempty"`
	HTMLBody  *string    `json:"htmlBody,omitempty"`
	cleared   map[string]bool
}

func (p updatePatch) apply(v *usermanager.VacationResponse) {
	if p.IsEnabled != nil {
		v.IsEnabled = *p.IsEnabled
	}
	if p.FromDate != nil {
		t := p.FromDate.UTC()
		v.FromDate = &t
	} else if p.cleared["fromDate"] {
		v.FromDate = nil
	}
	if p.ToDate != nil {
		t := p.ToDate.UTC()
		v.ToDate = &t
	} else if p.cleared["toDate"] {
		v.ToDate = nil
	}
	if p.Subject != nil {
		s := *p.Subject
		v.Subject = &s
	} else if p.cleared["subject"] {
		v.Subject = nil
	}
	if p.TextBody != nil {
		s := *p.TextBody
		v.TextBody = &s
	} else if p.cleared["textBody"] {
		v.TextBody = nil
	}
	if p.HTMLBody != nil {
		s := *p.HTMLBody
		v.HTMLBody = &s
	} else if p.cleared["htmlBody"] {
		v.HTMLBody = nil
	}
}

// handlePatch decodes a create/update value, rejecting properties that are
// not part of the singleton. RFC 8620 requires invalidProperties for unknown
// or unmodifiable fields.
func handlePatch(raw json.RawMessage) (updatePatch, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return updatePatch{}, base.Errorf("invalidProperties", "VacationResponse value must be an object")
	}
	allowed := map[string]bool{
		"isEnabled": true,
		"fromDate":  true,
		"toDate":    true,
		"subject":   true,
		"textBody":  true,
		"htmlBody":  true,
	}
	patch := updatePatch{cleared: make(map[string]bool)}
	for name, value := range fields {
		if !allowed[name] {
			return updatePatch{}, base.Errorf("invalidProperties", "unknown or immutable property %q", name)
		}
		if string(value) == "null" {
			if name == "isEnabled" {
				return updatePatch{}, base.Errorf("invalidProperties", "isEnabled is not nullable")
			}
			patch.cleared[name] = true
		}
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		return updatePatch{}, base.Errorf("invalidProperties", "invalid VacationResponse value")
	}
	return patch, nil
}

func setError(typ, description string) map[string]string {
	return map[string]string{"type": typ, "description": description}
}

// fingerprint derives the value used to detect changes to the singleton so
// the RFC 8620 state token only advances when the settings actually change.
func fingerprint(v usermanager.VacationResponse) string {
	raw, err := json.Marshal(fromStored(v))
	if err != nil {
		return ""
	}
	return string(raw)
}

// SyncState refreshes the singleton's state from the durable vacation store.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	email, err := h.accountEmail(ctx, accountID)
	if err != nil {
		return "", err
	}
	stored, err := h.store().GetVacationResponse(ctx, email)
	if err != nil {
		return "", err
	}
	return h.syncState(ctx, accountID, stored)
}

func (h *Handler) syncState(ctx context.Context, accountID string, v usermanager.VacationResponse) (string, error) {
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateVacation: {SingletonID: fingerprint(v)},
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateVacation)
}

// accountEmail resolves the account id to the canonical user email used as
// the persistence key.
func (h *Handler) accountEmail(ctx context.Context, accountID string) (string, error) {
	user, err := h.deps.UserMgr.Lookup(ctx, accountID)
	if err != nil {
		return "", err
	}
	if user == nil || user.Email == "" {
		return accountID, nil
	}
	return user.Email, nil
}

func (h *Handler) VacationResponseGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IDs []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", VacationResponse{}, "")
	if err != nil {
		return nil, err
	}

	email, err := h.accountEmail(ctx, accountID)
	if err != nil {
		return nil, err
	}
	stored, err := h.store().GetVacationResponse(ctx, email)
	if err != nil {
		return nil, err
	}
	state, err := h.syncState(ctx, accountID, stored)
	if err != nil {
		return nil, err
	}

	list := []VacationResponse{}
	notFound := []string{}
	found := false
	for _, id := range req.IDs {
		if id == SingletonID {
			if !found {
				list = append(list, fromStored(stored))
				found = true
			}
		} else {
			notFound = append(notFound, id)
		}
	}
	if len(req.IDs) == 0 {
		list = append(list, fromStored(stored))
	}

	selected, err := properties.Filter(list)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"accountId": accountID,
		"state":     state,
		"list":      selected,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

func (h *Handler) VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IfInState *string                    `json:"ifInState,omitempty"`
		Create    map[string]json.RawMessage `json:"create,omitempty"`
		Update    map[string]json.RawMessage `json:"update,omitempty"`
		Destroy   []string                   `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateVacation)
	if err != nil {
		return nil, err
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, base.Errorf("stateMismatch", "VacationResponse state mismatch")
	}

	created := map[string]VacationResponse{}
	updated := map[string]VacationResponse{}
	destroyed := []string{}
	notCreated := map[string]map[string]string{}
	notUpdated := map[string]map[string]string{}
	notDestroyed := map[string]map[string]string{}

	email, err := h.accountEmail(ctx, accountID)
	if err != nil {
		return nil, err
	}
	stored, err := h.store().GetVacationResponse(ctx, email)
	if err != nil {
		return nil, err
	}

	// The singleton always exists, so a create is rejected with alreadyExists.
	for id := range req.Create {
		if id == SingletonID {
			notCreated[id] = setError("alreadyExists", "the VacationResponse singleton already exists")
		} else {
			notCreated[id] = setError("invalidProperties", "the only valid VacationResponse id is \"singleton\"")
		}
	}

	for id, raw := range req.Update {
		if id != SingletonID {
			notUpdated[id] = setError("notFound", "the only valid VacationResponse id is \"singleton\"")
			continue
		}
		patch, err := handlePatch(raw)
		if err != nil {
			notUpdated[id] = setError("invalidProperties", err.Error())
			continue
		}
		patch.apply(&stored)
	}

	for _, id := range req.Destroy {
		if id == SingletonID {
			notDestroyed[id] = setError("notFound", "the VacationResponse singleton cannot be destroyed")
		} else {
			notDestroyed[id] = setError("notFound", "the only valid VacationResponse id is \"singleton\"")
		}
	}

	// The singleton can only be changed through update. A no-op update is still
	// reported as successful, but the state token stays unchanged because the
	// fingerprint is identical.
	_, updateFailed := notUpdated[SingletonID]
	if _, requested := req.Update[SingletonID]; requested && !updateFailed {
		if err := h.store().SaveVacationResponse(ctx, email, stored); err != nil {
			return nil, err
		}
		updated[SingletonID] = fromStored(stored)
	}

	newState, err := h.syncState(ctx, accountID, stored)
	if err != nil {
		return nil, err
	}

	resp := map[string]any{
		"accountId":    accountID,
		"oldState":     oldState,
		"newState":     newState,
		"created":      created,
		"updated":      updated,
		"destroyed":    destroyed,
		"notCreated":   notCreated,
		"notUpdated":   notUpdated,
		"notDestroyed": notDestroyed,
	}
	return json.Marshal(resp)
}
