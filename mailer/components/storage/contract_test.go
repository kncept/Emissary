package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/email"
)

type contractBackend struct {
	name string
	open func(*testing.T) Storage
}

// contractBackends is the single matrix used by the storage contract tests.
// Every invariant below must hold for all three implementations.
func contractBackends() []contractBackend {
	return []contractBackend{
		{
			name: "filesystem",
			open: func(t *testing.T) Storage {
				store, _ := newTestFileStorage(t)
				return store
			},
		},
		{
			name: "s3",
			open: func(t *testing.T) Storage {
				return &S3Storage{client: newFakeS3(nil), bucket: "mail-bucket"}
			},
		},
		{
			name: "memory",
			open: func(*testing.T) Storage {
				return NewMemoryStorage()
			},
		},
	}
}

func contractAccountID() string { return "contract@example.com" }

func contractPtr(value string) *string { return &value }

func addContractEmail(t *testing.T, store Storage, accountID, mailboxID, subject string, date time.Time, raw []byte) string {
	t.Helper()
	id, err := store.AddEmail(context.Background(), accountID, mailboxID, &email.UserEmail{
		From:    email.Address{Address: "sender@example.com"},
		To:      []email.Address{{Address: accountID}},
		Subject: subject,
		Text:    "contract body",
		Date:    date,
	}, raw)
	if err != nil {
		t.Fatalf("AddEmail(%q, %q) error = %v", mailboxID, subject, err)
	}
	if id == "" {
		t.Fatalf("AddEmail(%q, %q) returned empty id", mailboxID, subject)
	}
	return id
}

func assertContractRaw(t *testing.T, store Storage, accountID, id string, want []byte) {
	t.Helper()
	got, err := store.GetEmailRaw(context.Background(), accountID, id)
	if err != nil {
		t.Fatalf("GetEmailRaw(%q) error = %v", id, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("GetEmailRaw(%q) = %q, want original bytes %q", id, got, want)
	}
}

// TestStorageContractCreatedIDsUseRevisions exercises the revision-aware
// SessionStateStore contract on every backend. Concurrent Lambda invocations
// rely on this to avoid losing each other's creation ids.
func TestStorageContractCreatedIDsUseRevisions(t *testing.T) {
	ctx := context.Background()
	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)

			ids, revision, err := store.LoadCreatedIDs(ctx, "session-1")
			if err != nil || len(ids) != 0 || revision != "" {
				t.Fatalf("empty load = %v / %q / %v", ids, revision, err)
			}

			revision, err = store.SaveCreatedIDs(ctx, "session-1", map[string]string{"create1": "id-1"}, "")
			if err != nil || revision == "" {
				t.Fatalf("SaveCreatedIDs() = %q / %v, want a revision", revision, err)
			}
			if _, err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"create2": "id-2"}, ""); !errors.Is(err, ErrSessionStateConflict) {
				t.Fatalf("stale create error = %v, want ErrSessionStateConflict", err)
			}

			nextRevision, err := store.SaveCreatedIDs(ctx, "session-1",
				map[string]string{"create1": "id-1", "create2": "id-2"}, revision)
			if err != nil || nextRevision == "" || nextRevision == revision {
				t.Fatalf("conditional save = %q / %v, want a new revision", nextRevision, err)
			}
			if _, err := store.SaveCreatedIDs(ctx, "session-1", map[string]string{"stale": "x"}, revision); !errors.Is(err, ErrSessionStateConflict) {
				t.Fatalf("stale update error = %v, want ErrSessionStateConflict", err)
			}

			ids, loadedRevision, err := store.LoadCreatedIDs(ctx, "session-1")
			if err != nil {
				t.Fatalf("LoadCreatedIDs() error = %v", err)
			}
			if loadedRevision != nextRevision {
				t.Errorf("loaded revision = %q, want %q", loadedRevision, nextRevision)
			}
			if ids["create1"] != "id-1" || ids["create2"] != "id-2" || len(ids) != 2 {
				t.Errorf("loaded ids = %v, want create1 and create2", ids)
			}
		})
	}
}

