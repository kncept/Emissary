package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kncept/Emissary/mailer/components/sender"
)

func TestNew_FilesystemBackends(t *testing.T) {
	root := t.TempDir()
	h, err := newTestHandler(t, handlerTestConfig{
		AuthUser:        "admin",
		AuthPass:        "admin-pass",
		JWTSecret:       "test-secret",
		FilesystemDir:   root,
		StorageType:     "filesystem",
		MetadataType:    "file",
		MetadataPath:    filepath.Join(root, "metadata"),
		UserDomain:      "example.com",
		UserManagerType: "filesystem",
		Sender:          sender.NewInMemoryRecording(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, dir := range []string{filepath.Join(root, "mail"), filepath.Join(root, "users")} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("directory %q missing: %v", dir, err)
		}
	}

	status, token, _ := login(t, h, "admin", "admin-pass")
	if status != 200 || token == "" {
		t.Errorf("admin login = %d, token %q", status, token)
	}
}
