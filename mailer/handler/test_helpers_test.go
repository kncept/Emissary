package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/session"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/receiver"
)

// handlerTestConfig is the test-only adapter for the old flat construction
// shape. It translates the handful of settings tests care about into the
// config.Config and configvendor.Bundle that the production New consumes.
// Component selectors are only used here, to preserve tests that exercise a
// vendored backend without calling config.Load.
type handlerTestConfig struct {
	AuthUser       string
	AuthPass       string
	ListenAddr     string
	JWTSecret      string
	JWTSecretTTL   time.Duration
	S3Bucket       string
	FilesystemDir  string
	StorageType    string
	AWSRegion      string
	MetadataType   string
	MetadataPath   string
	MetadataTable  string
	MetadataRegion string
	SessionType    string
	SessionTable   string
	SessionRegion  string
	DDBTablePrefix string

	Storage     storage.Storage
	Metadata    metadata.MetadataStore
	Session     session.SessionStore
	JWTAudience string
	JWTIssuer   string

	UserManager      usermanager.UserManager
	UserManagerType  string
	UserDomain       string
	UserTable        string
	UserRegion       string
	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string

	Sender        sender.Sender
	SenderType    string
	SenderMTAPort string

	ReceiverConfigType    string
	ReceiverBucket        string
	ReceiverFilesystemDir string
	ReceiverTable         string
	ReceiverRegion        string
	ReceiverConfig        receiver.MailIdentityConfig
	ReceiverConfigStore   receiver.ConfigStore

	SESIdentityLister    SESIdentityLister
	IsLambda             bool
	LoginRateLimit       int
	LoginRateLimitWindow time.Duration
	MaxFileSize          int64
}