func TestStorageContractMailboxes(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)

			defaults, err := store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() error = %v", err)
			}
			if len(defaults) != len(DefaultMailboxes()) {
				t.Fatalf("ListMailboxes() returned %d defaults, want %d", len(defaults), len(DefaultMailboxes()))
			}
			for _, want := range DefaultMailboxes() {
				found := false
				for _, got := range defaults {
					if got.ID == want.ID {
						found = true
						if got.Role == nil || *got.Role != *want.Role {
							t.Errorf("default mailbox %q role = %v, want %q", want.ID, got.Role, *want.Role)
						}
						break
					}
				}
				if !found {
					t.Errorf("default mailbox %q missing from %+v", want.ID, defaults)
				}
			}

			created, err := store.CreateMailbox(ctx, accountID, Mailbox{
				ID:        "  Projects  ",
				Name:      "  Team Projects  ",
				ParentID:  contractPtr("  INBOX "),
				SortOrder: 9,
			})
			if err != nil {
				t.Fatalf("CreateMailbox() error = %v", err)
			}
			if created.ID != "projects" || created.Name != "Team Projects" {
				t.Errorf("CreateMailbox() = %+v, want normalized id/name", created)
			}
			if created.ParentID == nil || *created.ParentID != "inbox" {
				t.Errorf("CreateMailbox() parent = %v, want inbox", created.ParentID)
			}

			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "projects", Name: "Duplicate"}); !errors.Is(err, ErrMailboxExists) {
				t.Errorf("duplicate CreateMailbox() error = %v, want ErrMailboxExists", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: " ", Name: "Blank"}); err == nil || err.Error() != "invalid mailbox" {
				t.Errorf("CreateMailbox() blank id error = %v, want invalid mailbox", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "orphan", Name: "Orphan", ParentID: contractPtr("missing")}); !errors.Is(err, ErrNotFound) {
				t.Errorf("CreateMailbox() missing parent error = %v, want ErrNotFound", err)
			}
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "self", Name: "Self", ParentID: contractPtr("self")}); err == nil || err.Error() != "mailbox cannot be its own parent" {
				t.Errorf("CreateMailbox() self parent error = %v, want mailbox cannot be its own parent", err)
			}

			updated, err := store.UpdateMailbox(ctx, accountID, Mailbox{
				ID:        "projects",
				Name:      "  Renamed Projects  ",
				ParentID:  contractPtr("  SENT "),
				SortOrder: 4,
			})
			if err != nil {
				t.Fatalf("UpdateMailbox() error = %v", err)
			}
			if updated.Name != "Renamed Projects" || updated.ParentID == nil || *updated.ParentID != "sent" {
				t.Errorf("UpdateMailbox() = %+v, want normalized name and sent parent", updated)
			}
			if _, err := store.UpdateMailbox(ctx, accountID, Mailbox{ID: "projects", Name: "Projects", ParentID: contractPtr("projects")}); err == nil || err.Error() != "mailbox cannot be its own parent" {
				t.Errorf("UpdateMailbox() self parent error = %v, want mailbox cannot be its own parent", err)
			}
			if _, err := store.UpdateMailbox(ctx, accountID, Mailbox{ID: "missing", Name: "Missing"}); !errors.Is(err, ErrNotFound) {
				t.Errorf("UpdateMailbox() missing error = %v, want ErrNotFound", err)
			}

			mailboxes, err := store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() after update error = %v", err)
			}
			found := false
			for _, mb := range mailboxes {
				if mb.ID == "projects" {
					found = true
					if mb.Name != "Renamed Projects" || mb.ParentID == nil || *mb.ParentID != "sent" {
						t.Errorf("listed mailbox = %+v, want updated record", mb)
					}
				}
			}
			if !found {
				t.Error("updated mailbox projects missing from ListMailboxes()")
			}
		})
	}
}

func TestStorageContractListEmailsOrdering(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			specs := []struct {
				name    string
				subject string
				date    time.Time
			}{
				{name: "old", subject: "Old", date: base.Add(-2 * time.Hour)},
				{name: "mid", subject: "Mid", date: base.Add(-time.Hour)},
				{name: "new", subject: "New", date: base},
				{name: "tie-a", subject: "Tie A", date: base},
				{name: "tie-b", subject: "Tie B", date: base},
			}
			ids := make(map[string]string, len(specs))
			for _, spec := range specs {
				ids[spec.name] = addContractEmail(t, store, accountID, "inbox", spec.subject, spec.date, nil)
			}

			// All backends standardize on Email.Date descending, with Ref.ID
			// descending as the deterministic tie-breaker. The three newest
			// messages share a timestamp, so compute their expected order from
			// the ids the stores returned.
			tied := []string{ids["new"], ids["tie-a"], ids["tie-b"]}
			sort.Slice(tied, func(i, j int) bool { return tied[i] > tied[j] })
			want := append(tied, ids["mid"], ids["old"])

			got, err := store.ListEmails(ctx, accountID, "inbox")
			if err != nil {
				t.Fatalf("ListEmails() error = %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("ListEmails() returned %d messages, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].Ref.ID != want[i] {
					t.Errorf("ListEmails()[%d].ID = %q, want %q (date desc, id desc)", i, got[i].Ref.ID, want[i])
				}
			}
		})
	}
}

