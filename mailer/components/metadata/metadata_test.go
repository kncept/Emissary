package metadata

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestKeywordConstants(t *testing.T) {
	want := map[string]string{
		"KeywordDraft":     "$draft",
		"KeywordSeen":      "$seen",
		"KeywordFlagged":   "$flagged",
		"KeywordAnswered":  "$answered",
		"KeywordForwarded": "$forwarded",
		"KeywordJunk":      "$junk",
		"KeywordNotJunk":   "$notjunk",
		"KeywordPhishing":  "$phishing",
	}
	got := map[string]string{
		"KeywordDraft":     KeywordDraft,
		"KeywordSeen":      KeywordSeen,
		"KeywordFlagged":   KeywordFlagged,
		"KeywordAnswered":  KeywordAnswered,
		"KeywordForwarded": KeywordForwarded,
		"KeywordJunk":      KeywordJunk,
		"KeywordNotJunk":   KeywordNotJunk,
		"KeywordPhishing":  KeywordPhishing,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}

func TestNormalizeKeyword(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"$Seen", "$seen", true},
		{" $flagged ", "$flagged", true},
		{"", "", false},
		{"has space", "", false},
		{`bad"quote`, "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeKeyword(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("NormalizeKeyword(%q) = %q, %v; want %q, %v", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNewEmailMetadataFromMap(t *testing.T) {
	m := NewEmailMetadataFromMap("alice@example.com", "email-1", map[string]bool{
		"$Seen":    true,
		"$flagged": true,
		"$draft":   false,
		"invalid":  true,
	})
	if m.UserID != "alice@example.com" || m.ID != "email-1" {
		t.Fatalf("ids = %q/%q", m.UserID, m.ID)
	}
	if !m.HasKeyword(KeywordSeen) || !m.HasKeyword(KeywordFlagged) {
		t.Errorf("expected $seen and $flagged, got %+v", m.Keywords)
	}
	if m.HasKeyword(KeywordDraft) {
		t.Error("false and invalid keywords must not be stored")
	}
	if _, ok := m.Keywords["$Seen"]; ok {
		t.Error("keyword names must be normalized to lowercase")
	}
}

func TestCloneIsIndependent(t *testing.T) {
	m := NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)
	clone := m.Clone()
	clone.UserID = "bob@example.com"
	clone.ID = "email-2"
	clone.Keywords[KeywordFlagged] = true
	delete(clone.Keywords, KeywordSeen)
	if m.UserID != "alice@example.com" || m.ID != "email-1" {
		t.Errorf("mutating a clone changed ids: %q/%q", m.UserID, m.ID)
	}
	if !m.HasKeyword(KeywordSeen) || m.HasKeyword(KeywordFlagged) {
		t.Errorf("mutating a clone changed the original: %+v", m.Keywords)
	}
	if clone.ReceivedAt != m.ReceivedAt {
		t.Errorf("clone receivedAt = %q, want %q", clone.ReceivedAt, m.ReceivedAt)
	}
}

func TestNewEmailMetadataStampsReceivedAt(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	m := NewEmailMetadata("alice@example.com", "email-1")
	after := time.Now().UTC().Add(time.Second)

	if m.ReceivedAt == "" {
		t.Fatal("new metadata should stamp receivedAt")
	}
	received, err := time.Parse(time.RFC3339, m.ReceivedAt)
	if err != nil {
		t.Fatalf("receivedAt %q is not RFC 3339: %v", m.ReceivedAt, err)
	}
	if received.Location() != time.UTC && received.Format("-07:00") != "+00:00" {
		t.Errorf("receivedAt %q is not UTC", m.ReceivedAt)
	}
	if received.Before(before) || received.After(after) {
		t.Errorf("receivedAt %q outside [%s, %s]", m.ReceivedAt, before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

func TestEmptyEmailMetadataHasNoReceivedAt(t *testing.T) {
	m := EmptyEmailMetadata("alice@example.com", "email-1")
	if m.UserID != "alice@example.com" || m.ID != "email-1" || m.ReceivedAt != "" {
		t.Errorf("EmptyEmailMetadata() = %+v, want ids with no receivedAt", m)
	}
	if m.Keywords == nil {
		t.Error("EmptyEmailMetadata() should return an initialized keyword map")
	}
}

func TestFileMetadataPersistsReceivedAt(t *testing.T) {
	tmpFile := t.TempDir() + "/metadata.json"
	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	stored := NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)
	stored.ReceivedAt = FormatReceivedAt(time.Date(2026, time.November, 18, 1, 2, 3, 0, time.UTC))
	if err := m.Save(context.Background(), stored); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reopened, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() reopen error = %v", err)
	}
	got, err := reopened.Get(context.Background(), "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.UserID != "alice@example.com" || got.ID != "email-1" {
		t.Errorf("ids = %q/%q", got.UserID, got.ID)
	}
	if got.ReceivedAt != stored.ReceivedAt {
		t.Errorf("receivedAt = %q, want %q", got.ReceivedAt, stored.ReceivedAt)
	}
}

func TestMemoryMetadata(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()

	// A missing record is an empty keyword set, not an error.
	meta, err := m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.UserID != "alice@example.com" || meta.ID != "email-1" || len(meta.Keywords) != 0 || meta.IsRead() {
		t.Errorf("Get() for missing record = %+v, want empty keywords", meta)
	}
	if meta.ReceivedAt != "" {
		t.Errorf("Get() for missing record fabricated receivedAt %q", meta.ReceivedAt)
	}

	// Mark read
	meta.Keywords[KeywordSeen] = true
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.IsRead() {
		t.Error("Get() should return $seen after Save")
	}

	// Different user does not see it.
	other, err := m.Get(ctx, "bob@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get(other) error = %v", err)
	}
	if other.IsRead() || other.ReceivedAt != "" {
		t.Errorf("other user metadata leaked: %+v", other)
	}

	// Mark unread by removing the keyword
	delete(meta.Keywords, KeywordSeen)
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.IsRead() {
		t.Error("Get() should not return $seen after removal")
	}
}

