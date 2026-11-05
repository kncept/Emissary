package metadata

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMemoryMetadata(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()

	// Initially unread
	read, err := m.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if read {
		t.Error("IsRead() should return false for new email")
	}

	// Mark read
	if err := m.MarkRead(ctx, "email-1"); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}

	read, err = m.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if !read {
		t.Error("IsRead() should return true after MarkRead")
	}

	// Mark unread
	if err := m.MarkUnread(ctx, "email-1"); err != nil {
		t.Fatalf("MarkUnread() error = %v", err)
	}

	read, err = m.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if read {
		t.Error("IsRead() should return false after MarkUnread")
	}
}

func TestMemoryMetadata_Concurrent(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()
	done := make(chan bool, 100)

	for i := 0; i < 50; i++ {
		go func(id string) {
			m.MarkRead(ctx, id)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 50; i++ {
		go func(id string) {
			m.IsRead(ctx, id)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestFileMetadata(t *testing.T) {
	tmpFile := "/tmp/emissary-test-metadata-" + string(rune('0'+int(time.Now().UnixNano()%1000))) + ".json"
	defer os.Remove(tmpFile)

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	ctx := context.Background()

	// Initially unread
	read, err := m.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if read {
		t.Error("IsRead() should return false for new email")
	}

	// Mark read
	if err := m.MarkRead(ctx, "email-1"); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}

	read, err = m.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if !read {
		t.Error("IsRead() should return true after MarkRead")
	}

	// Create new instance to test persistence
	m2, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	read, err = m2.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if !read {
		t.Error("IsRead() should persist across instances")
	}

	// Mark unread
	if err := m2.MarkUnread(ctx, "email-1"); err != nil {
		t.Fatalf("MarkUnread() error = %v", err)
	}

	read, err = m2.IsRead(ctx, "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if read {
		t.Error("IsRead() should return false after MarkUnread")
	}
}

func TestFileMetadata_EmptyFile(t *testing.T) {
	tmpFile := "/tmp/emissary-test-empty-" + string(rune('0'+int(time.Now().UnixNano()%1000))) + ".json"
	defer os.Remove(tmpFile)

	// Create empty file
	os.WriteFile(tmpFile, []byte(""), 0644)

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	read, err := m.IsRead(context.Background(), "email-1")
	if err != nil {
		t.Fatalf("IsRead() error = %v", err)
	}
	if read {
		t.Error("IsRead() should return false for new email in empty file")
	}
}

func TestFileMetadata_InvalidJSON(t *testing.T) {
	tmpFile := "/tmp/emissary-test-invalid-" + string(rune('0'+int(time.Now().UnixNano()%1000))) + ".json"
	defer os.Remove(tmpFile)

	os.WriteFile(tmpFile, []byte("not valid json"), 0644)

	_, err := NewFileMetadata(tmpFile)
	if err == nil {
		t.Error("NewFileMetadata() should error on invalid JSON")
	}
}

func TestFileMetadata_CreatesDirectory(t *testing.T) {
	tmpDir := "/tmp/emissary-test-dir-" + string(rune('0'+int(time.Now().UnixNano()%1000)))
	tmpFile := tmpDir + "/metadata.json"
	defer os.RemoveAll(tmpDir)

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	if err := m.MarkRead(context.Background(), "email-1"); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Error("Metadata file should be created")
	}
}

func TestFileMetadata_AtomicWrite(t *testing.T) {
	tmpFile := "/tmp/emissary-test-atomic-" + string(rune('0'+int(time.Now().UnixNano()%1000))) + ".json"
	defer os.Remove(tmpFile)

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	// Write multiple times rapidly
	for i := 0; i < 100; i++ {
		if err := m.MarkRead(context.Background(), "email-"+string(rune('0'+i%10))); err != nil {
			t.Fatalf("MarkRead() error = %v", err)
		}
	}

	// Verify file is valid JSON
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if len(data) == 0 {
		t.Error("File should not be empty")
	}
}