func TestStorageContractResolveEmailByID(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			id := addContractEmail(t, store, accountID, "inbox", "Resolve me", base, nil)
			fullKey := email.DeliveredMessageKey(accountID, "inbox", id)

			for _, tc := range []struct {
				name string
				id   string
			}{
				{name: "stable token", id: id},
				{name: "full key", id: fullKey},
				{name: "leading slash full key", id: "/" + fullKey},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := store.GetEmail(ctx, accountID, tc.id)
					if err != nil {
						t.Fatalf("GetEmail(%q) error = %v", tc.id, err)
					}
					if got.Ref.ID != id || got.Email.Subject != "Resolve me" {
						t.Errorf("GetEmail(%q) = %+v, want stable id %q", tc.id, got, id)
					}
				})
			}

			if _, err := store.GetEmail(ctx, "other@example.com", fullKey); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetEmail() with another account error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStorageContractBlobs(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	data := []byte("immutable blob bytes")

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)

			meta, err := store.PutBlob(ctx, accountID, "text/plain", data)
			if err != nil {
				t.Fatalf("PutBlob() error = %v", err)
			}
			if meta.ID != BlobID(data) || meta.Type != "text/plain" || meta.Size != int64(len(data)) {
				t.Errorf("PutBlob() = %+v, want content id %q", meta, BlobID(data))
			}

			again, err := store.PutBlob(ctx, accountID, "text/plain", data)
			if err != nil || again.ID != meta.ID {
				t.Errorf("second PutBlob() = %+v, %v, want idempotent %q", again, err, meta.ID)
			}

			gotMeta, gotData, err := store.GetBlob(ctx, accountID, meta.ID)
			if err != nil {
				t.Fatalf("GetBlob(%q) error = %v", meta.ID, err)
			}
			if gotMeta.ID != meta.ID || !bytes.Equal(gotData, data) {
				t.Errorf("GetBlob(%q) = %+v, %q", meta.ID, gotMeta, gotData)
			}

			streamMeta, reader, err := store.OpenBlob(ctx, accountID, meta.ID)
			if err != nil {
				t.Fatalf("OpenBlob(%q) error = %v", meta.ID, err)
			}
			streamed, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil || streamMeta.ID != meta.ID || !bytes.Equal(streamed, data) {
				t.Errorf("OpenBlob(%q) = %+v, %q, %v", meta.ID, streamMeta, streamed, err)
			}

			if _, _, err := store.GetBlob(ctx, "other@example.com", meta.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetBlob() from another account error = %v, want ErrNotFound", err)
			}
			if _, _, err := store.GetBlob(ctx, accountID, "../escape"); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetBlob() with malformed id error = %v, want ErrNotFound", err)
			}

			// Delivered email bytes are blobs too: JMAP Email objects expose
			// their opaque id as a blobId.
			raw := []byte("From: sender@example.com\r\nSubject: Blob fallback\r\n\r\nbody\r\n")
			emailID := addContractEmail(t, store, accountID, "inbox", "Blob fallback", time.Now(), raw)
			emailBlob, emailData, err := store.GetBlob(ctx, accountID, emailID)
			if err != nil {
				t.Fatalf("GetBlob(email id) error = %v", err)
			}
			if !emailBlob.EmailBacked || !bytes.Equal(emailData, raw) {
				t.Errorf("GetBlob(email id) = %+v, %q, want raw email bytes", emailBlob, emailData)
			}
			if err := store.DeleteBlob(ctx, accountID, emailID); !errors.Is(err, ErrNotFound) {
				t.Errorf("DeleteBlob(email id) error = %v, want ErrNotFound", err)
			}

			if err := store.DeleteBlob(ctx, accountID, meta.ID); err != nil {
				t.Fatalf("DeleteBlob(%q) error = %v", meta.ID, err)
			}
			if _, _, err := store.GetBlob(ctx, accountID, meta.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetBlob() after delete error = %v, want ErrNotFound", err)
			}

			mailboxes, err := store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() after PutBlob error = %v", err)
			}
			for _, mailbox := range mailboxes {
				if mailbox.ID == blobDirName {
					t.Errorf("ListMailboxes() exposed reserved mailbox %q", mailbox.ID)
				}
			}

			// Reserving _blobs must not break otherwise valid mailbox names.
			if _, err := store.CreateMailbox(ctx, accountID, Mailbox{ID: "_archive", Name: "Archive"}); err != nil {
				t.Fatalf("CreateMailbox(_archive) error = %v", err)
			}
			if _, err := store.AddEmail(ctx, accountID, "_archive", &email.UserEmail{
				From:    email.Address{Address: "sender@example.com"},
				Subject: "Underscore mailbox",
			}, []byte("From: sender@example.com\r\nSubject: Underscore mailbox\r\n\r\nbody\r\n")); err != nil {
				t.Fatalf("AddEmail() to _archive mailbox error = %v", err)
			}
			mailboxes, err = store.ListMailboxes(ctx, accountID)
			if err != nil {
				t.Fatalf("ListMailboxes() after _archive error = %v", err)
			}
			found := false
			for _, mailbox := range mailboxes {
				if mailbox.ID == "_archive" {
					found = true
				}
			}
			if !found {
				t.Errorf("ListMailboxes() dropped the _archive mailbox: %+v", mailboxes)
			}
		})
	}
}

