// Package blob implements the Blob methods from RFC 8620 section 6.3.
package blob

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

// MethodCopy is the Blob/copy method name.
const MethodCopy = "Blob/copy"

// Methods returns the method names implemented by this package.
func Methods() []string { return []string{MethodCopy} }

// MethodHandler is the contract the dispatcher expects from Blob handlers.
type MethodHandler interface {
	BlobCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error)
}

// Handler implements the Blob methods.
type Handler struct{ deps *base.Deps }

// New creates a Blob method handler.
func New(deps *base.Deps) *Handler { return &Handler{deps: deps} }

var _ MethodHandler = (*Handler)(nil)

// copyRequest is the RFC 8620 argument set plus the two optional safeguards
// Emissary applies to copy operations described in the implementation plan.
// ifFromInState guards the source account's blob state; onSuccessDestroyOriginal
// removes a generic blob after it has been copied.
type copyRequest struct {
	FromAccountID            string   `json:"fromAccountId"`
	AccountID                string   `json:"accountId"`
	BlobIDs                  []string `json:"blobIds"`
	IfFromInState            *string  `json:"ifFromInState,omitempty"`
	OnSuccessDestroyOriginal bool     `json:"onSuccessDestroyOriginal,omitempty"`
}

type setError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type blobChange struct {
	accountID string
	id        string
	op        base.ChangeOp
}

func setErrorOf(typ, description string) setError {
	return setError{Type: typ, Description: description}
}

var copyArgumentNames = map[string]bool{
	"fromAccountId":            true,
	"accountId":                true,
	"blobIds":                  true,
	"ifFromInState":            true,
	"onSuccessDestroyOriginal": true,
}

