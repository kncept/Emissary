package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandDir(t *testing.T) {
	if got, err := ExpandDir(""); err != nil || got != "" {
		t.Fatalf("ExpandDir(\"\") = %q, %v; want empty", got, err)
	}
	dir, err := ExpandDir("~/emissary-test")
	if err != nil {
		t.Fatalf("ExpandDir(~/...) error = %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	if want := filepath.Join(home, "emissary-test"); dir != want {
		t.Errorf("ExpandDir = %q, want %q", dir, want)
	}

	relative, err := ExpandDir("some/../dir")
	if err != nil || relative != "dir" {
		t.Errorf("ExpandDir(relative) = %q, %v; want %q", relative, err, "dir")
	}
}

func TestResolveKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	got, err := ResolveKey(root, "users/alice@example.com/inbox/message.eml")
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	want := filepath.Join(root, "users", "alice@example.com", "inbox", "message.eml")
	if got != want {
		t.Errorf("ResolveKey() = %q, want %q", got, want)
	}

	for _, key := range []string{
		"",
		"/etc/passwd",
		"../outside",
		"users/../../outside",
		"users\\alice\\message.eml",
		"users/\x00name",
		".",
	} {
		if _, err := ResolveKey(root, key); err == nil {
			t.Errorf("ResolveKey(%q) succeeded, want error", key)
		}
	}
}

func TestWriteFileAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "record.json")
	if err := WriteFileAtomically(path, []byte(`{"id":"x"}`), 0600); err != nil {
		t.Fatalf("WriteFileAtomically() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.TrimSpace(string(data)) != `{"id":"x"}` {
		t.Errorf("contents = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	// No temporary files should be left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1", len(entries))
	}
}