func TestStorageContractBlobLifecycle(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	data := []byte("lifecycle metadata")

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			meta, err := store.PutBlob(ctx, accountID, "text/plain", data)
			if err != nil {
				t.Fatalf("PutBlob() error = %v", err)
			}

			gotMeta, err := store.GetBlobMeta(ctx, accountID, meta.ID)
			if err != nil {
				t.Fatalf("GetBlobMeta() error = %v", err)
			}
			if gotMeta.ID != meta.ID || gotMeta.AccountID != meta.AccountID || gotMeta.Size != int64(len(data)) {
				t.Errorf("GetBlobMeta() = %+v, want %+v", gotMeta, meta)
			}
			if gotMeta.Uploader() != accountID {
				t.Errorf("legacy blob uploader = %q, want %q", gotMeta.Uploader(), accountID)
			}

			listed, err := store.ListBlobs(ctx, accountID)
			if err != nil {
				t.Fatalf("ListBlobs() error = %v", err)
			}
			found := false
			for _, blob := range listed {
				if blob.ID == meta.ID {
					found = true
				}
			}
			if !found {
				t.Errorf("ListBlobs() = %+v, missing %q", listed, meta.ID)
			}

			pinned, err := store.AddBlobRefs(ctx, accountID, meta.ID, 1)
			if err != nil || pinned.Refs != 1 {
				t.Fatalf("AddBlobRefs(+1) = %+v, %v", pinned, err)
			}
			if err := store.DeleteBlob(ctx, accountID, meta.ID); !errors.Is(err, ErrBlobReferenced) {
				t.Errorf("DeleteBlob() while referenced error = %v, want ErrBlobReferenced", err)
			}
			released, err := store.AddBlobRefs(ctx, accountID, meta.ID, -1)
			if err != nil || released.Refs != 0 {
				t.Fatalf("AddBlobRefs(-1) = %+v, %v", released, err)
			}
			if err := store.DeleteBlob(ctx, accountID, meta.ID); err != nil {
				t.Fatalf("DeleteBlob() after release error = %v", err)
			}
			if _, err := store.GetBlobMeta(ctx, accountID, meta.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetBlobMeta() after delete error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStorageContractPushSubscriptions(t *testing.T) {
	ctx := context.Background()
	credentialA, credentialB := "contract-cred-a", "contract-cred-b"

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			a := []PushSubscription{{
				ID:               "sub-a1",
				CredentialID:     credentialA,
				DeviceClientID:   "device-a",
				URL:              "https://push.example/a",
				VerificationCode: "code-a",
				Expires:          "2027-01-01T00:00:00Z",
			}}
			b := []PushSubscription{{
				ID:             "sub-b1",
				CredentialID:   credentialB,
				DeviceClientID: "device-b",
				URL:            "https://push.example/b",
			}}
			if err := store.SavePushSubscriptions(ctx, credentialA, a); err != nil {
				t.Fatalf("SavePushSubscriptions(A) error = %v", err)
			}
			if err := store.SavePushSubscriptions(ctx, credentialB, b); err != nil {
				t.Fatalf("SavePushSubscriptions(B) error = %v", err)
			}

			loadedA, err := store.LoadPushSubscriptions(ctx, credentialA)
			if err != nil {
				t.Fatalf("LoadPushSubscriptions(A) error = %v", err)
			}
			if len(loadedA) != 1 || loadedA[0].ID != "sub-a1" || loadedA[0].VerificationCode != "code-a" {
				t.Fatalf("LoadPushSubscriptions(A) = %+v", loadedA)
			}
			loadedA[0].Keys = &PushKeys{P256DH: "mutated", Auth: "mutated"}
			loadedA[0].DeviceClientID = "mutated"
			reloaded, err := store.LoadPushSubscriptions(ctx, credentialA)
			if err != nil {
				t.Fatalf("second LoadPushSubscriptions(A) error = %v", err)
			}
			if reloaded[0].Keys != nil || reloaded[0].DeviceClientID != "device-a" {
				t.Errorf("storage returned aliased subscription state: %+v", reloaded[0])
			}

			loadedB, err := store.LoadPushSubscriptions(ctx, credentialB)
			if err != nil || len(loadedB) != 1 || loadedB[0].ID != "sub-b1" {
				t.Errorf("LoadPushSubscriptions(B) = %+v, %v", loadedB, err)
			}

			if err := store.DeletePushSubscriptions(ctx, credentialA); err != nil {
				t.Fatalf("DeletePushSubscriptions(A) error = %v", err)
			}
			loadedA, err = store.LoadPushSubscriptions(ctx, credentialA)
			if err != nil || len(loadedA) != 0 {
				t.Errorf("LoadPushSubscriptions(A) after delete = %+v, %v", loadedA, err)
			}
			loadedB, err = store.LoadPushSubscriptions(ctx, credentialB)
			if err != nil || len(loadedB) != 1 {
				t.Errorf("DeletePushSubscriptions(A) affected B: %+v, %v", loadedB, err)
			}

			if _, err := store.LoadPushSubscriptions(ctx, "bad/credential"); err == nil {
				t.Error("LoadPushSubscriptions() accepted an invalid credential id")
			}
		})
	}
}

