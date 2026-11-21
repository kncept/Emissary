package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func TestMemoryJMAPStateStoreUsesRevisions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStorage()

	data, revision, err := store.LoadJMAPState(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if len(data) != 0 || revision != "" {
		t.Fatalf("empty load = %q / %q, want empty", data, revision)
	}

	revision, err = store.SaveJMAPState(ctx, "user@example.com", []byte("one"), "")
	if err != nil {
		t.Fatalf("SaveJMAPState() error = %v", err)
	}
	if revision == "" {
		t.Fatal("SaveJMAPState() returned an empty revision")
	}

	if _, err := store.SaveJMAPState(ctx, "user@example.com", []byte("stale"), ""); !errors.Is(err, ErrJMAPStateConflict) {
		t.Fatalf("stale save error = %v, want ErrJMAPStateConflict", err)
	}

	if _, err := store.SaveJMAPState(ctx, "user@example.com", []byte("two"), revision); err != nil {
		t.Fatalf("conditional save error = %v", err)
	}
	data, revision, err = store.LoadJMAPState(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if !bytes.Equal(data, []byte("two")) || revision == "" {
		t.Fatalf("load = %q / %q, want two", data, revision)
	}
}

type conditionalS3 struct {
	objects map[string]conditionalS3Object
}

type conditionalS3Object struct {
	data string
	etag string
}

func (f *conditionalS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	object, ok := f.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(object.data)),
		ETag: aws.String(object.etag),
	}, nil
}

func (f *conditionalS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(input.Key)
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	current, exists := f.objects[key]
	if exists && aws.ToString(input.IfNoneMatch) == "*" {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object exists"}
	}
	if input.IfMatch != nil && (!exists || aws.ToString(input.IfMatch) != current.etag) {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "etag mismatch"}
	}
	etag := fmt.Sprintf("\"etag-%d\"", len(f.objects)+1)
	f.objects[key] = conditionalS3Object{data: string(body), etag: etag}
	return &s3.PutObjectOutput{ETag: aws.String(etag)}, nil
}

func (f *conditionalS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}

func (f *conditionalS3) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return &s3.HeadObjectOutput{}, nil
}

func (f *conditionalS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return &s3.ListObjectsV2Output{}, nil
}

func (f *conditionalS3) CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	return &s3.CopyObjectOutput{}, nil
}

func TestS3JMAPStateStoreUsesETagConditionalWrites(t *testing.T) {
	ctx := context.Background()
	store := &S3Storage{
		client: &conditionalS3{objects: make(map[string]conditionalS3Object)},
		bucket: "test-bucket",
	}

	data, revision, err := store.LoadJMAPState(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if len(data) != 0 || revision != "" {
		t.Fatalf("empty load = %q / %q, want empty", data, revision)
	}

	revision, err = store.SaveJMAPState(ctx, "user@example.com", []byte("one"), "")
	if err != nil {
		t.Fatalf("SaveJMAPState() error = %v", err)
	}
	if revision == "" {
		t.Fatal("SaveJMAPState() returned an empty revision")
	}
	if _, err := store.SaveJMAPState(ctx, "user@example.com", []byte("stale"), ""); !errors.Is(err, ErrJMAPStateConflict) {
		t.Fatalf("stale save error = %v, want ErrJMAPStateConflict", err)
	}
	if _, err := store.SaveJMAPState(ctx, "user@example.com", []byte("stale"), `"wrong-etag"`); !errors.Is(err, ErrJMAPStateConflict) {
		t.Fatalf("wrong ETag save error = %v, want ErrJMAPStateConflict", err)
	}

	if _, err := store.SaveJMAPState(ctx, "user@example.com", []byte("two"), revision); err != nil {
		t.Fatalf("conditional save error = %v", err)
	}
	data, newRevision, err := store.LoadJMAPState(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if !bytes.Equal(data, []byte("two")) || newRevision == revision {
		t.Fatalf("load = %q / %q, want two with a new revision", data, newRevision)
	}
}

func TestFileJMAPStateStorePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	store, root := newTestFileStorage(t)

	revision, err := store.SaveJMAPState(ctx, "Alice@Example.com", []byte("one"), "")
	if err != nil {
		t.Fatalf("SaveJMAPState() error = %v", err)
	}

	restarted, err := NewFileStorage(root)
	if err != nil {
		t.Fatalf("NewFileStorage() error = %v", err)
	}
	data, loadedRevision, err := restarted.LoadJMAPState(ctx, "Alice@Example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if !bytes.Equal(data, []byte("one")) || loadedRevision != revision {
		t.Fatalf("load = %q / %q, want one / %q", data, loadedRevision, revision)
	}

	if _, err := store.SaveJMAPState(ctx, "Alice@Example.com", []byte("stale"), ""); !errors.Is(err, ErrJMAPStateConflict) {
		t.Fatalf("stale save error = %v, want ErrJMAPStateConflict", err)
	}
	if _, err := restarted.SaveJMAPState(ctx, "Alice@Example.com", []byte("two"), loadedRevision); err != nil {
		t.Fatalf("conditional save error = %v", err)
	}
	data, _, err = store.LoadJMAPState(ctx, "Alice@Example.com")
	if err != nil {
		t.Fatalf("LoadJMAPState() error = %v", err)
	}
	if !bytes.Equal(data, []byte("two")) {
		t.Fatalf("load = %q, want two", data)
	}
}
