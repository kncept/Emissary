package usermanager

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDdb struct {
	mu    sync.Mutex
	items map[string]map[string]types.AttributeValue
}

func newFakeDdb() *fakeDdb {
	return &fakeDdb{items: make(map[string]map[string]types.AttributeValue)}
}

func (f *fakeDdb) GetItem(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := keyID(params.Key)
	return &dynamodb.GetItemOutput{Item: f.items[id]}, nil
}

func (f *fakeDdb) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := keyID(params.Item)
	_, exists := f.items[id]
	condition := aws.ToString(params.ConditionExpression)
	if condition == "attribute_not_exists(id)" && exists {
		return nil, conditionalFailure()
	}
	if condition == "attribute_exists(id)" && !exists {
		return nil, conditionalFailure()
	}
	f.items[id] = params.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDdb) DeleteItem(_ context.Context, params *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := keyID(params.Key)
	if _, exists := f.items[id]; !exists && aws.ToString(params.ConditionExpression) == "attribute_exists(id)" {
		return nil, conditionalFailure()
	}
	delete(f.items, id)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDdb) Scan(_ context.Context, params *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.items))
	for id := range f.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	start := 0
	if params.ExclusiveStartKey != nil {
		start = len(ids)
		after := keyID(params.ExclusiveStartKey)
		for i, id := range ids {
			if id == after {
				start = i + 1
				break
			}
		}
	}

	limit := len(ids)
	if params.Limit != nil && int(*params.Limit) < limit {
		limit = int(*params.Limit)
	}
	end := start + limit
	if end > len(ids) {
		end = len(ids)
	}

	items := make([]map[string]types.AttributeValue, 0, end-start)
	for _, id := range ids[start:end] {
		items = append(items, f.items[id])
	}

	output := &dynamodb.ScanOutput{Items: items}
	if end < len(ids) {
		output.LastEvaluatedKey = userKey(ids[end-1])
	}
	return output, nil
}

