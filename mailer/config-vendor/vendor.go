// Package configvendor turns runtime configuration into the concrete
// implementations that back each configurable seam (storage, sender,
// metadata, sessions, user management and receiver rules).
//
// Keeping the selector switches here means a new implementation is registered
// in one place instead of separately in handler.New and cmd/s3-receiver.
package configvendor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kncept/Emissary/mailer/config"
	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// StorageOptions carries the settings used by the storage backends.
// FilesystemDir is the already resolved mail directory (not the shared root).
type StorageOptions struct {
	Type          string // "s3", "filesystem" or "memory"
	S3Bucket      string
	FilesystemDir string
}

// Storage creates the storage backend named by opts.Type.
func Storage(opts StorageOptions) (storage.Storage, error) {
	switch normalize(opts.Type) {
	case "memory":
		return storage.NewMemoryStorage(), nil
	case "filesystem":
		return storage.NewFileStorage(opts.FilesystemDir)
	case "", "s3":
		return storage.NewS3Storage(opts.S3Bucket)
	default:
		return nil, fmt.Errorf("unknown storage type %q", opts.Type)
	}
}

// SenderOptions carries the settings used by the outbound senders.
type SenderOptions struct {
	Type    string // "aws-ses", "ses", "mta" or "log"
	MTAPort string
}

// Sender creates the outbound sender named by opts.Type.
func Sender(ctx context.Context, opts SenderOptions) (sender.Sender, error) {
	switch normalize(opts.Type) {
	case "aws-ses", "ses":
		return sender.NewAwsSesSender(ctx)
	case "mta":
		return sender.NewMtaSender(opts.MTAPort), nil
	case "", "log":
		return sender.NewLogSender(), nil
	default:
		return nil, fmt.Errorf("unknown sender type %q", opts.Type)
	}
}

// SessionOptions carries the settings used by the refresh-token stores.
// TableName is expected to be fully resolved by the caller.
type SessionOptions struct {
	Type      string // "memory", "ddb" or "dynamodb"
	TableName string
	Region    string
}

// Session creates the session store named by opts.Type.
func Session(opts SessionOptions) (session.SessionStore, error) {
	switch normalize(opts.Type) {
	case "", "memory":
		return session.NewMemorySessionStore(), nil
	case "ddb", "dynamodb":
		return session.NewDdbSessionStore(opts.TableName, opts.Region)
	default:
		return nil, fmt.Errorf("unknown session type %q", opts.Type)
	}
}

// MetadataOptions carries the settings used by the metadata stores.
// TableName is expected to be fully resolved by the caller.
type MetadataOptions struct {
	Type      string // "memory", "file", "ddb" or "dynamodb"
	FilePath  string
	TableName string
	Region    string
}

// Metadata creates the metadata store named by opts.Type.
func Metadata(opts MetadataOptions) (metadata.MetadataStore, error) {
	return metadata.NewStore(opts.Type, opts.FilePath, opts.TableName, opts.Region)
}

// UserManagerOptions carries the settings used by the user managers.
// DdbTable and FilesystemDir are expected to be fully resolved by the caller.
type UserManagerOptions struct {
	Type          string // "none", "ddb", "dynamodb", "filesystem", "ldap" or "s3"
	AdminUsername string
	AdminPassword string
	Domain        string
	DdbTable      string
	Region        string
	S3Bucket      string
	FilesystemDir string

	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
}

// UserManager creates the user manager named by opts.Type.
func UserManager(opts UserManagerOptions) (usermanager.UserManager, error) {
	return usermanager.New(usermanager.Config{
		Type:             opts.Type,
		AdminUsername:    opts.AdminUsername,
		AdminPassword:    opts.AdminPassword,
		Domain:           opts.Domain,
		DdbTable:         opts.DdbTable,
		Region:           opts.Region,
		S3Bucket:         opts.S3Bucket,
		FilesystemDir:    opts.FilesystemDir,
		LDAPURL:          opts.LDAPURL,
		LDAPBindDN:       opts.LDAPBindDN,
		LDAPBindPassword: opts.LDAPBindPassword,
		LDAPBaseDN:       opts.LDAPBaseDN,
		LDAPUserFilter:   opts.LDAPUserFilter,
		LDAPCacheTTL:     opts.LDAPCacheTTL,
	})
}