// BlobCopy copies immutable blobs between accounts. Copies are performed
// server-side: the bytes are read from the source account and written to the
// destination through the generic blob store, so the two accounts stay scoped
// even though the destination blob id is a content digest shared by both.
func (h *Handler) BlobCopy(ctx context.Context, accountID string, args json.RawMessage) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, base.Errorf("invalidArguments", "Blob/copy arguments must be an object")
	}
	for name := range raw {
		if !copyArgumentNames[name] {
			return nil, base.Errorf("invalidArguments", "unknown argument %q for Blob/copy", name)
		}
	}
	var req copyRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, base.Errorf("invalidArguments", "invalid Blob/copy arguments")
	}
	if strings.TrimSpace(req.FromAccountID) == "" {
		return nil, base.Errorf("invalidArguments", "fromAccountId is required")
	}
	if strings.TrimSpace(req.AccountID) == "" {
		req.AccountID = accountID
	}
	if strings.TrimSpace(req.AccountID) == "" {
		return nil, base.Errorf("invalidArguments", "accountId is required")
	}
	if req.BlobIDs == nil {
		return nil, base.Errorf("invalidArguments", "blobIds is required")
	}

	// The dispatcher checks the destination account from `accountId`, but
	// fromAccountId is a method-specific argument and needs its own check.
	if !base.CanAccessAccount(ctx, req.FromAccountID) {
		return nil, base.Errorf("forbidden", "insufficient permissions to access account %s", req.FromAccountID)
	}
	if !base.CanAccessAccount(ctx, req.AccountID) {
		return nil, base.Errorf("forbidden", "insufficient permissions to access account %s", req.AccountID)
	}
	if !h.accountExists(ctx, req.FromAccountID) {
		return nil, base.Errorf("fromAccountNotFound", "from account %s does not exist", req.FromAccountID)
	}
	if !h.accountExists(ctx, req.AccountID) {
		return nil, base.Errorf("accountNotFound", "account %s does not exist", req.AccountID)
	}
	// RFC 8620 section 5.4 requires the destination of a copy to differ from
	// its source. Without this check, onSuccessDestroyOriginal would delete
	// the only copy while still reporting success.
	if req.FromAccountID == req.AccountID {
		return nil, base.Errorf("invalidArguments", "fromAccountId and accountId must be different")
	}

	if req.IfFromInState != nil {
		state, err := h.deps.Changes.State(ctx, req.FromAccountID, base.StateBlob)
		if err != nil {
			return nil, base.Errorf("serverFail", "could not read source blob state: %v", err)
		}
		if state != *req.IfFromInState {
			return nil, base.Errorf("stateMismatch", "source blob state has changed")
		}
	}

	copied := make(map[string]string)
	notCopied := make(map[string]setError)
	notDestroyed := make(map[string]setError)
	seen := make(map[string]bool, len(req.BlobIDs))
	var changes []blobChange

	for _, blobID := range req.BlobIDs {
		if seen[blobID] {
			continue
		}
		seen[blobID] = true

		// Pin the source blob for the duration of this copy. Blob ids are
		// references held by JMAP objects, and RFC 8620 requires that a blob
		// not be deleted inside the method call that still references it.
		// Email-backed blobs are not in the generic blob index, so they need
		// no pin (and DeleteBlob will not remove them anyway).
		pinned := false
		if _, err := h.deps.Storage.AddBlobRefs(ctx, req.FromAccountID, blobID, 1); err == nil {
			pinned = true
		} else if !errors.Is(err, storage.ErrNotFound) {
			notCopied[blobID] = setErrorOf("invalidProperties", "Could not pin blob")
			continue
		}
		releasePin := func() {
			if !pinned {
				return
			}
			_, _ = h.deps.Storage.AddBlobRefs(ctx, req.FromAccountID, blobID, -1)
			pinned = false
		}

		meta, data, err := h.deps.Storage.GetBlob(ctx, req.FromAccountID, blobID)
		if err != nil {
			releasePin()
			if errors.Is(err, storage.ErrNotFound) {
				notCopied[blobID] = setErrorOf("notFound", "Blob not found")
			} else {
				notCopied[blobID] = setErrorOf("invalidProperties", "Could not read blob")
			}
			continue
		}

		targetID := storage.BlobID(data)
		_, _, existingErr := h.deps.Storage.GetBlob(ctx, req.AccountID, targetID)
		existed := existingErr == nil

		newMeta, err := h.deps.Storage.PutBlob(ctx, req.AccountID, meta.Type, data)
		if err != nil {
			releasePin()
			notCopied[blobID] = setErrorOf("invalidProperties", "Could not store blob in destination account")
			continue
		}

		destroyed := false
		if req.OnSuccessDestroyOriginal {
			// Email-backed blob ids are opaque message ids. Destroying one
			// would delete a mail message, which is an Email/set operation,
			// so only generic _blobs entries are removed here.
			if meta.EmailBacked {
				// Deleting an email-backed blob would delete a mail message,
				// which is an Email/set operation. Report the skip explicitly
				// rather than silently claiming the original was destroyed.
				notDestroyed[blobID] = setErrorOf("forbidden", "email-backed blobs cannot be destroyed by Blob/copy")
			} else {
				releasePin()
				remaining, metaErr := h.deps.Storage.GetBlobMeta(ctx, req.FromAccountID, blobID)
				if metaErr != nil || remaining.IsPinned() {
					notDestroyed[blobID] = setErrorOf("forbidden", "blob is still referenced")
				} else if err := h.deps.Storage.DeleteBlob(ctx, req.FromAccountID, blobID); err != nil {
					if !existed {
						_ = h.deps.Storage.DeleteBlob(ctx, req.AccountID, newMeta.ID)
					}
					notCopied[blobID] = setErrorOf("invalidProperties", "Could not destroy original blob")
					continue
				} else {
					destroyed = true
				}
			}
		}
		releasePin()

		if !existed {
			changes = append(changes, blobChange{req.AccountID, newMeta.ID, base.ChangeCreate})
		}
		if destroyed {
			changes = append(changes, blobChange{req.FromAccountID, blobID, base.ChangeDestroy})
		}
		copied[blobID] = newMeta.ID
	}

	for _, change := range changes {
		if err := h.deps.Changes.Record(ctx, change.accountID, base.StateBlob, change.id, change.op); err != nil {
			return nil, base.Errorf("serverFail", "could not record blob state: %v", err)
		}
	}

	response := map[string]any{
		"fromAccountId": req.FromAccountID,
		"accountId":     req.AccountID,
	}
	if len(copied) > 0 {
		response["copied"] = copied
	} else {
		response["copied"] = nil
	}
	if len(notCopied) > 0 {
		response["notCopied"] = notCopied
	} else {
		response["notCopied"] = nil
	}
	if len(notDestroyed) > 0 {
		response["notDestroyed"] = notDestroyed
	}
	return json.Marshal(response)
}

// accountExists treats the caller's own account as inherently valid; a user
// cannot authenticate as an account the server does not know. Other accounts
// are checked through the user manager so masquerading callers cannot copy
// from or into a nonexistent account.
func (h *Handler) accountExists(ctx context.Context, accountID string) bool {
	if strings.TrimSpace(accountID) == "" {
		return false
	}
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil || claims.Subject == accountID {
		return true
	}
	if h.deps.UserMgr == nil {
		return false
	}
	user, err := h.deps.UserMgr.Lookup(ctx, accountID)
	return err == nil && user != nil
}