func keyID(item map[string]types.AttributeValue) string {
	value, ok := item["id"].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func conditionalFailure() error {
	return &types.ConditionalCheckFailedException{Message: aws.String("condition failed")}
}

func newDdbTestManager() (*DdbManager, *fakeDdb) {
	client := newFakeDdb()
	return &DdbManager{
		admin:  newAdminAuth(Config{AdminUsername: "admin", AdminPassword: "secret"}),
		table:  "users",
		client: client,
	}, client
}

func testUser(email, password string, enabled bool) *EmissaryUser {
	user := &EmissaryUser{
		Enabled:        enabled,
		SaltedPassword: password,
		Rules:          []string{},
	}
	user.ID = email
	user.Username = email
	user.Email = email
	return user
}

func TestDdbManager_Authenticate(t *testing.T) {
	m, fake := newDdbTestManager()
	ctx := context.Background()

	user := testUser("Alice@Example.com", "hunter2", true)
	if err := m.Create(ctx, user); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if user.ID != "alice@example.com" {
		t.Fatalf("Create() id = %q, want normalized address", user.ID)
	}

	stored := fake.items[user.ID]["saltedPassword"].(*types.AttributeValueMemberS).Value
	if stored == "hunter2" || !strings.HasPrefix(stored, passwordHashScheme+"$") {
		t.Errorf("stored password = %q, want a salted hash", stored)
	}

	got, err := m.Authenticate(ctx, "Alice@Example.com", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.ID != "alice@example.com" || got.Email != "alice@example.com" {
		t.Errorf("Authenticate() user = %+v, want normalized address", got)
	}
	if got.Admin {
		t.Error("ordinary user must not be admin")
	}

	if _, err := m.Authenticate(ctx, "alice@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(ctx, "missing@example.com", "hunter2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(ctx, "not-an-address", "hunter2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("invalid address error = %v, want ErrInvalidCredentials", err)
	}

	disabled := testUser("disabled@example.com", "hunter2", false)
	if err := m.Create(ctx, disabled); err != nil {
		t.Fatalf("Create(disabled) error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "disabled@example.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(ctx, "disabled@example.com", "hunter2"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("disabled correct password error = %v, want ErrUserDisabled", err)
	}

	admin, err := m.Authenticate(ctx, "admin", "secret")
	if err != nil || !admin.Admin {
		t.Errorf("admin login = (%+v, %v), want admin", admin, err)
	}
	if _, err := m.Authenticate(ctx, "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong admin password error = %v, want ErrInvalidCredentials", err)
	}
}

func TestDdbManager_Management(t *testing.T) {
	m, _ := newDdbTestManager()
	ctx := context.Background()

	alice := testUser("alice@example.com", "first", true)
	if err := m.Create(ctx, alice); err != nil {
		t.Fatalf("Create(alice) error = %v", err)
	}
	if err := m.Create(ctx, testUser("alice@example.com", "again", true)); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate Create() error = %v, want ErrUserExists", err)
	}
	if err := m.Create(ctx, testUser("not-an-address", "secret", true)); err == nil {
		t.Error("Create() should reject a user id that is not an email address")
	}

	got, err := m.Get(ctx, "Alice@Example.com")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !got.Enabled || got.Rules == nil {
		t.Errorf("Get() = %+v, want enabled user with empty rules", got)
	}

	got.Enabled = false
	got.Rules = []string{"delete"}
	if err := m.Save(ctx, got); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	saved, err := m.Get(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("Get() after Save error = %v", err)
	}
	if saved.Enabled || len(saved.Rules) != 1 || saved.Rules[0] != "delete" {
		t.Errorf("saved user = %+v, want disabled with delete rule", saved)
	}

	missing := testUser("missing@example.com", "secret", true)
	if err := m.Save(ctx, missing); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Save(absent) error = %v, want ErrUserNotFound", err)
	}

	for _, email := range []string{"bob@example.com", "carol@example.com", "dave@example.com"} {
		if err := m.Create(ctx, testUser(email, "secret", true)); err != nil {
			t.Fatalf("Create(%s) error = %v", email, err)
		}
	}

	first, cursor, err := m.List(ctx, "", 2)
	if err != nil {
		t.Fatalf("List(first) error = %v", err)
	}
	if len(first) != 2 || cursor == "" {
		t.Fatalf("List(first) = (%d users, cursor %q), want 2 users and a cursor", len(first), cursor)
	}
	second, cursor, err := m.List(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("List(second) error = %v", err)
	}
	if len(second) != 2 || cursor != "" {
		t.Fatalf("List(second) = (%d users, cursor %q), want 2 users and no cursor", len(second), cursor)
	}
	if _, _, err := m.List(ctx, "not-a-cursor!", 2); err == nil {
		t.Error("List() with an invalid cursor should fail")
	}
}

func TestDdbManager_ChangePassword(t *testing.T) {
	m, _ := newDdbTestManager()
	ctx := context.Background()

	if err := m.Create(ctx, testUser("alice@example.com", "first", true)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := m.ChangePassword(ctx, "alice@example.com", "wrong", "second"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("ChangePassword(wrong old) error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "first"); err != nil {
		t.Fatalf("old password stopped working after a failed change: %v", err)
	}

	if err := m.ChangePassword(ctx, "alice@example.com", "first", "second"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "second"); err != nil {
		t.Errorf("new password login failed: %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "first"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password login error = %v, want ErrInvalidCredentials", err)
	}

	// An administrator reset (no old password) must not verify the old value.
	if err := m.ChangePassword(ctx, "alice@example.com", "", "third"); err != nil {
		t.Fatalf("ChangePassword(admin reset) error = %v", err)
	}
	if _, err := m.Authenticate(ctx, "alice@example.com", "third"); err != nil {
		t.Errorf("reset password login failed: %v", err)
	}
	if err := m.ChangePassword(ctx, "missing@example.com", "", "third"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("ChangePassword(absent) error = %v, want ErrUserNotFound", err)
	}
}

func TestDdbManager_DeleteAndLookup(t *testing.T) {
	m, _ := newDdbTestManager()
	ctx := context.Background()

	if err := m.Create(ctx, testUser("alice@example.com", "secret", true)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := m.Lookup(ctx, "Alice@Example.com")
	if err != nil || user.ID != "alice@example.com" {
		t.Errorf("Lookup() = (%+v, %v), want alice", user, err)
	}
	if _, err := m.Lookup(ctx, "missing@example.com"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Lookup(absent) error = %v, want ErrInvalidCredentials", err)
	}
	if err := m.Create(ctx, testUser("disabled@example.com", "secret", false)); err != nil {
		t.Fatalf("Create(disabled) error = %v", err)
	}
	if _, err := m.Lookup(ctx, "disabled@example.com"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("Lookup(disabled) error = %v, want ErrUserDisabled", err)
	}

	if err := m.Delete(ctx, "Alice@Example.com"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := m.Get(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Get() after Delete error = %v, want ErrUserNotFound", err)
	}
	if err := m.Delete(ctx, "alice@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("second Delete() error = %v, want ErrUserNotFound", err)
	}
}
