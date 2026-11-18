package usermanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory object store implementing the s3Client interface.
// ListObjectsV2 honours Prefix, StartAfter (exclusive) and MaxKeys, so the
// pagination logic under test is exercised.
type fakeS3 struct {
	mu   sync.Mutex
	blob map[string][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{blob: make(map[string][]byte)}
}

func (f *fakeS3) put(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blob[key] = body
}

func (f *fakeS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := aws.ToString(params.Key)
	body, ok := f.blob[key]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("no such key")}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := aws.ToString(params.Key)
	body, err := io.ReadAll(params.Body)
	if err != nil {
		return nil, err
	}
	f.blob[key] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.blob, aws.ToString(params.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, params *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.blob[aws.ToString(params.Key)]; !ok {
		return nil, &types.NotFound{Message: aws.String("not found")}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	prefix := aws.ToString(params.Prefix)
	startAfter := aws.ToString(params.StartAfter)

	keys := make([]string, 0, len(f.blob))
	for key := range f.blob {
		if strings.HasPrefix(key, prefix) && key > startAfter {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	limit := 1000
	if params.MaxKeys != nil && int(*params.MaxKeys) < limit {
		limit = int(*params.MaxKeys)
	}
	if limit > len(keys) {
		limit = len(keys)
	}
	page := keys[:limit]

	contents := make([]types.Object, 0, len(page))
	for _, key := range page {
		contents = append(contents, types.Object{Key: aws.String(key)})
	}

	output := &s3.ListObjectsV2Output{
		Contents:    contents,
		KeyCount:    aws.Int32(int32(len(page))),
		IsTruncated: aws.Bool(len(page) < len(keys)),
	}
	if len(page) < len(keys) && len(page) > 0 {
		output.NextContinuationToken = aws.String(page[len(page)-1])
	}
	return output, nil
}

func newS3TestManager() (*S3Manager, *fakeS3) {
	client := newFakeS3()
	return &S3Manager{
		admin:  newAdminAuth(Config{AdminUsername: "admin", AdminPassword: "secret"}),
		bucket: "test-bucket",
		client: client,
	}, client
}

func TestS3Manager_CreateStoresUserJSON(t *testing.T) {
	m, fake := newS3TestManager()
	ctx := context.Background()

	user := testUser("Alice@Example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	key := "users/alice@example.com/user.json"
	body, ok := fake.blob[key]
	if !ok {
		t.Fatalf("object %q not created; keys = %v", key, fakeKeys(fake))
	}

	// The object must be a plain JSON write-out of the EmissaryUser, with
	// the password stored hashed.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("stored object is not JSON: %v", err)
	}
	wantKeys := []string{"ID", "Username", "Email", "Admin", "Enabled", "SaltedPassword", "Salt", "Rules", "UserManager", "SystemManager", "SchemaVersion"}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("stored object missing field %q: %v", k, raw)
		}
	}
	if len(raw) != len(wantKeys) {
		t.Errorf("stored object has unexpected fields: %v", raw)
	}
	if raw["Salt"] != "b-" {
		t.Errorf("Salt = %v, want bcrypt prefix b-", raw["Salt"])
	}
	if raw["SaltedPassword"] == "hunter2" {
		t.Error("stored password is plaintext, want hashed")
	}

	got, err := m.Get(ctx, "Alice@Example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	// Like the ddb backend, Create normalises ID and Email but keeps the
	// username as supplied.
	if got.ID != "alice@example.com" || got.Email != "alice@example.com" || got.Username != "Alice@Example.com" {
		t.Errorf("Get() = %+v, want ID/Email normalised, username as supplied", got)
	}
	if !got.Enabled {
		t.Error("Get() Enabled = false, want true")
	}
	if !checkPassword(got.Salt, got.SaltedPassword, "hunter2") {
		t.Error("stored password does not verify against the original")
	}

	if _, err := m.Get(ctx, "nobody@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrUserNotFound", err)
	}
}

func TestS3Manager_MailboxWithoutUserJSONIsNotAUser(t *testing.T) {
	m, fake := newS3TestManager()
	ctx := context.Background()

	// A delivered mailbox for bob@example.com, but no user.json record.
	fake.put("users/bob@example.com/inbox/20261117T103045_msg-1.eml", []byte("From: a@b\n\nhi"))

	if _, err := m.Get(ctx, "bob@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Get() error = %v, want ErrUserNotFound", err)
	}
	if _, err := m.Lookup(ctx, "bob@example.com"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Lookup() error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(ctx, "bob@example.com", "anything"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
	users, _, err := m.List(ctx, "", 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) != 0 {
		t.Errorf("List() = %v, want no users", users)
	}
}

func TestS3Manager_Authenticate(t *testing.T) {
	m, _ := newS3TestManager()
	ctx := context.Background()

	user := testUser("alice@example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := m.Authenticate(ctx, "alice@example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.ID != "alice@example.com" || got.Admin {
		t.Errorf("Authenticate() = %+v, want ordinary user alice@example.com", got)
	}

	if _, err := m.Authenticate(ctx, "alice@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Authenticate(wrong password) error = %v, want ErrInvalidCredentials", err)
	}

	// Disabled accounts reject with ErrUserDisabled.
	saved, err := m.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	saved.Enabled = false
	if err := m.Save(ctx, saved); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "hunter2"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("Authenticate(disabled) error = %v, want ErrUserDisabled", err)
	}
	if _, err := m.Lookup(ctx, "alice@example.com"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("Lookup(disabled) error = %v, want ErrUserDisabled", err)
	}

	// The built-in administrator still logs in.
	admin, err := m.Authenticate(ctx, "admin", "secret")
	if err != nil {
		t.Fatalf("Authenticate(admin) error = %v", err)
	}
	if !admin.Admin {
		t.Error("Authenticate(admin) should return the administrator")
	}
}

func TestS3Manager_CreateExisting(t *testing.T) {
	m, _ := newS3TestManager()
	ctx := context.Background()

	user := testUser("alice@example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := m.Create(ctx, testUser("alice@example.com", "other", true)); !errors.Is(err, ErrUserExists) {
		t.Errorf("Create(existing) error = %v, want ErrUserExists", err)
	}
}

func TestS3Manager_Save(t *testing.T) {
	m, _ := newS3TestManager()
	ctx := context.Background()

	if err := m.Save(ctx, testUser("ghost@example.com", "pw", true)); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Save(unknown) error = %v, want ErrUserNotFound", err)
	}

	user := testUser("alice@example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	saved, err := m.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	saved.Enabled = false
	saved.Rules = []string{"bounce"}
	if err := m.Save(ctx, saved); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	again, err := m.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() after Save() error = %v", err)
	}
	if again.Enabled {
		t.Error("Save() did not persist Enabled=false")
	}
	if len(again.Rules) != 1 || again.Rules[0] != "bounce" {
		t.Errorf("Save() did not persist rules: %v", again.Rules)
	}
}

func TestS3Manager_Delete(t *testing.T) {
	m, fake := newS3TestManager()
	ctx := context.Background()

	if err := m.Delete(ctx, "ghost@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Delete(unknown) error = %v, want ErrUserNotFound", err)
	}

	user := testUser("alice@example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Deleting the user must not touch the user's mail.
	fake.put("users/alice@example.com/inbox/20261117T103045_msg-1.eml", []byte("From: a@b\n\nhi"))

	if err := m.Delete(ctx, "alice@example.com"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := m.Get(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Get() after Delete() error = %v, want ErrUserNotFound", err)
	}
	if err := m.Delete(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Delete(again) error = %v, want ErrUserNotFound", err)
	}
	if _, ok := fake.blob["users/alice@example.com/inbox/20261117T103045_msg-1.eml"]; !ok {
		t.Error("Delete() removed the user's mail, want only user.json")
	}
}

func TestS3Manager_ListPagination(t *testing.T) {
	m, fake := newS3TestManager()
	ctx := context.Background()

	// Three users plus a large amount of mail so the list is paginated
	// across many S3 pages (MaxKeys is 100 objects per page).
	for _, name := range []string{"alice", "carol", "dave"} {
		if err := m.Create(ctx, testUser(name+"@example.com", "pw", true)); err != nil {
			t.Fatalf("Create(%s) error = %v", name, err)
		}
	}
	for i := 0; i < 150; i++ {
		fake.put("users/alice@example.com/inbox/20261117T103045_msg-"+string(rune('A'+i/26))+string(rune('a'+i%26))+".eml", []byte("x"))
	}

	seen := make(map[string]int, 3)
	cursor := ""
	for {
		users, next, err := m.List(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("List(cursor=%q) error = %v", cursor, err)
		}
		if len(users) == 0 && next == "" {
			break
		}
		for _, u := range users {
			seen[u.Email]++
		}
		if next == "" {
			break
		}
		cursor = next
	}

	got := make([]string, 0, len(seen))
	for email, count := range seen {
		if count != 1 {
			t.Fatalf("List() returned %s %d times", email, count)
		}
		got = append(got, email)
	}
	sort.Strings(got)
	want := []string{"alice@example.com", "carol@example.com", "dave@example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List() pages = %v, want %v", got, want)
	}
}

func TestS3Manager_Autocomplete(t *testing.T) {
	m, _ := newS3TestManager()
	ctx := context.Background()

	for _, name := range []string{"alice", "carol", "dave"} {
		if err := m.Create(ctx, testUser(name+"@example.com", "pw", true)); err != nil {
			t.Fatalf("Create(%s) error = %v", name, err)
		}
	}

	matches, err := m.Autocomplete(ctx, "AL")
	if err != nil {
		t.Fatalf("Autocomplete() error = %v", err)
	}
	if len(matches) != 1 || matches[0] != "alice@example.com" {
		t.Errorf("Autocomplete(AL) = %v, want [alice@example.com]", matches)
	}

	empty, err := m.Autocomplete(ctx, "")
	if err != nil {
		t.Fatalf("Autocomplete() error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Autocomplete(\"\") = %v, want none", empty)
	}

	none, err := m.Autocomplete(ctx, "zzz")
	if err != nil {
		t.Fatalf("Autocomplete() error = %v", err)
	}
	if len(none) != 0 {
		t.Errorf("Autocomplete(zzz) = %v, want none", none)
	}
}

func TestS3Manager_ChangePassword(t *testing.T) {
	m, _ := newS3TestManager()
	ctx := context.Background()

	user := testUser("alice@example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := m.ChangePassword(ctx, "alice@example.com", "wrong", "newpw"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("ChangePassword(wrong old) error = %v, want ErrInvalidCredentials", err)
	}
	if err := m.ChangePassword(ctx, "alice@example.com", "hunter2", "   "); err == nil {
		t.Error("ChangePassword(blank new) should fail")
	}

	if err := m.ChangePassword(ctx, "alice@example.com", "hunter2", "newpw"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "hunter2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("old password still authenticates after ChangePassword")
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "newpw"); err != nil {
		t.Errorf("new password does not authenticate: %v", err)
	}

	// An administrator reset skips the old password.
	if err := m.ChangePassword(ctx, "alice@example.com", "", "resetpw"); err != nil {
		t.Fatalf("ChangePassword(admin reset) error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "resetpw"); err != nil {
		t.Errorf("reset password does not authenticate: %v", err)
	}
}

func TestNew_S3Backend(t *testing.T) {
	mgr, err := New(Config{Type: "s3", S3Bucket: "bucket", AdminUsername: "admin", AdminPassword: "pw"})
	if err != nil {
		t.Fatalf("New(s3) error = %v", err)
	}
	if mgr.Name() != "s3" {
		t.Errorf("Name() = %q, want s3", mgr.Name())
	}
	if _, ok := mgr.(EmissaryAuthManager); !ok {
		t.Error("s3 backend should implement EmissaryAuthManager")
	}
}

func fakeKeys(fake *fakeS3) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	keys := make([]string, 0, len(fake.blob))
	for key := range fake.blob {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
