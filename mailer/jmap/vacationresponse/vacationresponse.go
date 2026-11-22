// Package vacationresponse implements the VacationResponse methods from RFC
// 8621 section 8.
package vacationresponse

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
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

var (
	// vacationPatchProperties is the full VacationResponse object. RFC 8620
	// section 5.3 lets a PatchObject repeat unchanged server-set properties,
	// so id is accepted here and validated as immutable by ValidatePatch.
	vacationPatchProperties = base.ArgumentSet(
		"id", "isEnabled", "fromDate", "toDate", "subject", "textBody", "htmlBody")
	vacationCreateProperties = base.ArgumentSet(
		"isEnabled", "fromDate", "toDate", "subject", "textBody", "htmlBody")
	vacationImmutableProperties = base.ArgumentSet("id")
)

// applyVacationChanges applies the properties ValidatePatch reports as
// changed. RFC 8621 section 8 defines isEnabled as non-nullable; every other
// property accepts null to return to its default. Invalid values are returned
// as property names so the caller can build one invalidProperties SetError.
func applyVacationChanges(v *usermanager.VacationResponse, changed map[string]json.RawMessage) []string {
	invalid := make([]string, 0)
	for name, raw := range changed {
		isNull := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		switch name {
		case "id":
			// Handled by the immutable-property check in ValidatePatch.
		case "isEnabled":
			var value bool
			if isNull || json.Unmarshal(raw, &value) != nil {
				invalid = append(invalid, name)
				continue
			}
			v.IsEnabled = value
		case "fromDate", "toDate":
			var value *time.Time
			if !isNull {
				var parsed time.Time
				if json.Unmarshal(raw, &parsed) != nil {
					invalid = append(invalid, name)
					continue
				}
				parsed = parsed.UTC()
				value = &parsed
			}
			if name == "fromDate" {
				v.FromDate = value
			} else {
				v.ToDate = value
			}
		case "subject", "textBody", "htmlBody":
			var value *string
			if !isNull {
				var parsed string
				if json.Unmarshal(raw, &parsed) != nil {
					invalid = append(invalid, name)
					continue
				}
				value = &parsed
			}
			switch name {
			case "subject":
				v.Subject = value
			case "textBody":
				v.TextBody = value
			case "htmlBody":
				v.HTMLBody = value
			}
		}
	}
	return invalid
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
	// Make the state token known even for an account that has never called
	// /get, so ifInState and oldState are meaningful on the first /set.
	if _, err := h.syncState(ctx, accountID, stored); err != nil {
		return nil, err
	}
	final := stored

	return base.HandleSet(ctx, accountID, args, base.SetSpec{
		Allowed: base.ArgumentSet("accountId", "ifInState", "create", "update", "destroy"),
		State: func(ctx context.Context) (string, error) {
			return h.deps.Changes.State(ctx, accountID, base.StateVacation)
		},
		Create: func(_ context.Context, creationID string, record json.RawMessage) (json.RawMessage, *base.SetError, error) {
			if bad := base.ValidateCreateProperties(
				map[string]json.RawMessage{creationID: record},
				vacationCreateProperties, vacationImmutableProperties); len(bad[creationID]) > 0 {
				setErr := base.InvalidProperties("Invalid create properties", bad[creationID]...)
				return nil, &setErr, nil
			}
			// RFC 8620 section 5.3 defines the singleton SetError for create
			// and destroy on singleton types. RFC 8621 section 8 requires
			// exactly one VacationResponse per account.
			setErr := base.NewSetError("singleton", "VacationResponse is a singleton and cannot be created")
			return nil, &setErr, nil
		},
		Update: func(ctx context.Context, id string, patch json.RawMessage) (json.RawMessage, *base.SetError, error) {
			if id != SingletonID {
				setErr := base.NewSetError("notFound", "the only valid VacationResponse id is \"singleton\"")
				return nil, &setErr, nil
			}
			currentJSON, err := json.Marshal(fromStored(stored))
			if err != nil {
				return nil, nil, err
			}
			validation, err := base.ValidatePatch(currentJSON, patch,
				vacationPatchProperties, vacationImmutableProperties)
			if err != nil {
				setErr := base.NewSetError("invalidPatch", err.Error())
				return nil, &setErr, nil
			}
			invalid := append([]string{}, validation.Unknown...)
			invalid = append(invalid, validation.Immutable...)
			next := stored
			invalid = append(invalid, applyVacationChanges(&next, validation.Changed)...)
			if len(invalid) > 0 {
				sort.Strings(invalid)
				setErr := base.InvalidProperties("Invalid VacationResponse properties", invalid...)
				return nil, &setErr, nil
			}
			if err := h.store().SaveVacationResponse(ctx, email, next); err != nil {
				return nil, nil, err
			}
			stored = next
			final = next
			// RFC 8620 section 5.3: an updated entry contains only properties
			// changed implicitly by the server. Nothing else changes here.
			return json.RawMessage("{}"), nil, nil
		},
		Destroy: func(_ context.Context, id string) *base.SetError {
			err := base.NewSetError("notFound", "the only valid VacationResponse id is \"singleton\"")
			if id == SingletonID {
				err = base.NewSetError("singleton", "VacationResponse is a singleton and cannot be destroyed")
			}
			return &err
		},
		// Sync once per request from the final value. A no-op patch therefore
		// leaves the state token unchanged even if several updates are sent.
		Finalize: func(ctx context.Context) error {
			_, err := h.syncState(ctx, accountID, final)
			return err
		},
	})
}
