package thread

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const threadTestAccount = "alice@example.com"

func TestThreadGetGroupsReplyChain(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	deps := &base.Deps{Storage: store, Metadata: metaStore, Changes: base.NewTracker(store)}
	h := New(deps)

	messages := []struct {
		id        string
		subject   string
		messageID string
		inReplyTo []string
		received  string
	}{
		{"e1", "Project update", "<m1@example.com>", nil, "2026-01-01T09:00:00Z"},
		{"e2", "Re: Project update", "<m2@example.com>", []string{"<m1@example.com>"}, "2026-01-01T10:00:00Z"},
		{"e3", "[Team] Re: Re: Project update", "<m3@example.com>", []string{"<m2@example.com>"}, "2026-01-01T11:00:00Z"},
	}
	for _, message := range messages {
		received, err := time.Parse(time.RFC3339, message.received)
		if err != nil {
			t.Fatal(err)
		}
		store.Add(&mailemail.ListedEmail{
			Ref: &mailemail.EmailRef{ID: message.id, AccountID: threadTestAccount, MailboxID: "inbox"},
			Email: &mailemail.UserEmail{
				Subject:    message.subject,
				MessageID:  message.messageID,
				InReplyTo:  message.inReplyTo,
				References: append(append([]string(nil), message.inReplyTo...), message.messageID),
				Date:       received,
			},
		})
		meta := metadata.EmptyEmailMetadata(threadTestAccount, message.id)
		meta.ReceivedAt = metadata.FormatReceivedAt(received)
		if err := metaStore.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%s) error = %v", message.id, err)
		}
	}

	raw, err := h.ThreadGet(ctx, threadTestAccount, json.RawMessage(`{"ids":null}`))
	if err != nil {
		t.Fatalf("ThreadGet() error = %v", err)
	}
	var response struct {
		State string   `json:"state"`
		List  []Thread `json:"list"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal ThreadGet response: %v (raw %s)", err, raw)
	}
	if len(response.List) != 1 {
		t.Fatalf("threads = %+v, want one grouped thread", response.List)
	}
	if !reflect.DeepEqual(response.List[0].EmailIDs, []string{"e1", "e2", "e3"}) {
		t.Errorf("emailIds = %v, want [e1 e2 e3] oldest first", response.List[0].EmailIDs)
	}
	if response.List[0].ID == "" || response.List[0].ID == "e1" {
		t.Errorf("thread id = %q, want a stable digest", response.List[0].ID)
	}

	// The grouped snapshot is durable tracker state, so an unchanged reload
	// must not advance Thread/changes state.
	again, err := h.ThreadGet(ctx, threadTestAccount, json.RawMessage(`{"ids":null}`))
	if err != nil {
		t.Fatalf("second ThreadGet() error = %v", err)
	}
	var second struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(again, &second); err != nil {
		t.Fatalf("unmarshal second ThreadGet response: %v", err)
	}
	if second.State != response.State {
		t.Errorf("state changed from %q to %q with no data change", response.State, second.State)
	}
}
