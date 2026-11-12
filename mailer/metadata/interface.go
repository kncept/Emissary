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
	// Name returns the name of the metadata store implementation.
	Name() string

	// Get retrieves the metadata for the given email ID.
	Get(ctx context.Context, id string) (EmailMetadata, error)

	// Save stores the complete metadata for the given email ID.
	Save(ctx context.Context, metadata EmailMetadata) error

	// Delete discards all state for the given id. Deleting an already
	// absent id is not an error.
	Delete(ctx context.Context, id string) error

	// List returns all stored metadata entries.
	List(ctx context.Context) ([]EmailMetadata, error)
}