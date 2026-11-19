package receiver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestBaseMailIdentityConfig(t *testing.T) {
	got := BaseMailIdentityConfig()
	groups, err := got.Get("anyone@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := []HandlerRules{{MailIdentity: FallbackIdentity, Rules: []HandlerRule{DeliverToUser, Bounce}}}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("Get() = %+v, want %+v", groups, want)
	}
}

func TestMailIdentityConfigGet(t *testing.T) {
	config := MailIdentityConfig{
		"alias@example.com": []HandlerRules{{Rules: []HandlerRule{Bounce}}},
		FallbackIdentity:    []HandlerRules{{Rules: []HandlerRule{DeliverToUser, Bounce}}},
	}

	got, err := config.Get("ALIAS@Example.com")
	if err != nil {
		t.Fatalf("Get(configured) error = %v", err)
	}
	if len(got) != 1 || got[0].Rules[0] != Bounce {
		t.Errorf("Get(configured) = %+v, want Bounce", got)
	}

	got, err = config.Get("unknown@example.com")
	if err != nil {
		t.Fatalf("Get(fallback) error = %v", err)
	}
	if len(got) != 1 || len(got[0].Rules) != 2 {
		t.Errorf("Get(fallback) = %+v, want the base rules", got)
	}

	if _, err := (MailIdentityConfig{}).Get("anyone@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("Get() without fallback error = %v, want ErrIdentityConfigNotFound", err)
	}
}

func TestMailIdentityConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  MailIdentityConfig
		wantErr string
	}{
		{name: "base config", config: BaseMailIdentityConfig()},
		{name: "missing fallback", config: MailIdentityConfig{}, wantErr: "fallback"},
		{
			name:    "empty rule groups",
			config:  MailIdentityConfig{FallbackIdentity: []HandlerRules{}},
			wantErr: "no rule groups",
		},
		{
			name:    "unknown rule",
			config:  MailIdentityConfig{FallbackIdentity: []HandlerRules{{Rules: []HandlerRule{"Nope"}}}},
			wantErr: "unknown handler rule",
		},
		{
			name:    "empty key",
			config:  MailIdentityConfig{" ": []HandlerRules{{Rules: []HandlerRule{Bounce}}}, FallbackIdentity: []HandlerRules{{Rules: []HandlerRule{Bounce}}}},
			wantErr: "key cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMailIdentityConfigSeedsFallback(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryConfigStore()

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	groups, err := config.Get("nobody@example.com")
	if err != nil || len(groups) != 1 || len(groups[0].Rules) != 2 {
		t.Fatalf("Get() = %+v, %v; want base rules", groups, err)
	}
	stored, err := store.Get(ctx, FallbackIdentity)
	if err != nil || len(stored) != 1 || stored[0].MailIdentity != FallbackIdentity {
		t.Fatalf("stored fallback = %+v, %v; want seeded base rules", stored, err)
	}

	// Loading again is idempotent and must not duplicate the fallback.
	keys, err := store.List(ctx)
	if err != nil || !reflect.DeepEqual(keys, []string{FallbackIdentity}) {
		t.Fatalf("List() = %v, %v; want only %q", keys, err, FallbackIdentity)
	}
}

func TestLoadMailIdentityConfigLoadsEveryConfiguration(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryConfigStore()
	aliasRules := []HandlerRules{{Rules: []HandlerRule{Bounce}}}
	if err := store.Put(ctx, "Alias@Example.com", aliasRules); err != nil {
		t.Fatal(err)
	}

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	got, err := config.Get("alias@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(got, aliasRules) {
		t.Errorf("Get() = %+v, want %+v", got, aliasRules)
	}
	if _, ok := config[FallbackIdentity]; !ok {
		t.Error("fallback was not seeded alongside existing configurations")
	}
}

func TestFileConfigStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileConfigStore(root)
	if err != nil {
		t.Fatalf("NewFileConfigStore() error = %v", err)
	}
	rules := []HandlerRules{{MailIdentity: "catchall@example.com", Rules: []HandlerRule{DeliverToMailIdentity}}}
	if err := store.Put(ctx, "alias@example.com", rules); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	path := filepath.Join(root, configPrefix, "alias@example.com.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file %q not written: %v", path, err)
	}
	got, err := store.Get(ctx, "ALIAS@example.com")
	if err != nil || !reflect.DeepEqual(got, rules) {
		t.Fatalf("Get() = %+v, %v; want %+v", got, err, rules)
	}
	if _, err := store.Get(ctx, "missing@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrIdentityConfigNotFound", err)
	}

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	if _, ok := config[FallbackIdentity]; !ok {
		t.Error("fallback was not seeded in the file store")
	}
}

func TestNewConfigStore(t *testing.T) {
	store, err := NewConfigStore(ConfigStoreOptions{})
	if err != nil || store.Name() != "memory" {
		t.Fatalf("NewConfigStore(\"\") = %v, %v; want memory", store, err)
	}
	store, err = NewConfigStore(ConfigStoreOptions{Type: "filesystem", FilesystemDir: t.TempDir()})
	if err != nil || store.Name() != "file" {
		t.Fatalf("NewConfigStore(filesystem) = %v, %v; want file", store, err)
	}
	if _, err := NewConfigStore(ConfigStoreOptions{Type: "s3"}); err == nil {
		t.Error("NewConfigStore(s3) without a bucket should fail")
	}
	if _, err := NewConfigStore(ConfigStoreOptions{Type: "ddb"}); err == nil {
		t.Error("NewConfigStore(ddb) without a table should fail")
	}
}

