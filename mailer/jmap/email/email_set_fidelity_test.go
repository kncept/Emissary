package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/storage"
	mailemail "github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/jmap/internal/base"
)

const setFidelityAccount = "alice@example.com"

func newSetFidelityFixture(t *testing.T) (*Handler, *storage.MemoryStorage, *metadata.MemoryMetadata, context.Context) {
	t.Helper()
	store := storage.NewMemoryStorage()
	metaStore := metadata.NewMemoryMetadata()
	ctx := context.Background()
	deps := &base.Deps{Storage: store, Metadata: metaStore, Changes: base.NewTracker(store)}
	return New(deps), store, metaStore, ctx
}

func TestEmailCreateConflictValidation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"low-level headers forbidden", `{"headers":[{"name":"X","value":"y"}]}`, []string{"headers"}},
		{"server-set id forbidden", `{"id":"e1"}`, []string{"id"}},
		{"duplicate header forms", `{"header:from":"x","from":[{"email":"x@example.com"}]}`, []string{"from", "header:from"}},
		{"top-level content header forbidden", `{"header:Content-Type":"text/plain"}`, []string{"header:Content-Type"}},
		{"forbidden parsed form", `{"header:From:asDate":"x"}`, []string{"header:From:asDate"}},
		{"valid parsed header accepted", `{"header:X-Team:asText":"ops"}`, nil},
		{"bodyStructure conflicts with textBody", `{"bodyStructure":{"type":"text/plain"},"textBody":[{"type":"text/plain"}]}`, []string{"bodyStructure", "textBody"}},
		{"textBody requires one text/plain part", `{"textBody":[{"type":"text/html"}]}`, []string{"textBody"}},
		{"partId and blobId conflict", `{"textBody":[{"partId":"1","blobId":"b","type":"text/plain"}]}`, []string{"textBody"}},
		{"partId requires bodyValues", `{"textBody":[{"partId":"1","type":"text/plain"}]}`, []string{"textBody"}},
		{"bodyValues flags must be false", `{"bodyValues":{"1":{"value":"x","isTruncated":true}}}`, []string{"bodyValues"}},
		{"body part headers cannot duplicate From", `{"from":[{"email":"x@example.com"}],"textBody":[{"type":"text/plain","headers":[{"name":"From","value":"x"}]}]}`, []string{"from", "textBody"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateEmailCreate(json.RawMessage(tc.raw))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("validateEmailCreate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmailSetCreateRejectsConflictAndAcceptsMessageID(t *testing.T) {
	h, _, _, ctx := newSetFidelityFixture(t)

	raw, err := h.EmailSet(ctx, setFidelityAccount, json.RawMessage(
		`{"create":{"c1":{"mailboxIds":{"inbox":true},"headers":[]}}}`))
	if err != nil {
		t.Fatalf("EmailSet(conflict) error = %v", err)
	}
	var conflict struct {
		NotCreated map[string]struct {
			Type       string   `json:"type"`
			Properties []string `json:"properties"`
		} `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &conflict); err != nil {
		t.Fatalf("unmarshal conflict response: %v (raw %s)", err, raw)
	}
	setErr, ok := conflict.NotCreated["c1"]
	if !ok || setErr.Type != "invalidProperties" || !reflect.DeepEqual(setErr.Properties, []string{"headers"}) {
		t.Errorf("notCreated.c1 = %+v, want invalidProperties with headers", setErr)
	}

	raw, err = h.EmailSet(ctx, setFidelityAccount, json.RawMessage(
		`{"create":{"c2":{"mailboxIds":{"inbox":true},"messageId":["<client@example.com>"],"subject":"ok"}}}`))
	if err != nil {
		t.Fatalf("EmailSet(create) error = %v", err)
	}
	var created struct {
		Created map[string]json.RawMessage `json:"created"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal create response: %v (raw %s)", err, raw)
	}
	if _, ok := created.Created["c2"]; !ok {
		t.Fatalf("created = %v, want c2 (raw %s)", created.Created, raw)
	}
}

func TestEmailDuplicateDetectionUsesMessageID(t *testing.T) {
	h, store, _, ctx := newSetFidelityFixture(t)
	existingID, err := store.AddEmail(ctx, setFidelityAccount, "inbox", &mailemail.UserEmail{
		Subject:   "existing",
		MessageID: "<dup@example.com>",
	}, rawWithMessageID("<dup@example.com>", "existing"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("create", func(t *testing.T) {
		raw, err := h.EmailSet(ctx, setFidelityAccount, json.RawMessage(
			`{"create":{"c1":{"mailboxIds":{"inbox":true},"messageId":["<dup@example.com>"]}}}`))
		if err != nil {
			t.Fatalf("EmailSet() error = %v", err)
		}
		assertAlreadyExists(t, raw, "notCreated", "c1", existingID)
	})

	t.Run("import", func(t *testing.T) {
		blobID, err := store.PutBlob(ctx, setFidelityAccount, "message/rfc822", rawWithMessageID("<dup@example.com>", "import"))
		if err != nil {
			t.Fatal(err)
		}
		args := fmt.Sprintf(`{"emails":{"c2":{"blobId":%q,"mailboxIds":{"inbox":true}}}}`, blobID.ID)
		raw, err := h.EmailImport(ctx, setFidelityAccount, json.RawMessage(args))
		if err != nil {
			t.Fatalf("EmailImport() error = %v", err)
		}
		assertAlreadyExists(t, raw, "notCreated", "c2", existingID)
	})

	t.Run("copy", func(t *testing.T) {
		args := fmt.Sprintf(`{"fromAccountId":%q,"accountId":%q,"create":{"c3":{"id":%q,"mailboxIds":{"inbox":true}}}}`,
			setFidelityAccount, setFidelityAccount, existingID)
		raw, err := h.EmailCopy(ctx, setFidelityAccount, json.RawMessage(args))
		if err != nil {
			t.Fatalf("EmailCopy() error = %v", err)
		}
		assertAlreadyExists(t, raw, "notCreated", "c3", existingID)
	})
}

func rawWithMessageID(messageID, subject string) []byte {
	return []byte("From: sender@example.net\r\nTo: alice@example.com\r\nMessage-ID: " + messageID +
		"\r\nSubject: " + subject + "\r\nDate: Thu, 01 Jan 2026 09:00:00 +0000\r\n\r\nbody\r\n")
}

func assertAlreadyExists(t *testing.T, raw json.RawMessage, container, key, existingID string) {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal response: %v (raw %s)", err, raw)
	}
	var records map[string]struct {
		Type       string `json:"type"`
		ExistingID string `json:"existingId"`
	}
	if err := json.Unmarshal(response[container], &records); err != nil {
		t.Fatalf("unmarshal %s: %v (raw %s)", container, err, raw)
	}
	setErr, ok := records[key]
	if !ok || setErr.Type != "alreadyExists" || setErr.ExistingID != existingID {
		t.Errorf("%s.%s = %+v, want alreadyExists with existingId %q", container, key, setErr, existingID)
	}
}

func TestStoreSetErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"blob quota", storage.ErrBlobQuota, "overQuota"},
		{"typed size", errors.New("blob exceeds maximum size of 10 bytes"), "tooLarge"},
		{"generic", errors.New("disk unavailable"), "serverFail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := storeSetError(tc.err, "fallback")
			if got.Type != tc.want {
				t.Errorf("type = %q, want %q", got.Type, tc.want)
			}
			if got.Type == "tooLarge" && (got.MaxSize == nil || *got.MaxSize == 0) {
				t.Error("tooLarge SetError is missing maxSize")
			}
		})
	}
}

