// Package identity implements the Identity methods from RFC 8621 section 6.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

const (
	MethodGet     = "Identity/get"
	MethodSet     = "Identity/set"
	MethodChanges = "Identity/changes"

	// SingletonID is the only identity Emissary exposes.
	SingletonID = "primary"
)

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodGet, MethodSet, MethodChanges} }

// MethodHandler is the contract the dispatcher expects from Identity handlers.
type MethodHandler interface {
	IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
	IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Identity methods.
type Handler struct{ deps *base.Deps }

// New creates an Identity method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

// Identity represents a JMAP Identity object. replyTo and bcc are nullable
// (RFC 8621 section 6.1), so a nil pointer marshals to JSON null rather than
// the empty array an empty slice would produce.
type Identity struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Email         string                `json:"email"`
	ReplyTo       *[]model.EmailAddress `json:"replyTo"`
	Bcc           *[]model.EmailAddress `json:"bcc"`
	TextSignature string                `json:"textSignature"`
	HTMLSignature string                `json:"htmlSignature"`
	MayDelete     bool                  `json:"mayDelete"`
}

var _ MethodHandler = (*Handler)(nil)

// identityAddresses returns nil for an empty list so the JSON value is null,
// matching the RFC 8621 default for replyTo and bcc.
func identityAddresses(addresses []storage.IdentityAddress) *[]model.EmailAddress {
	if len(addresses) == 0 {
		return nil
	}
	out := make([]model.EmailAddress, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, model.EmailAddress{Name: address.Name, Email: address.Email})
	}
	return &out
}

// loadIdentity builds the one identity from the immutable user email and the
// durable client-settable overrides. A missing user (for example the built-in
// admin account) still yields an identity whose address is the account id.
func (h *Handler) loadIdentity(ctx context.Context, accountID string) (Identity, error) {
	user, err := h.deps.UserMgr.Lookup(ctx, accountID)
	if err != nil {
		return Identity{}, err
	}
	address := accountID
	if user != nil && user.Email != "" {
		address = user.Email
	}
	record, _, err := h.deps.Identities.Load(ctx, accountID)
	if err != nil {
		return Identity{}, err
	}
	name := record.Name
	if name == "" {
		name = address
	}
	return Identity{
		ID:            SingletonID,
		Name:          name,
		Email:         address,
		ReplyTo:       identityAddresses(record.ReplyTo),
		Bcc:           identityAddresses(record.Bcc),
		TextSignature: record.TextSignature,
		HTMLSignature: record.HTMLSignature,
		MayDelete:     false,
	}, nil
}

func (h *Handler) IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IDs []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}
	properties, err := base.ParseSelection(args, "properties", Identity{}, "")
	if err != nil {
		return nil, err
	}

	identity, err := h.loadIdentity(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var list []Identity
	var notFound []string
	if len(req.IDs) > 0 {
		for _, id := range req.IDs {
			if id == SingletonID {
				list = append(list, identity)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, identity)
	}

	selected, err := properties.Filter(list)
	if err != nil {
		return nil, err
	}
	state, err := h.syncIdentityState(ctx, accountID, identity)
	if err != nil {
		return nil, err
	}
	resp := map[string]interface{}{
		"accountId": accountID,
		"state":     state,
		"list":      selected,
		"notFound":  notFound,
	}
	return json.Marshal(resp)
}

// SyncState refreshes the derived singleton identity from the user record and
// the durable overrides so push state reflects Identity/set changes.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	identity, err := h.loadIdentity(ctx, accountID)
	if err != nil {
		return "", err
	}
	return h.syncIdentityState(ctx, accountID, identity)
}

// syncIdentityState fingerprints the derived identity so Identity/changes
// reports an update when the underlying record changes.
func (h *Handler) syncIdentityState(ctx context.Context, accountID string, identity Identity) (string, error) {
	fingerprint, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateIdentity: {identity.ID: string(fingerprint)},
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateIdentity)
}

// identityPatchFields lists the properties a client may change on the
// singleton. Email and mayDelete are server-owned and cannot be patched.
var identityPatchFields = map[string]bool{
	"name":          true,
	"replyTo":       true,
	"bcc":           true,
	"textSignature": true,
	"htmlSignature": true,
}

