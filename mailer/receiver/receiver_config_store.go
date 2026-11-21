package receiver

import (
	"context"
	"fmt"
	"strings"
)

// configPrefix is the S3 key prefix and filesystem directory holding one
// receiver configuration file per mail identity.
const configPrefix = "receiver"

// ConfigStore persists one JSON configuration per mail identity under the
// "receiver" prefix. Get returns ErrIdentityConfigNotFound when the identity
// has no configuration; Put overwrites a configuration; Delete removes one;
// List returns all configured identities. Every implementation refuses to
// delete the FallbackIdentity ("_") because all unconfigured recipients rely
// on it.
type ConfigStore interface {
	Name() string
	Get(ctx context.Context, identity string) (HandlerRules, error)
	Put(ctx context.Context, identity string, rules HandlerRules) error
	Delete(ctx context.Context, identity string) error
	List(ctx context.Context) ([]string, error)
}

// ConfigStoreOptions carries the resolved settings for each backend. Only the
// fields relevant to the selected Type are used.
type ConfigStoreOptions struct {
	Type          string // "s3", "filesystem", "memory", "ddb" or "dynamodb"
	FilesystemDir string // FILESYSTEM_DIR root; the store adds "receiver/"
	Bucket        string // S3_BUCKET for the s3 backend
	TableName     string // fully resolved DynamoDB table name
	Region        string // AWS region for the DynamoDB client
}

// NewConfigStore selects a receiver config backend by name. The type must be
// explicit: memory is a supported implementation, but an unset or unknown
// selector is an error rather than a silent in-memory fallback.
func NewConfigStore(opts ConfigStoreOptions) (ConfigStore, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Type)) {
	case "s3":
		return NewS3ConfigStore(opts.Bucket)
	case "file", "filesystem":
		return NewFileConfigStore(opts.FilesystemDir)
	case "ddb", "dynamodb":
		return NewDdbConfigStore(opts.TableName, opts.Region)
	case "memory":
		return NewMemoryConfigStore(), nil
	default:
		return nil, fmt.Errorf("unknown receiver type %q (set RECEIVER explicitly)", opts.Type)
	}
}

// LoadMailIdentityConfig loads every configuration from store into a map. If
// the store has no base fallback ("_"), one is added to the returned config so
// unknown recipients always have rules to apply. Loading never writes to the
// store; the fallback is only persisted if a save explicitly includes it.
func LoadMailIdentityConfig(ctx context.Context, store ConfigStore) (MailIdentityConfig, error) {
	if store == nil {
		store = NewMemoryConfigStore()
	}
	keys, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list receiver config: %w", err)
	}

	config := make(MailIdentityConfig, len(keys)+1)
	for _, key := range keys {
		rules, err := store.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("load receiver config %q: %w", key, err)
		}
		config[normalizeIdentity(key)] = rules
	}

	if _, ok := config[FallbackIdentity]; !ok {
		config[FallbackIdentity] = BaseMailIdentityConfig()[FallbackIdentity]
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}
