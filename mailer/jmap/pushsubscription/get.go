package pushsubscription

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

var getArgumentNames = map[string]bool{
	"ids":        true,
	"properties": true,
}

// PushSubscriptionGet returns only subscriptions created with the current
// credentials. It is account-less: no accountId is accepted and no state token
// is returned.
func (h *Handler) PushSubscriptionGet(ctx context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	unlock := h.lockCredential(CredentialID(ctx))
	defer unlock()

	if err := validateArguments(args, getArgumentNames); err != nil {
		return nil, err
	}
	if err := rejectPrivateProperties(args); err != nil {
		return nil, err
	}
	selection, err := base.ParseSelection(args, "properties", view{}, "")
	if err != nil {
		return nil, err
	}

	subs, err := h.load(ctx, CredentialID(ctx))
	if err != nil {
		return nil, base.Errorf("serverFail", "could not load push subscriptions: %v", err)
	}
	byID := make(map[string]storage.PushSubscription, len(subs))
	for _, sub := range subs {
		byID[sub.ID] = sub
	}

	var request struct {
		IDs []string `json:"ids,omitempty"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, base.Errorf("invalidArguments", "invalid ids argument")
	}

	list := make([]view, 0, len(subs))
	notFound := []string{}
	if request.IDs == nil {
		for _, sub := range subs {
			list = append(list, toView(sub))
		}
	} else {
		seen := make(map[string]bool, len(request.IDs))
		for _, id := range request.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			sub, ok := byID[id]
			if !ok {
				notFound = append(notFound, id)
				continue
			}
			list = append(list, toView(sub))
		}
	}

	filtered, err := selection.Filter(list)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"list":     filtered,
		"notFound": notFound,
	})
}

// rejectPrivateProperties implements RFC 8620 section 7.2.1: asking for url or
// keys explicitly is forbidden even though a normal /get would include them.
func rejectPrivateProperties(args json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return base.Errorf("invalidArguments", "arguments must be an object")
	}
	value, ok := raw["properties"]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil
	}
	var names []string
	if err := json.Unmarshal(value, &names); err != nil {
		return base.Errorf("invalidArguments", "properties must be an array of strings or null")
	}
	for _, name := range names {
		if name == "url" || name == "keys" {
			return base.Errorf("forbidden", "the %s property may not be returned", name)
		}
	}
	return nil
}
