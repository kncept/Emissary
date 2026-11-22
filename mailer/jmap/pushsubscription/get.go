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
	subs, err := h.load(ctx, CredentialID(ctx))
	if err != nil {
		return nil, base.Errorf("serverFail", "could not load push subscriptions: %v", err)
	}

	return base.HandleGet(ctx, "", args, base.GetSpec[storage.PushSubscription]{
		Allowed:     getArgumentNames,
		Model:       view{},
		AccountLess: true,
		Load:        func(context.Context) ([]storage.PushSubscription, error) { return subs, nil },
		ID:          func(sub storage.PushSubscription) string { return sub.ID },
		Render: func(_ context.Context, sub storage.PushSubscription, selection *base.Selection, _ json.RawMessage) (json.RawMessage, error) {
			return selection.Filter(toView(sub))
		},
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