type conflictMetadata struct {
	metadata.MetadataStore
	fail bool
}

func (c *conflictMetadata) Save(ctx context.Context, meta metadata.EmailMetadata) error {
	if c.fail {
		return metadata.ErrVersionConflict
	}
	return c.MetadataStore.Save(ctx, meta)
}

func TestEmailSetUpdatePropertiesAndStateMismatch(t *testing.T) {
	h, store, metaStore, ctx := newSetFidelityFixture(t)
	emailID, err := store.AddEmail(ctx, setFidelityAccount, "inbox", &mailemail.UserEmail{Subject: "update me"},
		rawWithMessageID("<update@example.com>", "update me"))
	if err != nil {
		t.Fatal(err)
	}
	if err := metaStore.Save(ctx, metadata.NewEmailMetadata(setFidelityAccount, emailID)); err != nil {
		t.Fatal(err)
	}

	raw, err := h.EmailSet(ctx, setFidelityAccount, json.RawMessage(
		fmt.Sprintf(`{"update":{%q:{"keywords":{"$flagged":true}}}}`, emailID)))
	if err != nil {
		t.Fatalf("EmailSet(update) error = %v", err)
	}
	var success struct {
		Updated map[string]json.RawMessage `json:"updated"`
	}
	if err := json.Unmarshal(raw, &success); err != nil {
		t.Fatalf("unmarshal update response: %v (raw %s)", err, raw)
	}
	value, ok := success.Updated[emailID]
	if !ok || strings.TrimSpace(string(value)) != "{}" {
		t.Errorf("updated[%s] = %s, want {}", emailID, value)
	}

	conflicting := &conflictMetadata{MetadataStore: metaStore, fail: true}
	conflictDeps := &base.Deps{Storage: store, Metadata: conflicting, Changes: base.NewTracker(store)}
	raw, err = New(conflictDeps).EmailSet(ctx, setFidelityAccount, json.RawMessage(
		fmt.Sprintf(`{"update":{%q:{"keywords":{"$seen":true}}}}`, emailID)))
	if err != nil {
		t.Fatalf("EmailSet(conflict) error = %v", err)
	}
	var failed struct {
		NotUpdated map[string]struct {
			Type string `json:"type"`
		} `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &failed); err != nil {
		t.Fatalf("unmarshal conflict response: %v (raw %s)", err, raw)
	}
	if got := failed.NotUpdated[emailID].Type; got != "stateMismatch" {
		t.Errorf("notUpdated[%s].type = %q, want stateMismatch (raw %s)", emailID, got, raw)
	}
}

func TestEmailParseNullMapsAndMetadata(t *testing.T) {
	h, store, _, ctx := newSetFidelityFixture(t)
	blob, err := store.PutBlob(ctx, setFidelityAccount, "message/rfc822",
		rawWithMessageID("<parse@example.com>", "parse me"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := h.EmailParse(ctx, setFidelityAccount, json.RawMessage(
		fmt.Sprintf(`{"blobIds":[%q]}`, blob.ID)))
	if err != nil {
		t.Fatalf("EmailParse() error = %v", err)
	}
	var parsed struct {
		Parsed      map[string]map[string]json.RawMessage `json:"parsed"`
		NotParsable json.RawMessage                       `json:"notParsable"`
		NotFound    json.RawMessage                       `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal parse response: %v (raw %s)", err, raw)
	}
	if len(parsed.Parsed) != 1 {
		t.Fatalf("parsed = %v, want one email", parsed.Parsed)
	}
	if string(parsed.NotParsable) != "null" || string(parsed.NotFound) != "null" {
		t.Errorf("notParsable/notFound = %s/%s, want null", parsed.NotParsable, parsed.NotFound)
	}
	object := parsed.Parsed[blob.ID]
	for _, property := range []string{"id", "mailboxIds", "keywords", "receivedAt"} {
		if _, present := object[property]; present {
			t.Errorf("parsed email includes %q, want it omitted", property)
		}
	}

	raw, err = h.EmailParse(ctx, setFidelityAccount, json.RawMessage(
		fmt.Sprintf(`{"blobIds":[%q],"properties":["id","blobId","subject"]}`, blob.ID)))
	if err != nil {
		t.Fatalf("EmailParse(explicit properties) error = %v", err)
	}
	var explicit struct {
		Parsed map[string]map[string]json.RawMessage `json:"parsed"`
	}
	if err := json.Unmarshal(raw, &explicit); err != nil {
		t.Fatalf("unmarshal explicit parse response: %v", err)
	}
	if got := string(explicit.Parsed[blob.ID]["id"]); got != "null" {
		t.Errorf("requested id = %s, want null", got)
	}
	if got := string(explicit.Parsed[blob.ID]["blobId"]); got != fmt.Sprintf("%q", blob.ID) {
		t.Errorf("requested blobId = %s, want %q", got, blob.ID)
	}
}

func TestHeaderPropertyFormValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"valid raw header", `{"properties":["header:X-Team"]}`, false},
		{"valid parsed header", `{"properties":["header:X-Team:asText"]}`, false},
		{"valid :all suffix", `{"bodyProperties":["header:Subject:asText:all"]}`, false},
		{"valid restricted form", `{"properties":["header:From:asAddresses"]}`, false},
		{"forbidden restricted form", `{"properties":["header:From:asDate"]}`, true},
		{"unknown parsed form", `{"properties":["header:X-Team:asNonsense"]}`, true},
		{"malformed suffix", `{"properties":["header:Site:asURLs:first"]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHeaderPropertyForms(json.RawMessage(tc.args), "properties", "bodyProperties")
			if tc.wantErr && err == nil {
				t.Error("validateHeaderPropertyForms() succeeded, want invalidArguments")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateHeaderPropertyForms() error = %v", err)
			}
		})
	}
}

type failingBlobStorage struct {
	storage.Storage
	err error
}

func (f failingBlobStorage) PutBlob(context.Context, string, string, []byte) (storage.Blob, error) {
	return storage.Blob{}, f.err
}

func TestEmailSetCreateMapsBlobQuotaToSetError(t *testing.T) {
	store := storage.NewMemoryStorage()
	deps := &base.Deps{
		Storage:  failingBlobStorage{Storage: store, err: storage.ErrBlobQuota},
		Metadata: metadata.NewMemoryMetadata(),
		Changes:  base.NewTracker(store),
	}
	h := New(deps)

	raw, err := h.EmailSet(context.Background(), setFidelityAccount, json.RawMessage(
		`{"create":{"c1":{"mailboxIds":{"inbox":true},"subject":"quota"}}}`))
	if err != nil {
		t.Fatalf("EmailSet() error = %v", err)
	}
	var response struct {
		NotCreated map[string]struct {
			Type string `json:"type"`
		} `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal create response: %v (raw %s)", err, raw)
	}
	if got := response.NotCreated["c1"].Type; got != "overQuota" {
		t.Errorf("notCreated.c1.type = %q, want overQuota (raw %s)", got, raw)
	}
	listed, err := store.ListEmails(context.Background(), setFidelityAccount, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("email was not rolled back after blob quota failure: %d messages", len(listed))
	}
}

func TestEmailParseRequiresBlobIDs(t *testing.T) {
	h, _, _, ctx := newSetFidelityFixture(t)
	if _, err := h.EmailParse(ctx, setFidelityAccount, json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "invalidArguments") {
		t.Errorf("EmailParse() error = %v, want invalidArguments", err)
	}
}

func TestNormalizeMessageID(t *testing.T) {
	cases := map[string]string{
		"<Dup@Example.COM>": "dup@example.com",
		"  <a@b>  ":         "a@b",
		"not-an-id":         "",
		"":                  "",
	}
	for input, want := range cases {
		if got := normalizeMessageID(input); got != want {
			t.Errorf("normalizeMessageID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseValidationAcceptsReasonableTextBody(t *testing.T) {
	properties := validateEmailCreate(json.RawMessage(
		`{"textBody":[{"type":"text/plain","partId":"1"}],"bodyValues":{"1":{"value":"hello"}}}`))
	sort.Strings(properties)
	if len(properties) != 0 {
		t.Errorf("validateEmailCreate() = %v, want no conflicts", properties)
	}
}