func TestMemoryMetadata_Concurrent(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()
	done := make(chan bool, 100)

	for i := 0; i < 50; i++ {
		go func(id string) {
			meta, _ := m.Get(ctx, "alice@example.com", id)
			meta.Keywords[KeywordSeen] = true
			m.Save(ctx, meta)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 50; i++ {
		go func(id string) {
			m.Get(ctx, "alice@example.com", id)
			done <- true
		}("email-" + string(rune('0'+i%10)))
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	// Saves must not share a keyword map with later readers.
	meta, err := m.Get(ctx, "alice@example.com", "email-0")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	meta.Keywords[KeywordFlagged] = true
	again, err := m.Get(ctx, "alice@example.com", "email-0")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if again.HasKeyword(KeywordFlagged) {
		t.Error("Get() returned a shared keyword map")
	}
}

func TestMemoryMetadata_Delete(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()

	meta := NewEmailMetadata("alice@example.com", "email-1", KeywordSeen, KeywordFlagged)
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := m.Delete(ctx, "alice@example.com", "email-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	meta, err := m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(meta.Keywords) != 0 {
		t.Errorf("Get() after delete = %+v, want no keywords", meta)
	}
	list, err := m.List(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after delete = %v, want empty", list)
	}

	// Deleting an absent record is not an error.
	if err := m.Delete(ctx, "alice@example.com", "absent"); err != nil {
		t.Errorf("Delete() absent error = %v, want nil", err)
	}
}

func TestMemoryMetadata_ListScopedToUser(t *testing.T) {
	m := NewMemoryMetadata()
	ctx := context.Background()
	if err := m.Save(ctx, NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)); err != nil {
		t.Fatalf("Save alice: %v", err)
	}
	if err := m.Save(ctx, NewEmailMetadata("bob@example.com", "email-2", KeywordSeen)); err != nil {
		t.Fatalf("Save bob: %v", err)
	}
	list, err := m.List(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].UserID != "alice@example.com" || list[0].ID != "email-1" {
		t.Fatalf("List(alice) = %+v", list)
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

	meta := NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := m.Delete(ctx, "alice@example.com", "email-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// A fresh instance shows the deletion was persisted.
	m2, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}
	list, err := m2.List(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after delete = %v, want empty", list)
	}

	// Deleting an absent record is not an error.
	if err := m2.Delete(ctx, "alice@example.com", "absent"); err != nil {
		t.Errorf("Delete() absent error = %v, want nil", err)
	}
}

func TestFileMetadata(t *testing.T) {
	tmpFile := t.TempDir() + "/metadata.json"

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	ctx := context.Background()

	// Missing metadata returns an empty keyword set.
	meta, err := m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(meta.Keywords) != 0 {
		t.Errorf("Get() for new email = %+v, want no keywords", meta)
	}

	meta.Keywords[KeywordSeen] = true
	if err := m.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.IsRead() {
		t.Error("Get() should return $seen after Save")
	}

	// Create new instance to test persistence.
	m2, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	meta, err = m2.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !meta.IsRead() {
		t.Error("Get() should persist across instances")
	}

	delete(meta.Keywords, KeywordSeen)
	if err := m2.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	meta, err = m2.Get(ctx, "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if meta.IsRead() {
		t.Error("Get() should not return $seen after removal")
	}
}

func TestFileMetadata_EmptyFile(t *testing.T) {
	tmpFile := t.TempDir() + "/metadata.json"

	if err := os.WriteFile(tmpFile, []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	meta, err := m.Get(context.Background(), "alice@example.com", "email-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(meta.Keywords) != 0 {
		t.Errorf("Get() for empty file = %+v, want no keywords", meta)
	}
}

func TestFileMetadata_InvalidJSON(t *testing.T) {
	tmpFile := t.TempDir() + "/metadata.json"
	if err := os.WriteFile(tmpFile, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	_, err := NewFileMetadata(tmpFile)
	if err == nil {
		t.Error("NewFileMetadata() should error on invalid JSON")
	}
}

func TestFileMetadata_CreatesDirectory(t *testing.T) {
	tmpFile := t.TempDir() + "/nested/metadata.json"

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	if err := m.Save(context.Background(), NewEmailMetadata("alice@example.com", "email-1", KeywordSeen)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Error("Metadata file should be created")
	}
}

func TestFileMetadata_AtomicWrite(t *testing.T) {
	tmpFile := t.TempDir() + "/metadata.json"

	m, err := NewFileMetadata(tmpFile)
	if err != nil {
		t.Fatalf("NewFileMetadata() error = %v", err)
	}

	// Write multiple times rapidly.
	for i := 0; i < 100; i++ {
		id := "email-" + string(rune('0'+i%10))
		if err := m.Save(context.Background(), NewEmailMetadata("alice@example.com", id, KeywordSeen)); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if len(data) == 0 {
		t.Error("File should not be empty")
	}
}

func TestNewStore(t *testing.T) {
	store, err := NewStore("", "", "", "")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if store.Name() != "memory" {
		t.Errorf("default store = %q, want memory", store.Name())
	}

	tmpFile := t.TempDir() + "/metadata.json"
	store, err = NewStore("file", tmpFile, "", "")
	if err != nil {
		t.Fatalf("NewStore(file) error = %v", err)
	}
	if store.Name() != "file" {
		t.Errorf("file store = %q, want file", store.Name())
	}
}
