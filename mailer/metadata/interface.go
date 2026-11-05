package metadata

import (
	"context"
)

type EmailMetadata struct {
	ID      string
	Read    bool
	Flagged bool
}

type MetadataStore interface {
	Get(ctx context.Context, id string) (EmailMetadata, error)
	SetRead(ctx context.Context, id string, read bool) error
	SetFlagged(ctx context.Context, id string, flagged bool) error
	List(ctx context.Context) ([]EmailMetadata, error)
}