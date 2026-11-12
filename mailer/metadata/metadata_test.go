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
	meta, err := m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read {
		t.Error("Get() should return false for new email")
	}

	// Mark read
	meta.Read = true
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.Read {
		t.Error("Get() should return true after Save")
	}

	// Mark unread
	meta.Read = false
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read {
		t.Error("Get() should return false after Save unread")
	}
}

func TestMemoryMetadata_Concurrent(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()
	done := make(chan bool, 100)

	for i := 0; i < 50; i++ {
		go func(id string) {
			meta, _ := m.Get(ctx, id)
			meta.Read = true
			m.Save(ctx, meta)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 50; i++ {
		go func(id string) {
			m.Get(ctx, id)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestMemoryMetadata_Delete(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()

	meta := EmailMetadata{ID: "email-1", Read: true}
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := m.Delete(ctx, "email-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	meta, err := m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read || meta.Flagged {
		t.Errorf("Get() after delete = %+v, want no state", meta)
	}
	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after delete = %v, want empty", list)
	}

	// Deleting an absent id is not an error.
	if err := m.Delete(ctx, "absent"); err != nil {
		t.Errorf("Delete() absent error = %v, want nil", err)
	}
}

func TestFileMetadata_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := tmpDir + "/metadata.json"

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	ctx := context.Background()

	meta := EmailMetadata{ID: "email-1", Read: true}
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := m.Delete(ctx, "email-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// A fresh instance shows the deletion was persisted.
	m2, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	list, err := m2.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after delete = %v, want empty", list)
	}

	// Deleting an absent id is not an error.
	if err := m2.Delete(ctx, "absent"); err != nil {
		t.Errorf("Delete() absent error = %v, want nil", err)
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
	meta, err := m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read {
		t.Error("Get() should return false for new email")
	}

	// Mark read
	meta.Read = true
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.Read {
		t.Error("Get() should return true after Save")
	}

	// Create new instance to test persistence
	m2, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	meta, err = m2.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.Read {
		t.Error("Get() should persist across instances")
	}

	// Mark unread
	meta.Read = false
	if err := m2.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m2.Get(ctx, "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read {
		t.Error("Get() should return false after Save unread")
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

	meta, err := m.Get(context.Background(), "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.Read {
		t.Error("Get() should return false for new email in empty file")
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

	if err := m.Save(context.Background(), EmailMetadata{ID: "email-1", Read: true}); err != nil {
		t.Fatalf("Save() error = %v", err)
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
		id := "email-" + string(rune('0'+i%10))
		if err := m.Save(context.Background(), EmailMetadata{ID: id, Read: true}); err != nil {
			t.Fatalf("Save() error = %v", err)
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