func (tc handlerTestConfig) build(t *testing.T) (*config.Config, *configvendor.Bundle, Options) {
	t.Helper()
	ctx := context.Background()

	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:  tc.ListenAddr,
			Username:    tc.AuthUser,
			Password:    tc.AuthPass,
			JWTSecret:   tc.JWTSecret,
			JWTExpiry:   tc.JWTSecretTTL,
			JWTAudience: tc.JWTAudience,
			JWTIssuer:   tc.JWTIssuer,
		},
		Storage: config.StorageConfig{
			Type:          tc.StorageType,
			S3Bucket:      tc.S3Bucket,
			FilesystemDir: tc.FilesystemDir,
		},
		Sender: config.SenderConfig{
			Type:    tc.SenderType,
			MTAPort: tc.SenderMTAPort,
		},
		Receiver: config.ReceiverConfig{
			Type:          tc.ReceiverConfigType,
			S3Bucket:      tc.ReceiverBucket,
			FilesystemDir: tc.ReceiverFilesystemDir,
			DdbTable:      tc.ReceiverTable,
			Region:        tc.ReceiverRegion,
		},
		Metadata: config.MetadataConfig{
			Type:     tc.MetadataType,
			DdbTable: tc.MetadataTable,
			FilePath: tc.MetadataPath,
			Region:   tc.MetadataRegion,
		},
		Session: config.SessionConfig{
			Type:     tc.SessionType,
			DdbTable: tc.SessionTable,
			Region:   tc.SessionRegion,
		},
		UserManager: config.UserManagerConfig{
			Type:             tc.UserManagerType,
			Domain:           tc.UserDomain,
			Region:           tc.UserRegion,
			DdbTable:         tc.UserTable,
			FilesystemDir:    tc.FilesystemDir,
			LDAPURL:          tc.LDAPURL,
			LDAPBindDN:       tc.LDAPBindDN,
			LDAPBindPassword: tc.LDAPBindPassword,
			LDAPBaseDN:       tc.LDAPBaseDN,
			LDAPUserFilter:   tc.LDAPUserFilter,
			LDAPCacheTTL:     tc.LDAPCacheTTL,
		},
		AWSRegion:      tc.AWSRegion,
		FilesystemDir:  tc.FilesystemDir,
		DDBTablePrefix: tc.DDBTablePrefix,
		MaxFileSize:    tc.MaxFileSize,
	}
	// config.Load applies both fallbacks; mirror them for tests that set only
	// the shared storage/filesystem settings.
	if cfg.Receiver.S3Bucket == "" {
		cfg.Receiver.S3Bucket = cfg.Storage.S3Bucket
	}
	if cfg.Receiver.FilesystemDir == "" {
		cfg.Receiver.FilesystemDir = cfg.FilesystemDir
	}

	b := &configvendor.Bundle{
		Storage:             tc.Storage,
		Metadata:            tc.Metadata,
		Session:             tc.Session,
		Sender:              tc.Sender,
		UserManager:         tc.UserManager,
		ReceiverConfigStore: tc.ReceiverConfigStore,
		ReceiverConfig:      tc.ReceiverConfig,
	}
	var err error
	if b.Storage == nil {
		b.Storage, err = configvendor.Storage(configvendor.StorageOptions{
			Type:          cfg.Storage.Type,
			S3Bucket:      cfg.Storage.S3Bucket,
			FilesystemDir: cfg.FilesystemMailDir(),
		})
		if err != nil {
			t.Fatalf("test storage: %v", err)
		}
	}
	if b.Metadata == nil {
		b.Metadata, err = configvendor.Metadata(configvendor.MetadataOptions{
			Type:      cfg.Metadata.Type,
			FilePath:  cfg.Metadata.FilePath,
			TableName: cfg.MetadataTableName(),
			Region:    cfg.Metadata.Region,
		})
		if err != nil {
			t.Fatalf("test metadata: %v", err)
		}
	}
	if b.Session == nil {
		b.Session, err = configvendor.Session(configvendor.SessionOptions{
			Type:      cfg.Session.Type,
			TableName: cfg.SessionTableName(),
			Region:    cfg.Session.Region,
		})
		if err != nil {
			t.Fatalf("test session: %v", err)
		}
	}
	if b.Sender == nil {
		b.Sender, err = configvendor.Sender(ctx, configvendor.SenderOptions{
			Type:    cfg.Sender.Type,
			MTAPort: cfg.Sender.MTAPort,
		})
		if err != nil {
			t.Fatalf("test sender: %v", err)
		}
	}
	if b.UserManager == nil {
		b.UserManager, err = configvendor.UserManager(configvendor.UserManagerOptions{
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
		})
		if err != nil {
			t.Fatalf("test user manager: %v", err)
		}
	}
	if b.ReceiverConfigStore == nil {
		if tc.ReceiverConfigType != "" {
			b.ReceiverConfigStore, err = configvendor.ReceiverConfigStore(configvendor.ReceiverConfigStoreOptions{
				Type:          cfg.Receiver.Type,
				FilesystemDir: cfg.Receiver.FilesystemDir,
				Bucket:        cfg.Receiver.S3Bucket,
				TableName:     cfg.ReceiverTableName(),
				Region:        cfg.Receiver.Region,
			})
			if err != nil {
				t.Fatalf("test receiver config store: %v", err)
			}
		} else {
			b.ReceiverConfigStore = receiver.NewMemoryConfigStore()
		}
	}

	opts := Options{
		JWTSecret:            tc.JWTSecret,
		JWTSecretTTL:         tc.JWTSecretTTL,
		LoginRateLimit:       tc.LoginRateLimit,
		LoginRateLimitWindow: tc.LoginRateLimitWindow,
		MaxFileSize:          tc.MaxFileSize,
		IsLambda:             tc.IsLambda,
		SESIdentityLister:    tc.SESIdentityLister,
	}
	return cfg, b, opts
}

func newTestHandler(t *testing.T, tc handlerTestConfig) (http.Handler, error) {
	t.Helper()
	cfg, b, opts := tc.build(t)
	return New(context.Background(), cfg, b, opts)
}

func newTestSupportHandler(t *testing.T, ctx context.Context, tc handlerTestConfig) *supportHandler {
	t.Helper()
	cfg, b, opts := tc.build(t)
	return newSupportHandler(ctx, cfg, b, opts)
}
