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
	record := fromStored(stored)
	return base.HandleGet(ctx, accountID, args, base.GetSpec[VacationResponse]{
		Allowed: base.ArgumentSet("accountId", "ids", "properties"),
		Model:   VacationResponse{},
		Load:    func(context.Context) ([]VacationResponse, error) { return []VacationResponse{record}, nil },
		ID:      func(VacationResponse) string { return SingletonID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, v VacationResponse, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(v)
		},
	})
}

func (h *Handler) VacationResponseSet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	email, err := h.accountEmail(ctx, accountID)
	if err != nil {
		return nil, err
	}
	stored, err := h.store().GetVacationResponse(ctx, email)
	if err != nil {
		return nil, err
	}

	return base.HandleSet(ctx, accountID, args, base.SetSpec{
		Allowed: base.ArgumentSet("accountId", "ifInState", "create", "update", "destroy"),
		State: func(ctx context.Context) (string, error) {
			return h.deps.Changes.State(ctx, accountID, base.StateVacation)
		},
		Create: func(_ context.Context, id string, _ json.RawMessage) (json.RawMessage, *base.SetError, error) {
			// The singleton always exists, so a create is rejected.
			err := base.SetError{}
			if id == SingletonID {
				err = base.AlreadyExists("the VacationResponse singleton already exists", SingletonID)
			} else {
				err = base.NewSetError("invalidProperties", "the only valid VacationResponse id is \"singleton\"")
			}
			return nil, &err, nil
		},
		Update: func(ctx context.Context, id string, patch json.RawMessage) (json.RawMessage, *base.SetError, error) {
			if id != SingletonID {
				setErr := base.NewSetError("notFound", "the only valid VacationResponse id is \"singleton\"")
				return nil, &setErr, nil
			}
			// RFC 8620 section 5.3: validate the PatchObject structure before
			// applying property semantics.
			currentJSON, _ := json.Marshal(fromStored(stored))
			if _, err := base.ApplyPatch(currentJSON, patch); err != nil {
				setErr := base.NewSetError("invalidPatch", err.Error())
				return nil, &setErr, nil
			}
			changes, err := handlePatch(patch)
			if err != nil {
				setErr := base.NewSetError("invalidProperties", err.Error())
				return nil, &setErr, nil
			}
			changes.apply(&stored)
			if err := h.store().SaveVacationResponse(ctx, email, stored); err != nil {
				return nil, nil, err
			}
			// Refresh the derived state so HandleSet's newState read sees the
			// mutation. The fingerprint makes a no-op update state-neutral.
			if _, err := h.syncState(ctx, accountID, stored); err != nil {
				return nil, nil, err
			}
			result, err := json.Marshal(fromStored(stored))
			return result, nil, err
		},
		Destroy: func(_ context.Context, id string) *base.SetError {
			err := base.NewSetError("notFound", "the only valid VacationResponse id is \"singleton\"")
			if id == SingletonID {
				err = base.NewSetError("notFound", "the VacationResponse singleton cannot be destroyed")
			}
			return &err
		},
	})
}
