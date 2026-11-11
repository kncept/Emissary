package metadata

import (
	"context"
)

// EmailMetadata is the per-email state tracked by a MetadataStore.
// The struct tags keep the JSON and DynamoDB representations in sync.
type EmailMetadata struct {
	ID      string `json:"id" dynamodbav:"id"`
	Read    bool   `json:"read" dynamodbav:"read"`
	Flagged bool   `json:"flagged" dynamodbav:"flagged"`
}

type MetadataStore interface {
	Get(ctx context.Context, id string) (EmailMetadata, error)
	SetRead(ctx context.Context, id string, read bool) error
	SetFlagged(ctx context.Context, id string, flagged bool) error
	// Delete discards all state for the given id. Deleting an already
	// absent id is not an error.
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]EmailMetadata, error)
}