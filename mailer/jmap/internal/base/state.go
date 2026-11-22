package base

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kncept/Emissary/mailer/components/storage"
)

// UpdateState loads a revisioned state blob, applies mutate and saves it with
// optimistic-concurrency retries. mutate must be pure with respect to the
// supplied bytes: a conflict discards them and the mutator is called again
// with the newer state. Returning changed=false skips the write entirely.
//
// This is the same durable state family used by Tracker and the JMAP created-id
// store; push subscriptions reuse it rather than adding a second concurrency
// scheme. The key is a synthetic JMAP-state account id, so storage remains
// entirely responsible for ETag/Lock/version handling.
func UpdateState(ctx context.Context, store storage.JMAPStateStore, key string, mutate func(data []byte) (next []byte, changed bool, err error)) error {
	if store == nil {
		return errors.New("state store is required")
	}
	var lastErr error
	for attempt := 0; attempt < stateSaveRetries; attempt++ {
		data, revision, err := store.LoadJMAPState(ctx, key)
		if err != nil {
			return fmt.Errorf("load state %q: %w", key, err)
		}
		next, changed, err := mutate(data)
		if err != nil || !changed {
			return err
		}
		_, err = store.SaveJMAPState(ctx, key, next, revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrJMAPStateConflict) {
			return fmt.Errorf("save state %q: %w", key, err)
		}
		lastErr = err
	}
	return fmt.Errorf("save state %q after %d retries: %w", key, stateSaveRetries, lastErr)
}

// DecodeStateObject unmarshals a state blob into an object, treating a missing
// blob as an empty one. Push state blobs are always JSON objects.
func DecodeStateObject(data []byte, target any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, target)
}
