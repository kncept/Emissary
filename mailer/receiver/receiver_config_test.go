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
	config := BaseMailIdentityConfig()
	identity := MailIdentityForRecipient("anyone@example.com")
	if identity != "example.com" {
		t.Fatalf("MailIdentityForRecipient() = %q, want example.com", identity)
	}
	got, err := config.GetByIdentity(identity)
	if err != nil {
		t.Fatalf("GetByIdentity() error = %v", err)
	}
	want := HandlerRules{MailIdentity: FallbackIdentity, Rules: []HandlerRule{DeliverToUser, Bounce}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetByIdentity() = %+v, want %+v", got, want)
	}
}

func TestMailIdentityForRecipient(t *testing.T) {
	tests := []struct {
		recipient string
		want      string
	}{
		{recipient: "alias@example.com", want: "example.com"},
		{recipient: "ALIAS@Example.COM", want: "example.com"},
		{recipient: " alias@news.example.com ", want: "news.example.com"},
		{recipient: "alias@local@example.com", want: "example.com"},
		{recipient: "alias@", want: FallbackIdentity},
		{recipient: "alias", want: FallbackIdentity},
		{recipient: "", want: FallbackIdentity},
	}
	for _, tt := range tests {
		if got := MailIdentityForRecipient(tt.recipient); got != tt.want {
			t.Errorf("MailIdentityForRecipient(%q) = %q, want %q", tt.recipient, got, tt.want)
		}
	}
}

func TestMailIdentityConfigFetchByRecipientIdentity(t *testing.T) {
	config := MailIdentityConfig{
		"news.example.com": {MailIdentity: "news.example.com", Rules: []HandlerRule{Drop}},
		FallbackIdentity:   {Rules: []HandlerRule{DeliverToUser, Bounce}},
	}

	// A recipient is always routed by the identity derived from its address,
	// never by the full address or by an unrelated identity in the config.
	identity := MailIdentityForRecipient("ALIAS@News.Example.com")
	got, err := config.GetByIdentity(identity)
	if err != nil {
		t.Fatalf("GetByIdentity(configured) error = %v", err)
	}
	if len(got.Rules) != 1 || got.Rules[0] != Drop {
		t.Errorf("GetByIdentity(configured) = %+v, want Drop", got)
	}

	got, err = config.GetByIdentity(MailIdentityForRecipient("unknown@other.example"))
	if err != nil {
		t.Fatalf("GetByIdentity(fallback) error = %v", err)
	}
	if len(got.Rules) != 2 {
		t.Errorf("GetByIdentity(fallback) = %+v, want the base rules", got)
	}

	got, err = config.GetByIdentity(MailIdentityForRecipient("no-at-sign"))
	if err != nil {
		t.Fatalf("GetByIdentity(no hostname) error = %v", err)
	}
	if len(got.Rules) != 2 {
		t.Errorf("GetByIdentity(no hostname) = %+v, want the base rules", got)
	}

	if _, err := (MailIdentityConfig{}).GetByIdentity(MailIdentityForRecipient("anyone@example.com")); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("GetByIdentity() without fallback error = %v, want ErrIdentityConfigNotFound", err)
	}
}

func TestMailIdentityConfigGetByIdentity(t *testing.T) {
	config := MailIdentityConfig{
		"alias@example.com": {Rules: []HandlerRule{Bounce}},
		FallbackIdentity:    {Rules: []HandlerRule{DeliverToUser, Bounce}},
	}

	got, err := config.GetByIdentity("ALIAS@Example.com")
	if err != nil {
		t.Fatalf("GetByIdentity(configured) error = %v", err)
	}
	if len(got.Rules) != 1 || got.Rules[0] != Bounce {
		t.Errorf("GetByIdentity(configured) = %+v, want Bounce", got)
	}

	got, err = config.GetByIdentity("unknown@example.com")
	if err != nil {
		t.Fatalf("GetByIdentity(fallback) error = %v", err)
	}
	if len(got.Rules) != 2 {
		t.Errorf("GetByIdentity(fallback) = %+v, want the base rules", got)
	}

	if _, err := (MailIdentityConfig{}).GetByIdentity("anyone@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("GetByIdentity() without fallback error = %v, want ErrIdentityConfigNotFound", err)
	}
}

func TestMailIdentityConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  MailIdentityConfig
		wantErr string
	}{
		{name: "base config", config: BaseMailIdentityConfig()},
		{
			name:   "drop rule",
			config: MailIdentityConfig{FallbackIdentity: {Rules: []HandlerRule{Drop}}},
		},
		{name: "missing fallback", config: MailIdentityConfig{}, wantErr: "fallback"},
		{
			name:    "empty rules",
			config:  MailIdentityConfig{FallbackIdentity: {Rules: []HandlerRule{}}},
			wantErr: "at least one handler rule is required",
		},
		{
			name:    "unknown rule",
			config:  MailIdentityConfig{FallbackIdentity: {Rules: []HandlerRule{"Nope"}}},
			wantErr: "unknown handler rule",
		},
		{
			name:    "empty key",
			config:  MailIdentityConfig{" ": {Rules: []HandlerRule{Bounce}}, FallbackIdentity: {Rules: []HandlerRule{Bounce}}},
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

func TestLoadMailIdentityConfigProvidesFallbackWithoutWriting(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryConfigStore()

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	rules, err := config.GetByIdentity(MailIdentityForRecipient("nobody@example.com"))
	if err != nil || len(rules.Rules) != 2 {
		t.Fatalf("GetByIdentity() = %+v, %v; want base rules", rules, err)
	}

	// Loading is read-only: the fallback exists in memory but is not written
	// until the user explicitly saves rules.
	if _, err := store.Get(ctx, FallbackIdentity); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("stored fallback error = %v, want ErrIdentityConfigNotFound", err)
	}
	keys, err := store.List(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("List() after load = %v, %v; want no writes", keys, err)
	}
}

func TestLoadMailIdentityConfigLoadsEveryConfiguration(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryConfigStore()
	aliasRules := HandlerRules{MailIdentity: "alias@example.com", Rules: []HandlerRule{Bounce}}
	if err := store.Put(ctx, "Alias@Example.com", aliasRules); err != nil {
		t.Fatal(err)
	}

	config, err := LoadMailIdentityConfig(ctx, store)
	if err != nil {
		t.Fatalf("LoadMailIdentityConfig() error = %v", err)
	}
	got, err := config.GetByIdentity("alias@example.com")
	if err != nil {
		t.Fatalf("GetByIdentity() error = %v", err)
	}
	if !reflect.DeepEqual(got, aliasRules) {
		t.Errorf("GetByIdentity() = %+v, want %+v", got, aliasRules)
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
	rules := HandlerRules{MailIdentity: "alias@example.com", Rules: []HandlerRule{DeliverToMailIdentity}}
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
	if _, err := NewConfigStore(ConfigStoreOptions{}); err == nil {
		t.Error("NewConfigStore(\"\") should fail: memory must be selected explicitly")
	}
	store, err := NewConfigStore(ConfigStoreOptions{Type: "memory"})
	if err != nil || store.Name() != "memory" {
		t.Fatalf("NewConfigStore(memory) = %v, %v; want memory", store, err)
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

func TestConfigStoresDeleteProtectFallback(t *testing.T) {
	ctx := context.Background()
	fileStore, err := NewFileConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileConfigStore() error = %v", err)
	}
	stores := []struct {
		name  string
		store ConfigStore
	}{
		{"memory", NewMemoryConfigStore()},
		{"file", fileStore},
		{"s3", newS3ConfigStore(&fakeConfigS3{objects: map[string][]byte{}}, "bucket")},
		{"ddb", newDdbConfigStore(&fakeConfigDdb{items: make(map[string]map[string]ddbtypes.AttributeValue)}, "table")},
	}

	for _, tt := range stores {
		t.Run(tt.name, func(t *testing.T) {
			rules := HandlerRules{MailIdentity: "alias@example.com", Rules: []HandlerRule{Bounce}}
			if err := tt.store.Put(ctx, "alias@example.com", rules); err != nil {
				t.Fatalf("Put() error = %v", err)
			}

			// The fallback can never be removed, whatever case it is given in.
			for _, fallback := range []string{FallbackIdentity, "_"} {
				if err := tt.store.Delete(ctx, fallback); !errors.Is(err, ErrFallbackIdentity) {
					t.Errorf("Delete(%q) error = %v, want ErrFallbackIdentity", fallback, err)
				}
			}
			if err := tt.store.Delete(ctx, "bad/name"); err == nil {
				t.Error("Delete(invalid key) error = nil, want an error")
			}

			// Identity matching is case-insensitive on delete, as on get.
			if err := tt.store.Delete(ctx, "ALIAS@Example.com"); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			keys, err := tt.store.List(ctx)
			if err != nil || len(keys) != 0 {
				t.Fatalf("List() after Delete() = %v, %v; want no keys", keys, err)
			}
			if _, err := tt.store.Get(ctx, "alias@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
				t.Errorf("Get(deleted) error = %v, want ErrIdentityConfigNotFound", err)
			}
			if err := tt.store.Delete(ctx, "alias@example.com"); err != nil {
				t.Errorf("Delete(missing) error = %v, want nil (idempotent)", err)
			}
		})
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

func (f *fakeConfigS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
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
	if len(client.objects) != 0 {
		t.Fatalf("LoadMailIdentityConfig() wrote objects: %v", client.objects)
	}

	rules := HandlerRules{MailIdentity: "blocked@example.com", Rules: []HandlerRule{Bounce}}
	if err := store.Put(ctx, "blocked@example.com", rules); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	keys, err := store.List(ctx)
	if err != nil || !reflect.DeepEqual(keys, []string{"blocked@example.com"}) {
		t.Fatalf("List() = %v, %v; want the saved identity only", keys, err)
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

func (f *fakeConfigDdb) DeleteItem(_ context.Context, params *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	id := params.Key["id"].(*ddbtypes.AttributeValueMemberS).Value
	delete(f.items, id)
	return &dynamodb.DeleteItemOutput{}, nil
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
	if len(client.items) != 0 {
		t.Fatalf("LoadMailIdentityConfig() wrote items: %v", client.items)
	}
	rules, err := config.GetByIdentity(MailIdentityForRecipient("nobody@example.com"))
	if err != nil || len(rules.Rules) != 2 {
		t.Fatalf("GetByIdentity(fallback) = %+v, %v; want base rules", rules, err)
	}

	aliasRules := HandlerRules{MailIdentity: "alias@example.com", Rules: []HandlerRule{DeliverToMailIdentity}}
	if err := store.Put(ctx, "Alias@Example.com", aliasRules); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := store.Get(ctx, "ALIAS@example.com")
	if err != nil || !reflect.DeepEqual(got, aliasRules) {
		t.Fatalf("Get(case-insensitive) = %+v, %v; want %+v", got, err, aliasRules)
	}
	if _, err := store.Get(ctx, "missing@example.com"); !errors.Is(err, ErrIdentityConfigNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrIdentityConfigNotFound", err)
	}

	keys, err := store.List(ctx)
	wantKeys := []string{"alias@example.com"}
	if err != nil || !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("List() = %v, %v; want %v", keys, err, wantKeys)
	}

	// Loading an already-populated store must stay read-only.
	if _, err := LoadMailIdentityConfig(ctx, store); err != nil {
		t.Fatalf("second LoadMailIdentityConfig() error = %v", err)
	}
	keys, err = store.List(ctx)
	if err != nil || !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("List() after reload = %v, %v; want %v", keys, err, wantKeys)
	}
}
