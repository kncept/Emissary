package push

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// durableState is the cold-start-safe part of the push hub. It reuses the
// revision-aware JMAP state store (the same family as the change tracker and
// created ids) so no second concurrency scheme is needed. A nil store makes
// every operation a no-op, preserving the in-memory-only behaviour used by
// unit tests that do not supply persistence.
type durableState struct {
	store storage.JMAPStateStore
}

// durableLast is the last state published for one account, keyed by type name.
type durableLast struct {
	Tokens map[string]string `json:"tokens"`
}

// durableWebhook is the rate-limit backoff and the pending changes collected
// while a webhook credential is backed off.
type durableWebhook struct {
	BackoffUntil time.Time                    `json:"backoffUntil,omitempty"`
	Pending      map[string]map[string]string `json:"pending,omitempty"` // account -> type -> token
}

func lastStateKey(accountID string) string       { return "push:last:" + accountID }
func webhookStateKey(credential string) string   { return "push:webhook:" + credential }
func credentialStateKey(accountID string) string { return "push:credential:" + accountID }

// encodeTokens stores state tokens by their RFC type name, matching the wire
// format of a StateChange and keeping the durable record stable if StateKind
// values are ever renumbered.
func encodeTokens(states map[base.StateKind]string) map[string]string {
	out := make(map[string]string, len(states))
	for kind, token := range states {
		if name, ok := typeNames[kind]; ok {
			out[name] = token
		}
	}
	return out
}

func decodeTokens(tokens map[string]string) map[base.StateKind]string {
	out := make(map[base.StateKind]string, len(tokens))
	for name, token := range tokens {
		for kind, typeName := range typeNames {
			if typeName == name {
				out[kind] = token
			}
		}
	}
	return out
}

func (d durableState) loadLast(ctx context.Context, accountID string) (map[string]string, error) {
	if d.store == nil {
		return nil, nil
	}
	data, _, err := d.store.LoadJMAPState(ctx, lastStateKey(accountID))
	if err != nil {
		return nil, fmt.Errorf("load push baseline for %q: %w", accountID, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var record durableLast
	if err := base.DecodeStateObject(data, &record); err != nil {
		return nil, fmt.Errorf("decode push baseline for %q: %w", accountID, err)
	}
	return record.Tokens, nil
}

func (d durableState) saveLast(ctx context.Context, accountID string, tokens map[string]string) error {
	if d.store == nil {
		return nil
	}
	data, err := json.Marshal(durableLast{Tokens: tokens})
	if err != nil {
		return err
	}
	return base.UpdateState(ctx, d.store, lastStateKey(accountID), func(existing []byte) ([]byte, bool, error) {
		// Avoid needless writes on every timer poll when nothing changed.
		if len(existing) != 0 {
			var current durableLast
			if err := json.Unmarshal(existing, &current); err != nil {
				return nil, false, err
			}
			if sameStringMap(current.Tokens, tokens) {
				return nil, false, nil
			}
		}
		return data, true, nil
	})
}

// loadCredentials returns the credentials known to own subscriptions for an
// account. It lets an instance that never saw the original JMAP request find a
// webhook created by another instance.
func (d durableState) loadCredentials(ctx context.Context, accountID string) ([]string, error) {
	if d.store == nil {
		return nil, nil
	}
	data, _, err := d.store.LoadJMAPState(ctx, credentialStateKey(accountID))
	if err != nil {
		return nil, fmt.Errorf("load push credentials for %q: %w", accountID, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var credentials []string
	if err := base.DecodeStateObject(data, &credentials); err != nil {
		return nil, fmt.Errorf("decode push credentials for %q: %w", accountID, err)
	}
	return credentials, nil
}

// rememberCredential records a credential/account pair under optimistic
// concurrency. The local cache is only a fast path; this record is what makes
// the association survive a cold start.
func (d durableState) rememberCredential(ctx context.Context, accountID, credential string) error {
	if d.store == nil {
		return nil
	}
	return base.UpdateState(ctx, d.store, credentialStateKey(accountID), func(data []byte) ([]byte, bool, error) {
		var credentials []string
		if err := base.DecodeStateObject(data, &credentials); err != nil {
			return nil, false, err
		}
		for _, existing := range credentials {
			if existing == credential {
				return nil, false, nil
			}
		}
		credentials = append(credentials, credential)
		next, err := json.Marshal(credentials)
		return next, true, err
	})
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// loadWebhook reads the durable backoff/pending record for a credential.
func (d durableState) loadWebhook(ctx context.Context, credential string) (durableWebhook, error) {
	var record durableWebhook
	if d.store == nil {
		return record, nil
	}
	data, _, err := d.store.LoadJMAPState(ctx, webhookStateKey(credential))
	if err != nil {
		return record, fmt.Errorf("load webhook state for %q: %w", credential, err)
	}
	if len(data) == 0 {
		return record, nil
	}
	if err := base.DecodeStateObject(data, &record); err != nil {
		return record, fmt.Errorf("decode webhook state for %q: %w", credential, err)
	}
	return record, nil
}

// updateWebhook applies mutate to the durable record under optimistic
// concurrency. mutate must be pure; returning changed=false skips the write.
func (d durableState) updateWebhook(ctx context.Context, credential string, mutate func(*durableWebhook) (bool, error)) error {
	if d.store == nil {
		return nil
	}
	return base.UpdateState(ctx, d.store, webhookStateKey(credential), func(data []byte) ([]byte, bool, error) {
		var record durableWebhook
		if err := base.DecodeStateObject(data, &record); err != nil {
			return nil, false, err
		}
		changed, err := mutate(&record)
		if err != nil || !changed {
			return nil, false, err
		}
		next, err := json.Marshal(record)
		return next, true, err
	})
}
