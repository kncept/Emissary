package configvendor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/config"
	"github.com/kncept/Emissary/mailer/receiver"
)

func TestStorage(t *testing.T) {
	store, err := Storage(StorageOptions{Type: "memory"})
	if err != nil {
		t.Fatalf("Storage(memory) error = %v", err)
	}
	if _, ok := store.(*storage.MemoryStorage); !ok {
		t.Errorf("Storage(memory) = %T, want *storage.MemoryStorage", store)
	}

	root := t.TempDir()
	store, err = Storage(StorageOptions{Type: "filesystem", FilesystemDir: root})
	if err != nil {
		t.Fatalf("Storage(filesystem) error = %v", err)
	}
	if got := store.Name(); got != "filesystem" {
		t.Errorf("Name() = %q, want filesystem", got)
	}

	if _, err := Storage(StorageOptions{Type: "unknown"}); err == nil {
		t.Error("Storage(unknown) should fail")
	}
	if _, err := Storage(StorageOptions{}); err == nil {
		t.Error("Storage with no type should fail: a backend must be chosen explicitly")
	}
}

func TestSender(t *testing.T) {
	ctx := context.Background()

	mailSender, err := Sender(ctx, SenderOptions{Type: "log"})
	if err != nil {
		t.Fatalf("Sender(log) error = %v", err)
	}
	if _, ok := mailSender.(*sender.LogSender); !ok {
		t.Errorf("Sender(log) = %T, want *sender.LogSender", mailSender)
	}

	mailSender, err = Sender(ctx, SenderOptions{Type: "mta", MTAPort: "1025"})
	if err != nil {
		t.Fatalf("Sender(mta) error = %v", err)
	}
	if _, ok := mailSender.(*sender.MtaSender); !ok {
		t.Errorf("Sender(mta) = %T, want *sender.MtaSender", mailSender)
	}

	if _, err := Sender(ctx, SenderOptions{Type: "unknown"}); err == nil {
		t.Error("Sender(unknown) should fail")
	}
	if _, err := Sender(ctx, SenderOptions{}); err == nil {
		t.Error("Sender with no type should fail: a backend must be chosen explicitly")
	}
}

func TestSessionAndMetadata(t *testing.T) {
	store, err := Session(SessionOptions{Type: "memory"})
	if err != nil {
		t.Fatalf("Session(memory) error = %v", err)
	}
	if got := store.Name(); got != "memory" {
		t.Errorf("session Name() = %q, want memory", got)
	}
	if _, err := Session(SessionOptions{Type: "unknown"}); err == nil {
		t.Error("Session(unknown) should fail")
	}
	if _, err := Session(SessionOptions{}); err == nil {
		t.Error("Session with no type should fail: a backend must be chosen explicitly")
	}

	if _, err := Metadata(MetadataOptions{}); err == nil {
		t.Error("Metadata with no type should fail: a backend must be chosen explicitly")
	}
	meta, err := Metadata(MetadataOptions{Type: "memory"})
	if err != nil {
		t.Fatalf("Metadata(memory) error = %v", err)
	}
	if got := meta.Name(); got != "memory" {
		t.Errorf("metadata Name() = %q, want memory", got)
	}

	meta, err = Metadata(MetadataOptions{Type: "file", FilePath: filepath.Join(t.TempDir(), "metadata")})
	if err != nil {
		t.Fatalf("Metadata(file) error = %v", err)
	}
	if got := meta.Name(); got != "file" {
		t.Errorf("metadata Name() = %q, want file", got)
	}
}

func TestUserManagerAndReceiverConfigStore(t *testing.T) {
	if _, err := UserManager(UserManagerOptions{}); err == nil {
		t.Error("UserManager with no type should fail: a backend must be chosen explicitly")
	}
	users, err := UserManager(UserManagerOptions{Type: "none"})
	if err != nil {
		t.Fatalf("UserManager(none) error = %v", err)
	}
	if got := users.Name(); got != "none" {
		t.Errorf("user manager Name() = %q, want none", got)
	}

	if _, err := ReceiverConfigStore(ReceiverConfigStoreOptions{}); err == nil {
		t.Error("ReceiverConfigStore with no type should fail: a backend must be chosen explicitly")
	}
	store, err := ReceiverConfigStore(ReceiverConfigStoreOptions{Type: "memory"})
	if err != nil {
		t.Fatalf("ReceiverConfigStore(memory) error = %v", err)
	}
	if got := store.Name(); got != "memory" {
		t.Errorf("receiver store Name() = %q, want memory", got)
	}
}

func TestFromConfig(t *testing.T) {
	cfg := &config.Config{
		Storage:     config.StorageConfig{Type: "memory"},
		Sender:      config.SenderConfig{Type: "log"},
		Receiver:    config.ReceiverConfig{Type: "memory"},
		Metadata:    config.MetadataConfig{Type: "memory"},
		Session:     config.SessionConfig{Type: "memory"},
		UserManager: config.UserManagerConfig{Type: "none"},
	}

	bundle, err := FromConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FromConfig() error = %v", err)
	}
	if bundle.Storage == nil || bundle.Metadata == nil || bundle.Session == nil ||
		bundle.Sender == nil || bundle.UserManager == nil || bundle.ReceiverConfigStore == nil {
		t.Fatal("FromConfig() left a component nil")
	}
	if rules, ok := bundle.ReceiverConfig[receiver.FallbackIdentity]; !ok || len(rules.Rules) == 0 {
		t.Errorf("FromConfig() did not seed the fallback rules: %v", bundle.ReceiverConfig)
	}

	if _, err := FromConfig(context.Background(), nil); err == nil {
		t.Error("FromConfig(nil) should fail")
	}
}