// applyIdentityPatch decodes a PatchObject. A JSON null clears the property,
// which is how RFC 8620 removes replyTo, bcc or a signature. email is
// server-owned: a matching value is a no-op, but a different address is
// reported as forbiddenFrom because the user may not send from it.
func applyIdentityPatch(record *storage.IdentityRecord, raw json.RawMessage, expectedEmail string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return base.Errorf("invalidProperties", "Identity value must be an object")
	}
	for name, value := range fields {
		if name == "email" {
			var decoded string
			if err := json.Unmarshal(value, &decoded); err != nil {
				return base.Errorf("invalidProperties", "property \"email\" must be a string")
			}
			if decoded != expectedEmail {
				return base.Errorf("forbiddenFrom", "cannot send from %q", decoded)
			}
			continue
		}
		if !identityPatchFields[name] {
			return base.Errorf("invalidProperties", "unknown or immutable property %q", name)
		}
		if string(value) == "null" {
			switch name {
			case "name":
				record.Name = ""
			case "replyTo":
				record.ReplyTo = nil
			case "bcc":
				record.Bcc = nil
			case "textSignature":
				record.TextSignature = ""
			case "htmlSignature":
				record.HTMLSignature = ""
			}
			continue
		}
		switch name {
		case "name", "textSignature", "htmlSignature":
			var decoded string
			if err := json.Unmarshal(value, &decoded); err != nil {
				return base.Errorf("invalidProperties", "property %q must be a string", name)
			}
			switch name {
			case "name":
				record.Name = decoded
			case "textSignature":
				record.TextSignature = decoded
			case "htmlSignature":
				record.HTMLSignature = decoded
			}
		case "replyTo", "bcc":
			var decoded []storage.IdentityAddress
			if err := json.Unmarshal(value, &decoded); err != nil {
				return base.Errorf("invalidProperties", "property %q must be an array of EmailAddress", name)
			}
			if name == "replyTo" {
				record.ReplyTo = decoded
			} else {
				record.Bcc = decoded
			}
		}
	}
	return nil
}

func identitySetError(typ, description string) map[string]string {
	return map[string]string{"type": typ, "description": description}
}

// patchSetError preserves the typed error from applyIdentityPatch (currently
// forbiddenFrom) and folds every other failure into invalidProperties, which
// is the only other SetError the singleton update can produce.
func patchSetError(err error) map[string]string {
	typ, detail, found := strings.Cut(err.Error(), ": ")
	if !found || typ != "forbiddenFrom" {
		return identitySetError("invalidProperties", err.Error())
	}
	return identitySetError(typ, detail)
}

func (h *Handler) IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		IfInState *string                    `json:"ifInState,omitempty"`
		Create    map[string]json.RawMessage `json:"create,omitempty"`
		Update    map[string]json.RawMessage `json:"update,omitempty"`
		Destroy   []string                   `json:"destroy,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	oldState, err := h.deps.Changes.State(ctx, accountID, base.StateIdentity)
	if err != nil {
		return nil, err
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, base.Errorf("stateMismatch", "Identity state mismatch")
	}

	created := map[string]Identity{}
	updated := map[string]Identity{}
	destroyed := []string{}
	notCreated := map[string]map[string]string{}
	notUpdated := map[string]map[string]string{}
	notDestroyed := map[string]map[string]string{}

	// The canonical email is immutable; load it once so an attempted email
	// change can be checked against the address the user actually owns.
	identity, err := h.loadIdentity(ctx, accountID)
	if err != nil {
		return nil, err
	}

	// There is exactly one identity and it always exists. Creation and
	// destruction are therefore rejected; only update is meaningful.
	for id := range req.Create {
		notCreated[id] = identitySetError("alreadyExists", "the singleton identity already exists")
	}
	for _, id := range req.Destroy {
		notDestroyed[id] = identitySetError("forbidden", "the singleton identity cannot be destroyed")
	}

	for id, raw := range req.Update {
		if id != SingletonID {
			notUpdated[id] = identitySetError("notFound", "the only valid Identity id is \"primary\"")
			continue
		}
		// Load, patch and save inside each attempt so a conflict re-applies
		// the patch to the freshly loaded record instead of a stale one.
		for attempt := 0; attempt < 8; attempt++ {
			record, revision, err := h.deps.Identities.Load(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if err := applyIdentityPatch(&record, raw, identity.Email); err != nil {
				notUpdated[id] = patchSetError(err)
				break
			}
			_, saveErr := h.deps.Identities.Save(ctx, accountID, record, revision)
			if saveErr == nil {
				break
			}
			if !errors.Is(saveErr, storage.ErrIdentityConflict) {
				return nil, saveErr
			}
		}
	}

	// The persistent overrides may have changed during the update loop.
	identity, err = h.loadIdentity(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if _, failed := notUpdated[SingletonID]; !failed {
		if _, requested := req.Update[SingletonID]; requested {
			updated[SingletonID] = identity
		}
	}

	newState, err := h.syncIdentityState(ctx, accountID, identity)
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

func (h *Handler) IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		SinceState string `json:"sinceState"`
		MaxChanges *int   `json:"maxChanges,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, err
	}

	identity, err := h.loadIdentity(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if _, err := h.syncIdentityState(ctx, accountID, identity); err != nil {
		return nil, err
	}
	result, err := h.deps.Changes.ChangesFrom(ctx, accountID, base.StateIdentity, req.SinceState, req.MaxChanges)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