// fakeConfigS3 is the S3 subset used by the receiver config store.
type fakeConfigS3 struct {
	objects map[string][]byte
}

func (f *fakeConfigS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body, ok := f.objects[aws.ToString(params.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("no such key")}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *fakeConfigS3) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(params.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeConfigS3) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(params.Prefix)
	output := &s3.ListObjectsV2Output{}
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			output.Contents = append(output.Contents, types.Object{Key: aws.String(key)})
		}
	}
	return output, nil
}

func TestS3ConfigStoreUsesReceiverPrefix(t *testing.T) {
	ctx := context.Background()
	client := &fakeConfigS3{objects: map[string][]byte{}}
	store := newS3ConfigStore(client, "bucket")

	if _, err := LoadMailIdentityConfig(ctx, store); err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	key := configPrefix + "/" + FallbackIdentity + ".json"
	if _, ok := client.objects[key]; !ok {
		t.Fatalf("seeded object %q not found; objects = %v", key, client.objects)
	}

	rules := []HandlerRules{{Rules: []HandlerRule{Bounce}}}
	if err := store.Put(ctx, "blocked@example.com", rules); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	keys, err := store.List(ctx)
	if err != nil || !reflect.DeepEqual(keys, []string{FallbackIdentity, "blocked@example.com"}) {
		t.Fatalf("List() = %v, %v; want both identities", keys, err)
	}
	got, err := store.Get(ctx, "blocked@example.com")
	if err != nil || !reflect.DeepEqual(got, rules) {
		t.Fatalf("Get() = %+v, %v; want %+v", got, err, rules)
	}
}

// fakeConfigDdb is the DynamoDB subset used by the receiver config store. It
// keys items solely by their "id" attribute, matching the table's PK-only
// design, and paginates Scan one item at a time to exercise the loop.
type fakeConfigDdb struct {
	items map[string]map[string]ddbtypes.AttributeValue
}

func (f *fakeConfigDdb) GetItem(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	id := params.Key["id"].(*ddbtypes.AttributeValueMemberS).Value
	return &dynamodb.GetItemOutput{Item: f.items[id]}, nil
}

func (f *fakeConfigDdb) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	id := params.Item["id"].(*ddbtypes.AttributeValueMemberS).Value
	if f.items == nil {
		f.items = make(map[string]map[string]ddbtypes.AttributeValue)
	}
	f.items[id] = params.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeConfigDdb) Scan(_ context.Context, params *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	start := ""
	if len(params.ExclusiveStartKey) > 0 {
		start = params.ExclusiveStartKey["id"].(*ddbtypes.AttributeValueMemberS).Value
	}
	keys := make([]string, 0, len(f.items))
	for key := range f.items {
		if key > start {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	output := &dynamodb.ScanOutput{}
	if len(keys) > 0 {
		key := keys[0]
		output.Items = []map[string]ddbtypes.AttributeValue{f.items[key]}
		output.LastEvaluatedKey = map[string]ddbtypes.AttributeValue{
			"id": &ddbtypes.AttributeValueMemberS{Value: key},
		}
	}
	return output, nil
}

func TestDdbConfigStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := &fakeConfigDdb{items: make(map[string]map[string]ddbtypes.AttributeValue)}
	store := newDdbConfigStore(client, "emissary-receiver")

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	seeded, ok := client.items[FallbackIdentity]
	if !ok {
		t.Fatalf("fallback item %q not written; items = %v", FallbackIdentity, client.items)
	}
	// The table needs only the "id" partition key; rules are one list attr.
	if len(seeded) != 2 || seeded["id"] == nil || seeded["rules"] == nil {
		t.Errorf("seeded item attributes = %v, want only id and rules", seeded)
	}
	groups, err := config.Get("nobody@example.com")
	if err != nil || len(groups) != 1 || len(groups[0].Rules) != 2 {
		t.Fatalf("Get(fallback) = %+v, %v; want base rules", groups, err)
	}

	rules := []HandlerRules{{MailIdentity: "catchall@example.com", Rules: []HandlerRule{DeliverToMailIdentity}}}
	if err := store.Put(ctx, "Alias@Example.com", rules); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := store.Get(ctx, "ALIAS@example.com")
	if err != nil || !reflect.DeepEqual(got, rules) {
		t.Fatalf("Get(case-insensitive) = %+v, %v; want %+v", got, err, rules)
	}
	if _, err := store.Get(ctx, "missing@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrIdentityConfigNotFound", err)
	}

	keys, err := store.List(ctx)
	wantKeys := []string{FallbackIdentity, "alias@example.com"}
	if err != nil || !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("List() = %v, %v; want %v", keys, err, wantKeys)
	}

	// Loading an already-seeded store must not create a second fallback.
	if _, err := LoadMailIdentityConfig(ctx, store); err != nil {
		t.Fatalf("second LoadMailIdentityConfig() error = %v", err)
	}
	keys, err = store.List(ctx)
	if err != nil || !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("List() after reload = %v, %v; want %v", keys, err, wantKeys)
	}
}
