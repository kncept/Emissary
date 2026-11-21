package metadata

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type contractBackend struct {
	name string
	open func(*testing.T) MetadataStore
}

// contractBackends is the single matrix used by the metadata contract tests.
// Ordering and read normalization must hold for every implementation.
func contractBackends() []contractBackend {
	return []contractBackend{
		{
			name: "file-legacy",
			open: func(t *testing.T) MetadataStore {
				store, err := NewFileMetadata(filepath.Join(t.TempDir(), "metadata.json"))
				if err != nil {
					t.Fatalf("NewFileMetadata(file) error = %v", err)
				}
				return store
			},
		},
		{
			name: "file-directory",
			open: func(t *testing.T) MetadataStore {
				store, err := NewFileMetadata(filepath.Join(t.TempDir(), "metadata"))
				if err != nil {
					t.Fatalf("NewFileMetadata(dir) error = %v", err)
				}
				return store
			},
		},
		{
			name: "memory",
			open: func(*testing.T) MetadataStore {
				return NewMemoryMetadata()
			},
		},
		{
			name: "ddb",
			open: func(*testing.T) MetadataStore {
				return newDdbTestMetadata(&fakeDdb{
					items:    make(map[string]map[string]types.AttributeValue),
					unsorted: true,
				})
			},
		},
	}
}

func TestMetadataContractListOrderingAndReadNormalization(t *testing.T) {
	ctx := context.Background()
	const userID = "alice@example.com"
	ids := []string{"mail-3", "mail-1", "mail-2"}
	want := []string{"mail-1", "mail-2", "mail-3"}

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			for _, id := range ids {
				if err := store.Save(ctx, NewEmailMetadata(userID, id, KeywordSeen)); err != nil {
					t.Fatalf("Save(%q) error = %v", id, err)
				}
			}
			if err := store.Save(ctx, NewEmailMetadata("bob@example.com", "other", KeywordFlagged)); err != nil {
				t.Fatalf("Save(other user) error = %v", err)
			}

			// List twice: map-order backends must not be accidentally sorted
			// correctly on only the first iteration.
			for attempt := 0; attempt < 2; attempt++ {
				got, err := store.List(ctx, userID)
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				if len(got) != len(want) {
					t.Fatalf("List() returned %d records, want %d: %+v", len(got), len(want), got)
				}
				for i, id := range want {
					if got[i].ID != id || got[i].UserID != userID {
						t.Errorf("List()[%d] = %+v, want %q for %q", i, got[i], id, userID)
					}
					if !got[i].HasKeyword(KeywordSeen) {
						t.Errorf("List()[%d] lost $seen: %+v", i, got[i])
					}
				}
			}

			for _, id := range ids {
				got, err := store.Get(ctx, userID, id)
				if err != nil {
					t.Fatalf("Get(%q) error = %v", id, err)
				}
				if got.ID != id || got.UserID != userID || !got.HasKeyword(KeywordSeen) {
					t.Errorf("Get(%q) = %+v, want normalized record", id, got)
				}
				if got.ReceivedAt == "" {
					t.Errorf("Get(%q) has empty receivedAt", id)
				}
			}

			empty, err := store.List(ctx, "nobody@example.com")
			if err != nil {
				t.Fatalf("List(nobody) error = %v", err)
			}
			if empty == nil || len(empty) != 0 {
				t.Errorf("List(nobody) = %+v, want empty non-nil slice", empty)
			}
		})
	}
}
