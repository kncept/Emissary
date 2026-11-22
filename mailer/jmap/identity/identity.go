// Package identity implements the Identity methods from RFC 8621 section 6.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"sort"
	"strings"

	"github.com/kncept/Emissary/mailer/components/storage"
	appidentity "github.com/kncept/Emissary/mailer/identity"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
	"github.com/kncept/Emissary/mailer/jmap/internal/model"
)

const (
	MethodGet     = "Identity/get"
	MethodSet     = "Identity/set"
	MethodChanges = "Identity/changes"

	// SingletonID is the id of the default identity every account starts with.
	// It is no longer the only identity an account may have.
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

// ErrIdentityNotFound reports an unknown identityId to callers such as
// EmailSubmission/set that need to resolve a JMAP Identity by id.
var ErrIdentityNotFound = errors.New("identity not found")

// Lookup resolves one Identity by id, including the legacy "primary" entry on
// accounts that have not materialised their collection yet. It lets other
// methods trust the same durable identity source that Identity/get uses.
func Lookup(ctx context.Context, deps *base.Deps, accountID, id string) (Identity, error) {
	h := New(deps)
	entries, _, err := h.loadEntries(ctx, accountID)
	if err != nil {
		return Identity{}, err
	}
	entry, ok := entries[id]
	if !ok {
		return Identity{}, ErrIdentityNotFound
	}
	// A wildcard identity permits any address in the domain. The narrow
	// profile sends as the account's own verified address in that case.
	if strings.HasPrefix(entry.Email, "*@") {
		domain := strings.ToLower(strings.TrimPrefix(entry.Email, "*@"))
		canonical, err := h.accountEmail(ctx, accountID)
		if err != nil {
			return Identity{}, err
		}
		if at := strings.LastIndex(canonical, "@"); at > 0 &&
			strings.EqualFold(canonical[at+1:], domain) {
			entry.Email = canonical
		}
	}
	return toIdentity(entry), nil
}

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

func storageAddresses(addresses *[]model.EmailAddress) []storage.IdentityAddress {
	if addresses == nil {
		return nil
	}
	out := make([]storage.IdentityAddress, 0, len(*addresses))
	for _, address := range *addresses {
		out = append(out, storage.IdentityAddress{Name: address.Name, Email: address.Email})
	}
	return out
}

func toIdentity(entry storage.IdentityEntry) Identity {
	name := entry.Name
	if name == "" {
		name = entry.Email
	}
	return Identity{
		ID:            entry.ID,
		Name:          name,
		Email:         entry.Email,
		ReplyTo:       identityAddresses(entry.ReplyTo),
		Bcc:           identityAddresses(entry.Bcc),
		TextSignature: entry.TextSignature,
		HTMLSignature: entry.HTMLSignature,
		MayDelete:     entry.MayDelete,
	}
}

// accountEmail resolves the address a legacy identity inherits from the user
// record. Accounts with no user (for example the built-in admin) still get an
// identity whose address is the account id.
func (h *Handler) accountEmail(ctx context.Context, accountID string) (string, error) {
	user, err := h.deps.UserMgr.Lookup(ctx, accountID)
	if err != nil {
		return "", err
	}
	if user != nil && user.Email != "" {
		return user.Email, nil
	}
	return accountID, nil
}

// loadEntries returns the durable collection, seeding the default identity
// from the legacy singleton overrides and the user record on first read. It
// deliberately does not write: a read-only /get must not mutate storage.
func (h *Handler) loadEntries(ctx context.Context, accountID string) (map[string]storage.IdentityEntry, string, error) {
	entries, revision, err := h.deps.Identities.Load(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	if len(entries) > 0 {
		return entries, revision, nil
	}
	email, err := h.accountEmail(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	legacy, _, err := h.deps.Identities.LoadLegacy(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	return map[string]storage.IdentityEntry{
		SingletonID: {
			ID:            SingletonID,
			Email:         email,
			Name:          legacy.Name,
			ReplyTo:       legacy.ReplyTo,
			Bcc:           legacy.Bcc,
			TextSignature: legacy.TextSignature,
			HTMLSignature: legacy.HTMLSignature,
			MayDelete:     false,
		},
	}, "", nil
}

func (h *Handler) allIdentities(ctx context.Context, accountID string) ([]Identity, error) {
	entries, _, err := h.loadEntries(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Identity, 0, len(ids))
	for _, id := range ids {
		out = append(out, toIdentity(entries[id]))
	}
	return out, nil
}

func (h *Handler) IdentityGet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	identities, err := h.allIdentities(ctx, accountID)
	if err != nil {
		return nil, err
	}
	state, err := h.syncIdentityState(ctx, accountID, identities)
	if err != nil {
		return nil, err
	}
	return base.HandleGet(ctx, accountID, args, base.GetSpec[Identity]{
		Allowed: base.ArgumentSet("accountId", "ids", "properties"),
		Model:   Identity{},
		Load:    func(context.Context) ([]Identity, error) { return identities, nil },
		ID:      func(identity Identity) string { return identity.ID },
		State:   func(context.Context) (string, error) { return state, nil },
		Render: func(_ context.Context, record Identity, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(record)
		},
	})
}

// SyncState refreshes the derived collection snapshot so push state reflects
// Identity/{get,set} changes.
func (h *Handler) SyncState(ctx context.Context, accountID string) (string, error) {
	identities, err := h.allIdentities(ctx, accountID)
	if err != nil {
		return "", err
	}
	return h.syncIdentityState(ctx, accountID, identities)
}

// syncIdentityState fingerprints every identity so Identity/changes reports a
// create, update or destroy by comparing the whole collection.
func (h *Handler) syncIdentityState(ctx context.Context, accountID string, identities []Identity) (string, error) {
	snapshot := make(map[string]string, len(identities))
	for _, identity := range identities {
		fingerprint, err := json.Marshal(identity)
		if err != nil {
			return "", err
		}
		snapshot[identity.ID] = string(fingerprint)
	}
	if err := h.deps.Changes.Sync(ctx, accountID, map[base.StateKind]map[string]string{
		base.StateIdentity: snapshot,
	}); err != nil {
		return "", err
	}
	return h.deps.Changes.State(ctx, accountID, base.StateIdentity)
}

var identityCreateFields = map[string]bool{
	"name": true, "replyTo": true, "bcc": true,
	"textSignature": true, "htmlSignature": true,
}

// applyIdentityFields decodes the client-settable properties shared by create
// and update. A JSON null clears a property, matching RFC 8620 patch semantics.
func applyIdentityFields(entry *storage.IdentityEntry, fields map[string]json.RawMessage) error {
	for name, value := range fields {
		if !identityCreateFields[name] {
			continue
		}
		if strings.TrimSpace(string(value)) == "null" {
			switch name {
			case "name":
				entry.Name = ""
			case "textSignature":
				entry.TextSignature = ""
			case "htmlSignature":
				entry.HTMLSignature = ""
			case "replyTo":
				entry.ReplyTo = nil
			case "bcc":
				entry.Bcc = nil
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
				entry.Name = decoded
			case "textSignature":
				entry.TextSignature = decoded
			case "htmlSignature":
				entry.HTMLSignature = decoded
			}
		case "replyTo", "bcc":
			var decoded []storage.IdentityAddress
			if err := json.Unmarshal(value, &decoded); err != nil {
				return base.Errorf("invalidProperties", "property %q must be an array of EmailAddress", name)
			}
			if name == "replyTo" {
				entry.ReplyTo = decoded
			} else {
				entry.Bcc = decoded
			}
		}
	}
	return nil
}

// duplicateEmailError carries the existing id required by alreadyExists.
type duplicateEmailError struct{ existingID string }

func (e duplicateEmailError) Error() string { return "identity email already exists: " + e.existingID }

// setErrorFrom preserves a declared JMAP error type and folds everything else
// into invalidProperties, the standard SetError for malformed properties.
func setErrorFrom(err error) base.SetError {
	if errors.Is(err, storage.ErrIdentityConflict) {
		return base.NewSetError("serverFail", "identity state changed concurrently")
	}
	typ, detail, found := strings.Cut(err.Error(), ": ")
	if found && base.IsSetErrorType(typ) {
		return base.NewSetError(typ, detail)
	}
	return base.InvalidProperties(err.Error())
}

// decodeCreate validates an Identity/create value without consulting durable
// state; the duplicate-address check happens inside the CAS loop.
func (h *Handler) decodeCreate(ctx context.Context, raw json.RawMessage) (storage.IdentityEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return storage.IdentityEntry{}, base.Errorf("invalidProperties", "Identity value must be an object")
	}
	for name := range fields {
		if name != "email" && !identityCreateFields[name] {
			return storage.IdentityEntry{}, base.Errorf("invalidProperties", "unknown or server-set property %q", name)
		}
	}
	rawEmail, ok := fields["email"]
	if !ok {
		return storage.IdentityEntry{}, base.Errorf("invalidProperties", "email is required")
	}
	var email string
	if err := json.Unmarshal(rawEmail, &email); err != nil {
		return storage.IdentityEntry{}, base.Errorf("invalidProperties", "email must be a string")
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || !strings.Contains(parsed.Address, "@") {
		return storage.IdentityEntry{}, base.Errorf("invalidProperties", "email must be a valid address")
	}
	email = parsed.Address
	if h.deps.FromValidator != nil {
		if err := h.deps.FromValidator.Validate(ctx, email); err != nil {
			if errors.Is(err, appidentity.ErrNotAllowed) {
				return storage.IdentityEntry{}, base.Errorf("forbiddenFrom", "cannot send from %q", email)
			}
			return storage.IdentityEntry{}, base.Errorf("serverFail", "could not verify %q: %v", email, err)
		}
	}
	entry := storage.IdentityEntry{ID: storage.NewIdentityID(), Email: email, MayDelete: true}
	if err := applyIdentityFields(&entry, fields); err != nil {
		return storage.IdentityEntry{}, err
	}
	return entry, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// mutate applies fn to a freshly loaded collection until compare-and-swap
// succeeds. Each attempt reloads, so a batch of creates/updates sees earlier
// changes made by the same request and competing Lambda invocations.
func (h *Handler) mutate(ctx context.Context, accountID string, fn func(map[string]storage.IdentityEntry) error) error {
	for attempt := 0; attempt < 8; attempt++ {
		entries, revision, err := h.loadEntries(ctx, accountID)
		if err != nil {
			return err
		}
		if err := fn(entries); err != nil {
			return err
		}
		_, saveErr := h.deps.Identities.Save(ctx, accountID, entries, revision)
		if saveErr == nil {
			return nil
		}
		if !errors.Is(saveErr, storage.ErrIdentityConflict) {
			return saveErr
		}
	}
	return storage.ErrIdentityConflict
}

func (h *Handler) IdentitySet(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	// Seed (or refresh) the snapshot before any mutation so oldState really is
	// the state before this /set. Without this, a first-ever create would see
	// the tracker's initial baseline as both oldState and newState.
	before, err := h.allIdentities(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if _, err := h.syncIdentityState(ctx, accountID, before); err != nil {
		return nil, err
	}
	return base.HandleSet(ctx, accountID, args, base.SetSpec{
		Allowed: base.ArgumentSet("accountId", "ifInState", "create", "update", "destroy"),
		State: func(ctx context.Context) (string, error) {
			return h.deps.Changes.State(ctx, accountID, base.StateIdentity)
		},
		Create: func(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			entry, err := h.decodeCreate(ctx, raw)
			if err != nil {
				setErr := setErrorFrom(err)
				return nil, &setErr, nil
			}
			err = h.mutate(ctx, accountID, func(entries map[string]storage.IdentityEntry) error {
				for _, existing := range entries {
					if normalizeEmail(existing.Email) == normalizeEmail(entry.Email) {
						return duplicateEmailError{existingID: existing.ID}
					}
				}
				entries[entry.ID] = entry
				return nil
			})
			if err != nil {
				var duplicate duplicateEmailError
				if errors.As(err, &duplicate) {
					setErr := base.AlreadyExists("an identity for "+entry.Email+" already exists", duplicate.existingID)
					return nil, &setErr, nil
				}
				setErr := setErrorFrom(err)
				return nil, &setErr, nil
			}
			result, err := json.Marshal(toIdentity(entry))
			if err != nil {
				return nil, nil, err
			}
			return result, nil, nil
		},
		Update: func(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, *base.SetError, error) {
			entries, _, err := h.loadEntries(ctx, accountID)
			if err != nil {
				return nil, nil, err
			}
			current, ok := entries[id]
			if !ok {
				setErr := base.NewSetError("notFound", "unknown Identity id")
				return nil, &setErr, nil
			}
			currentJSON, err := json.Marshal(toIdentity(current))
			if err != nil {
				return nil, nil, err
			}
			if _, err := base.ApplyPatch(currentJSON, raw); err != nil {
				setErr := base.NewSetError("invalidPatch", err.Error())
				return nil, &setErr, nil
			}
			var updated storage.IdentityEntry
			err = h.mutate(ctx, accountID, func(fresh map[string]storage.IdentityEntry) error {
				entry, exists := fresh[id]
				if !exists {
					return base.Errorf("notFound", "unknown Identity id")
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					return base.Errorf("invalidPatch", "patch must be an object")
				}
				for name := range fields {
					switch name {
					case "id", "mayDelete":
						return base.Errorf("invalidProperties", "property %q is server-set", name)
					case "email":
						var decoded string
						if err := json.Unmarshal(fields[name], &decoded); err != nil {
							return base.Errorf("invalidProperties", "email must be a string")
						}
						if normalizeEmail(decoded) != normalizeEmail(entry.Email) {
							return base.Errorf("forbiddenFrom", "cannot change email to %q", decoded)
						}
					}
				}
				if err := applyIdentityFields(&entry, fields); err != nil {
					return err
				}
				fresh[id] = entry
				updated = entry
				return nil
			})
			if err != nil {
				setErr := setErrorFrom(err)
				return nil, &setErr, nil
			}
			result, err := json.Marshal(toIdentity(updated))
			return result, nil, err
		},
		Destroy: func(ctx context.Context, id string) *base.SetError {
			entries, _, err := h.loadEntries(ctx, accountID)
			if err != nil {
				se := base.NewSetError("serverFail", err.Error())
				return &se
			}
			entry, ok := entries[id]
			if !ok {
				se := base.NewSetError("notFound", "unknown Identity id")
				return &se
			}
			if !entry.MayDelete {
				se := base.NewSetError("forbidden", "this identity may not be deleted")
				return &se
			}
			if err := h.mutate(ctx, accountID, func(fresh map[string]storage.IdentityEntry) error {
				if _, exists := fresh[id]; !exists {
					return base.Errorf("notFound", "unknown Identity id")
				}
				delete(fresh, id)
				return nil
			}); err != nil {
				se := setErrorFrom(err)
				return &se
			}
			return nil
		},
		Finalize: func(ctx context.Context) error {
			identities, err := h.allIdentities(ctx, accountID)
			if err != nil {
				return err
			}
			_, err = h.syncIdentityState(ctx, accountID, identities)
			return err
		},
	})
}

func (h *Handler) IdentityChanges(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	return base.HandleChanges(ctx, accountID, args, base.ChangesSpec{
		Allowed: base.ArgumentSet("accountId", "sinceState", "maxChanges"),
		Sync: func(ctx context.Context) error {
			identities, err := h.allIdentities(ctx, accountID)
			if err != nil {
				return err
			}
			_, err = h.syncIdentityState(ctx, accountID, identities)
			return err
		},
		Changes: func(ctx context.Context, sinceState string, maxChanges *int) (map[string]any, error) {
			return h.deps.Changes.ChangesFrom(ctx, accountID, base.StateIdentity, sinceState, maxChanges)
		},
	})
}
