package receiver

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
)

type dedupFakeS3 struct {
	objects map[string][]byte
	headErr error
}

func (f *dedupFakeS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body, ok := f.objects[aws.ToString(params.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func (f *dedupFakeS3) HeadObject(_ context.Context, params *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if _, ok := f.objects[aws.ToString(params.Key)]; !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *dedupFakeS3) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(params.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *dedupFakeS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func TestDedupID(t *testing.T) {
	id := DedupID("ses-key", "Alice@Example.com")
	if id != DedupID("ses-key", "alice@example.com") {
		t.Error("recipient comparison should be case-insensitive")
	}
	if id == DedupID("other-key", "alice@example.com") {
		t.Error("different staging keys must have different ids")
	}
	if id == DedupID("ses-key", "bob@example.com") {
		t.Error("different recipients must have different ids")
	}
	if strings.Contains(id, "ses-key") || strings.Contains(id, "alice@example.com") {
		t.Errorf("id %q should not contain raw key or address", id)
	}
}

func TestMemoryDedup(t *testing.T) {
	store := NewMemoryDedup()
	id := DedupID("key", "alice@example.com")
	if seen, err := store.Seen(context.Background(), id); err != nil || seen {
		t.Fatalf("Seen() = %t, %v; want false, nil", seen, err)
	}
	if err := store.Record(context.Background(), id); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if seen, err := store.Seen(context.Background(), id); err != nil || !seen {
		t.Fatalf("Seen() after Record = %t, %v; want true, nil", seen, err)
	}
	if err := store.Clear(context.Background(), id); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if seen, err := store.Seen(context.Background(), id); err != nil || seen {
		t.Fatalf("Seen() after Clear = %t, %v; want false, nil", seen, err)
	}
}

func TestS3DedupRecordSeenClear(t *testing.T) {
	client := &dedupFakeS3{objects: make(map[string][]byte)}
	store := NewS3Dedup(client, "bucket")
	id := DedupID("ses-key", "alice@example.com")

	if seen, err := store.Seen(context.Background(), id); err != nil || seen {
		t.Fatalf("Seen() = %t, %v; want false, nil", seen, err)
	}
	if err := store.Record(context.Background(), id); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	markerKey := dedupObjectPrefix + id
	if _, ok := client.objects[markerKey]; !ok {
		t.Fatalf("marker %q was not written", markerKey)
	}
	if seen, err := store.Seen(context.Background(), id); err != nil || !seen {
		t.Fatalf("Seen() after Record = %t, %v; want true, nil", seen, err)
	}
	if err := store.Clear(context.Background(), id); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if seen, err := store.Seen(context.Background(), id); err != nil || seen {
		t.Fatalf("Seen() after Clear = %t, %v; want false, nil", seen, err)
	}
}

func TestS3DedupMissingMarkerVariants(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "not found", err: &types.NotFound{}},
		{name: "no such key", err: &types.NoSuchKey{}},
		{name: "generic api error", err: &smithy.GenericAPIError{Code: "NotFound"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := NewS3Dedup(&dedupFakeS3{headErr: tt.err}, "bucket")
			seen, err := store.Seen(context.Background(), DedupID("key", "alice@example.com"))
			if err != nil || seen {
				t.Fatalf("Seen() = %t, %v; want false, nil", seen, err)
			}
		})
	}
}

func TestS3DedupPropagatesLookupErrors(t *testing.T) {
	store := NewS3Dedup(&dedupFakeS3{headErr: errors.New("access denied")}, "bucket")
	seen, err := store.Seen(context.Background(), DedupID("key", "alice@example.com"))
	if err == nil || seen {
		t.Fatalf("Seen() = %t, %v; want an error", seen, err)
	}
}

func TestNewReceiverUsesMemoryDedupByDefault(t *testing.T) {
	r, err := NewReceiver(ReceiverConfig{
		Domain:      "example.com",
		UserManager: fakeUserManager{},
		Sender:      sender.NewInMemoryRecording(),
		Storage:     storage.NewMemoryStorage(),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	if _, ok := r.cfg.Dedup.(*MemoryDedup); !ok {
		t.Errorf("Dedup = %T, want *MemoryDedup", r.cfg.Dedup)
	}
}