// ReceiverConfigStoreOptions carries the settings used by receiver rule stores.
// TableName is expected to be fully resolved by the caller.
type ReceiverConfigStoreOptions struct {
	Type          string // "s3", "filesystem", "memory", "ddb" or "dynamodb"
	FilesystemDir string
	Bucket        string
	TableName     string
	Region        string
}

// ReceiverConfigStore creates the receiver rule store named by opts.Type.
func ReceiverConfigStore(opts ReceiverConfigStoreOptions) (receiver.ConfigStore, error) {
	return receiver.NewConfigStore(receiver.ConfigStoreOptions{
		Type:          opts.Type,
		FilesystemDir: opts.FilesystemDir,
		Bucket:        opts.Bucket,
		TableName:     opts.TableName,
		Region:        opts.Region,
	})
}

// Bundle holds every runtime component created from a Config.
type Bundle struct {
	Storage             storage.Storage
	Metadata            metadata.MetadataStore
	Session             session.SessionStore
	Sender              sender.Sender
	UserManager         usermanager.UserManager
	ReceiverConfigStore receiver.ConfigStore
	ReceiverConfig      receiver.MailIdentityConfig
}

// FromConfig creates every component from the corresponding subtype of cfg.
// Derived values (table names and per-store directories) come from cfg's
// helpers so all entry points see the same resolved settings.
func FromConfig(ctx context.Context, cfg *config.Config) (*Bundle, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}

	b := &Bundle{}
	var err error

	if b.Storage, err = Storage(StorageOptions{
		Type:          cfg.Storage.Type,
		S3Bucket:      cfg.Storage.S3Bucket,
		FilesystemDir: cfg.FilesystemMailDir(),
	}); err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	if b.Metadata, err = Metadata(MetadataOptions{
		Type:      cfg.Metadata.Type,
		FilePath:  cfg.Metadata.FilePath,
		TableName: cfg.MetadataTableName(),
		Region:    cfg.Metadata.Region,
	}); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	if b.Session, err = Session(SessionOptions{
		Type:      cfg.Session.Type,
		TableName: cfg.SessionTableName(),
		Region:    cfg.Session.Region,
	}); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if b.Sender, err = Sender(ctx, SenderOptions{
		Type:    cfg.Sender.Type,
		MTAPort: cfg.Sender.MTAPort,
	}); err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	if b.UserManager, err = UserManager(UserManagerOptions{
		Type:             cfg.UserManager.Type,
		AdminUsername:    cfg.Server.Username,
		AdminPassword:    cfg.Server.Password,
		Domain:           cfg.UserManager.Domain,
		DdbTable:         cfg.UserTableName(),
		Region:           cfg.UserManager.Region,
		S3Bucket:         cfg.Storage.S3Bucket,
		FilesystemDir:    cfg.FilesystemUsersDir(),
		LDAPURL:          cfg.UserManager.LDAPURL,
		LDAPBindDN:       cfg.UserManager.LDAPBindDN,
		LDAPBindPassword: cfg.UserManager.LDAPBindPassword,
		LDAPBaseDN:       cfg.UserManager.LDAPBaseDN,
		LDAPUserFilter:   cfg.UserManager.LDAPUserFilter,
		LDAPCacheTTL:     cfg.UserManager.LDAPCacheTTL,
	}); err != nil {
		return nil, fmt.Errorf("user manager: %w", err)
	}
	if b.ReceiverConfigStore, err = ReceiverConfigStore(ReceiverConfigStoreOptions{
		Type:          cfg.Receiver.Type,
		FilesystemDir: cfg.Receiver.FilesystemDir,
		Bucket:        cfg.Receiver.S3Bucket,
		TableName:     cfg.ReceiverTableName(),
		Region:        cfg.Receiver.Region,
	}); err != nil {
		return nil, fmt.Errorf("receiver config store: %w", err)
	}
	if b.ReceiverConfig, err = receiver.LoadMailIdentityConfig(ctx, b.ReceiverConfigStore); err != nil {
		return nil, fmt.Errorf("receiver config: %w", err)
	}

	slog.Debug("Runtime components created",
		"storage", b.Storage.Name(),
		"metadata", b.Metadata.Name(),
		"session", b.Session.Name(),
		"sender", b.Sender.Name(),
		"user_manager", b.UserManager.Name(),
		"receiver_config", b.ReceiverConfigStore.Name(),
		"receiver_identities", len(b.ReceiverConfig))
	return b, nil
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
