package base

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
)

func TestThreadIDIsStableDigestNotEmailAlias(t *testing.T) {
	msg := &mailemail.UserEmail{
		From:      mailemail.Address{Address: "sender@example.com"},
		To:        []mailemail.Address{{Address: "recipient@example.com"}},
		Subject:   "Thread fixture",
		Date:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:      "body",
		MessageID: "<fixture@example.com>",
	}
	id := ThreadID("email-1", msg)
	if id == "" || id == "email-1" {
		t.Fatalf("ThreadID() = %q, want a stable digest distinct from the email id", id)
	}
	if again := ThreadID("email-1", msg); again != id {
		t.Fatalf("ThreadID() is not stable: %q then %q", id, again)
	}
	if other := ThreadID("email-2", msg); other == id {
		t.Fatal("ThreadID() reused a thread for two emails despite 1:1 grouping")
	}
	// Without threading headers the digest must still not be the email id.
	msg.MessageID = ""
	if fallback := ThreadID("email-1", msg); fallback == "" || fallback == "email-1" {
		t.Fatalf("ThreadID() without Message-ID = %q, want a digest", fallback)
	}
}

func TestNormalizeSubjectStripsPrefixesAndWhitespace(t *testing.T) {
	for input, want := range map[string]string{
		"Re: Project  update":               "projectupdate",
		"[Team] Re[2]: Fwd: Project update": "projectupdate",
		"FWD: RE: Hello World":              "helloworld",
		"Project update":                    "projectupdate",
	} {
		if got := NormalizeSubject(input); got != want {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGroupThreadsFollowsReplyChains(t *testing.T) {
	const accountID = "alice@example.com"
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	deps := &Deps{Storage: store, Metadata: metaStore}

	listed := []*mailemail.ListedEmail{
		threadFixture("e1", "Project update", []string{"<m1@example.com>"}, nil, "2026-01-01T09:00:00Z"),
		threadFixture("e2", "Re: Project update", []string{"<m2@example.com>"}, []string{"<m1@example.com>"}, "2026-01-01T10:00:00Z"),
		threadFixture("e3", "[Team] Re: Re: Project update", []string{"<m3@example.com>"},
			[]string{"<m1@example.com>", "<m2@example.com>"}, "2026-01-01T11:00:00Z"),
		threadFixture("e4", "Project update", []string{"<other@example.com>"}, nil, "2026-01-01T08:00:00Z"),
		threadFixture("e5", "Different topic", []string{"<m5@example.com>"}, []string{"<m1@example.com>"}, "2026-01-01T12:00:00Z"),
	}
	for _, email := range listed {
		store.Add(email)
		meta := metadata.EmptyEmailMetadata(accountID, email.Ref.ID)
		meta.ReceivedAt = metadata.FormatReceivedAt(email.Email.Date)
		if err := metaStore.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%s) error = %v", email.Ref.ID, err)
		}
	}

	groups, byEmail, err := GroupThreads(ctx, deps, accountID, listed)
	if err != nil {
		t.Fatalf("GroupThreads() error = %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %+v, want 3 threads", groups)
	}
	conversation := groups[0]
	for _, group := range groups {
		if reflect.DeepEqual(group.EmailIDs, []string{"e1", "e2", "e3"}) {
			conversation = group
		}
	}
	if !reflect.DeepEqual(conversation.EmailIDs, []string{"e1", "e2", "e3"}) {
		t.Fatalf("conversation ids = %v, want [e1 e2 e3] (groups %+v)", conversation.EmailIDs, groups)
	}
	for _, id := range []string{"e1", "e2", "e3"} {
		if byEmail[id] != conversation.ID {
			t.Errorf("byEmail[%s] = %q, want conversation thread %q", id, byEmail[id], conversation.ID)
		}
	}
	if byEmail["e4"] == conversation.ID || byEmail["e5"] == conversation.ID {
		t.Error("unrelated and subject-changed emails joined the conversation thread")
	}

	// A different input order must not change thread ids or membership.
	reversed := append([]*mailemail.ListedEmail(nil), listed...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	again, againByEmail, err := GroupThreads(ctx, deps, accountID, reversed)
	if err != nil {
		t.Fatalf("GroupThreads(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(againByEmail, byEmail) {
		t.Errorf("thread ids changed with input order: %v vs %v", againByEmail, byEmail)
	}
	for _, group := range again {
		if group.ID == conversation.ID && !reflect.DeepEqual(group.EmailIDs, conversation.EmailIDs) {
			t.Errorf("membership changed with input order: %v vs %v", group.EmailIDs, conversation.EmailIDs)
		}
	}
}

func threadFixture(id, subject string, messageIDs, inReplyTo []string, receivedAt string) *mailemail.ListedEmail {
	date, err := time.Parse(time.RFC3339, receivedAt)
	if err != nil {
		panic(err)
	}
	return &mailemail.ListedEmail{
		Ref: &mailemail.EmailRef{ID: id, AccountID: "alice@example.com", MailboxID: "inbox"},
		Email: &mailemail.UserEmail{
			Subject:    subject,
			MessageID:  firstOrEmpty(messageIDs),
			InReplyTo:  inReplyTo,
			References: append(append([]string(nil), inReplyTo...), messageIDs...),
			Date:       date,
		},
	}
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func TestPageBoundsClampsUntrustedArguments(t *testing.T) {
	position, limit := -5, 10
	start, end := PageBounds(7, &position, &limit)
	if start != 0 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (0, 7)", start, end)
	}

	position, limit = 100, 2
	start, end = PageBounds(7, &position, &limit)
	if start != 7 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (7, 7)", start, end)
	}

	position, limit = 3, -2
	start, end = PageBounds(7, &position, &limit)
	if start != 3 || end != 3 {
		t.Fatalf("PageBounds() = (%d, %d), want (3, 3)", start, end)
	}
}