func TestStorageContractAddAndMoveEmail(t *testing.T) {
	ctx := context.Background()
	accountID := contractAccountID()
	base := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)

	for _, backend := range contractBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.open(t)
			// Render the raw bytes from the same message so every backend
			// parses the same subject back after the move.
			raw := email.RawFromUserEmail(&email.UserEmail{
				From:    email.Address{Address: "sender@example.com"},
				To:      []email.Address{{Address: accountID}},
				Subject: "Move me",
				Text:    "contract body",
				Date:    base,
			})
			id := addContractEmail(t, store, accountID, "inbox", "Move me", base, raw)
			assertContractRaw(t, store, accountID, id, raw)

			moved, err := store.MoveEmail(ctx, accountID, id, "sent")
			if err != nil {
				t.Fatalf("MoveEmail() error = %v", err)
			}
			if moved.Ref.ID != id || moved.Ref.MailboxID != "sent" || moved.Email.Subject != "Move me" {
				t.Errorf("MoveEmail() = %+v, want same id in sent", moved)
			}

			inbox, err := store.ListEmails(ctx, accountID, "inbox")
			if err != nil {
				t.Fatalf("ListEmails(inbox) error = %v", err)
			}
			if len(inbox) != 0 {
				t.Errorf("ListEmails(inbox) = %d messages, want 0 after move", len(inbox))
			}
			sent, err := store.ListEmails(ctx, accountID, "sent")
			if err != nil {
				t.Fatalf("ListEmails(sent) error = %v", err)
			}
			if len(sent) != 1 || sent[0].Ref.ID != id {
				t.Fatalf("ListEmails(sent) = %+v, want moved message %q", sent, id)
			}
			assertContractRaw(t, store, accountID, id, raw)

			if _, err := store.MoveEmail(ctx, accountID, id, "missing"); !errors.Is(err, ErrNotFound) {
				t.Errorf("MoveEmail() to missing mailbox error = %v, want ErrNotFound", err)
			}
			if _, err := store.MoveEmail(ctx, accountID, "missing.eml", "sent"); !errors.Is(err, ErrNotFound) {
				t.Errorf("MoveEmail() missing message error = %v, want ErrNotFound", err)
			}
		})
	}
}
