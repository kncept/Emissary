package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/receiver"
)

// TestNewProductionWiring exercises the real composition path: a loaded-style
// config, configvendor.FromConfig, then handler.New with no test adapter.
func TestNewProductionWiring(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Username:    "admin",
			Password:    "secret",
			JWTSecret:   "jwt-secret",
			JWTExpiry:   time.Hour,
			JWTAudience: "test-audience",
			JWTIssuer:   "test-issuer",
		},
		Storage:     config.StorageConfig{Type: "memory"},
		Sender:      config.SenderConfig{Type: "log"},
		Receiver:    config.ReceiverConfig{Type: "memory"},
		Metadata:    config.MetadataConfig{Type: "memory"},
		Session:     config.SessionConfig{Type: "memory"},
		UserManager: config.UserManagerConfig{Type: "none", Domain: "example.com"},
		MaxFileSize: config.DefaultMaxFileSize,
	}

	bundle, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("FromConfig() error = %v", err)
	}
	if bundle.ReceiverConfigStore == nil || bundle.ReceiverConfig == nil {
		t.Fatal("FromConfig() did not provide the receiver store and rules")
	}

	h, err := New(ctx, cfg, bundle, Options{IsLambda: true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	status, token, _ := login(t, h, "admin", "secret")
	if status != http.StatusOK || token == "" {
		t.Fatalf("admin login = %d, token %q", status, token)
	}

	w := request(t, h, http.MethodGet, "/api/support/configurations/server", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("server settings status = %d (body %s)", w.Code, w.Body.String())
	}
	var detail struct {
		Settings map[string]string `json:"settings"`
	}
	decodeJSONBody(t, w.Body.Bytes(), &detail)
	if detail.Settings["lambda"] != "true" {
		t.Errorf("lambda = %q, want true", detail.Settings["lambda"])
	}
	if detail.Settings["jwtAudience"] != "test-audience" || detail.Settings["jwtIssuer"] != "test-issuer" {
		t.Errorf("JWT audience/issuer = %q/%q, want test-audience/test-issuer", detail.Settings["jwtAudience"], detail.Settings["jwtIssuer"])
	}
}

// locatedConfigStore keeps the location it was constructed with, so the
// reported setting can be compared with the store actually in the bundle.
type locatedConfigStore struct {
	receiver.ConfigStore
	location string
}

func TestSupportReceiverReportsActiveStoreSettings(t *testing.T) {
	const tablePrefix = "test-prefix"
	tests := []struct {
		name     string
		backend  string
		setting  string
		newStore func(t *testing.T, cfg *config.Config) (receiver.ConfigStore, string)
	}{
		{
			name:    "s3",
			backend: "s3",
			setting: "bucket",
			newStore: func(t *testing.T, cfg *config.Config) (receiver.ConfigStore, string) {
				t.Helper()
				store := &locatedConfigStore{ConfigStore: receiver.NewMemoryConfigStore(), location: cfg.Receiver.S3Bucket}
				return store, store.location
			},
		},
		{
			name:    "filesystem",
			backend: "filesystem",
			setting: "dir",
			newStore: func(t *testing.T, cfg *config.Config) (receiver.ConfigStore, string) {
				t.Helper()
				store, err := receiver.NewFileConfigStore(cfg.Receiver.FilesystemDir)
				if err != nil {
					t.Fatalf("NewFileConfigStore() error = %v", err)
				}
				dir := filepath.Join(cfg.Receiver.FilesystemDir, "receiver")
				if err := store.Put(context.Background(), "example.com", receiver.HandlerRules{
					MailIdentity: "example.com",
					Rules:        []receiver.HandlerRule{receiver.DeliverToUser},
				}); err != nil {
					t.Fatalf("seed filesystem store: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "example.com.json")); err != nil {
					t.Fatalf("store did not write into %s: %v", dir, err)
				}
				return store, dir
			},
		},
		{
			name:    "ddb",
			backend: "ddb",
			setting: "table",
			newStore: func(t *testing.T, cfg *config.Config) (receiver.ConfigStore, string) {
				t.Helper()
				store := &locatedConfigStore{ConfigStore: receiver.NewMemoryConfigStore(), location: cfg.ReceiverTableName()}
				return store, store.location
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Storage:        config.StorageConfig{S3Bucket: "mail-bucket"},
				Receiver:       config.ReceiverConfig{Type: tc.backend, S3Bucket: "receiver-bucket", FilesystemDir: t.TempDir(), Region: "us-west-2"},
				DDBTablePrefix: tablePrefix,
			}
			store, want := tc.newStore(t, cfg)
			bundle := &configvendor.Bundle{ReceiverConfigStore: store}
			h := newSupportHandler(context.Background(), cfg, bundle, Options{})

			settings := h.configurationSettings(receiverConfigurationName)
			if got := settings[tc.setting]; got != want {
				t.Errorf("%s = %q, want active store value %q", tc.setting, got, want)
			}
			if got := settings["type"]; got != receiverImplementation(tc.backend) {
				t.Errorf("type = %q, want %q", got, receiverImplementation(tc.backend))
			}
			if tc.backend == "ddb" && want != tablePrefix+"-receiver" {
				t.Errorf("derived table = %q, want config helper result", want)
			}
		})
	}
}
