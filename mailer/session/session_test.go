package session

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDdb is an in-memory stand-in for the DynamoDB operations used by
// DdbSessionStore. Items are keyed by the composite (id, userEmail) key.
type fakeDdb struct {
	mu sync.Mutex

	items   map[string]map[string]types.AttributeValue
	queries []*dynamodb.QueryInput
	updates []*dynamodb.UpdateItemInput
	deletes []map[string]types.AttributeValue
}

func newFakeDdb() *fakeDdb {
	return &fakeDdb{items: make(map[string]map[string]types.AttributeValue)}
}

func sessionItemKey(id, userEmail string) string { return id + "\x00" + userEmail }

func itemKey(item map[string]types.AttributeValue) string {
	return sessionItemKey(attrString(item, "id"), attrString(item, "userEmail"))
}

func attrString(item map[string]types.AttributeValue, name string) string {
	value, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func attrNumber(item map[string]types.AttributeValue, name string) (int, bool) {
	value, ok := item[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, false
	}
	number, err := strconv.Atoi(value.Value)
	if err != nil {
		return 0, false
	}
	return number, true
}

func (f *fakeDdb) Query(_ context.Context, params *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queries = append(f.queries, params)

	if got := aws.ToString(params.KeyConditionExpression); got != "#id = :id" {
		return nil, errors.New("unsupported query")
	}
	id := attrString(params.ExpressionAttributeValues, ":id")

	var matches []map[string]types.AttributeValue
	for _, item := range f.items {
		if attrString(item, "id") == id {
			matches = append(matches, item)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return attrString(matches[i], "userEmail") < attrString(matches[j], "userEmail")
	})
	if params.Limit != nil && int(*params.Limit) < len(matches) {
		matches = matches[:*params.Limit]
	}
	return &dynamodb.QueryOutput{Items: matches}, nil
}

func (f *fakeDdb) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.items[itemKey(params.Item)] = params.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDdb) UpdateItem(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.updates = append(f.updates, params)

	item, ok := f.items[itemKey(params.Key)]
	if !ok {
		return nil, &types.ConditionalCheckFailedException{Message: aws.String("missing item")}
	}
	expected, ok := attrNumber(params.ExpressionAttributeValues, ":expected")
	current, hasIssue := attrNumber(item, "issue")
	if !ok || !hasIssue || current != expected {
		return nil, &types.ConditionalCheckFailedException{Message: aws.String("issue mismatch")}
	}
	newIssue, ok := attrNumber(params.ExpressionAttributeValues, ":newIssue")
	if !ok {
		return nil, errors.New("missing :newIssue")
	}
	item["issue"] = &types.AttributeValueMemberN{Value: strconv.Itoa(newIssue)}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDdb) DeleteItem(_ context.Context, params *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletes = append(f.deletes, params.Key)
	delete(f.items, itemKey(params.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

func newDdbTestStore(client ddbClient) *DdbSessionStore {
	return &DdbSessionStore{client: client, tableName: "sessions"}
}

func testSession(id, userEmail string, expiresAt time.Time) *Session {
	return &Session{
		ID:        id,
		UserEmail: userEmail,
		Issue:     1,
		CreatedAt: time.Date(2026, time.November, 18, 0, 0, 0, 0, time.UTC),
		ExpiresAt: expiresAt,
	}
}

func TestDdbSessionStore_CreateUsesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestStore(client)
	ctx := context.Background()

	sess := testSession("sid-1", "alice@example.com", time.Now().Add(time.Hour))
	if err := store.Create(ctx, sess); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if len(client.items) != 1 {
		t.Fatalf("stored %d items, want 1", len(client.items))
	}
	item := client.items[sessionItemKey(sess.ID, sess.UserEmail)]
	if item == nil {
		t.Fatalf("item not stored under (id=%q, userEmail=%q): %+v", sess.ID, sess.UserEmail, client.items)
	}
	if got := attrString(item, "id"); got != sess.ID {
		t.Errorf("partition key id = %q, want session id %q", got, sess.ID)
	}
	if got := attrString(item, "userEmail"); got != sess.UserEmail {
		t.Errorf("sort key userEmail = %q, want user id %q", got, sess.UserEmail)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != sess.ID || got.UserEmail != sess.UserEmail || got.Issue != sess.Issue {
		t.Errorf("Get() = %+v, want %+v", got, sess)
	}

	if len(client.queries) != 1 {
		t.Fatalf("Query calls = %d, want 1", len(client.queries))
	}
	query := client.queries[0]
	if !aws.ToBool(query.ConsistentRead) {
		t.Error("Get() must use a consistent read")
	}
	if query.Limit == nil || *query.Limit != 1 {
		t.Errorf("Get() Limit = %v, want 1", query.Limit)
	}
	if got := attrString(query.ExpressionAttributeValues, ":id"); got != sess.ID {
		t.Errorf("Query id value = %q, want %q", got, sess.ID)
	}
}

func TestDdbSessionStore_GetNotFound(t *testing.T) {
	store := newDdbTestStore(newFakeDdb())

	got, err := store.Get(context.Background(), "absent")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get() error = %v, want ErrSessionNotFound", err)
	}
	if got != nil {
		t.Errorf("Get() = %+v, want nil", got)
	}
}

func TestDdbSessionStore_GetExpiredDeletesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestStore(client)
	ctx := context.Background()

	sess := testSession("sid-1", "alice@example.com", time.Now().Add(-time.Minute))
	if err := store.Create(ctx, sess); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := store.Get(ctx, sess.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(expired) error = %v, want ErrSessionNotFound", err)
	}
	if len(client.items) != 0 {
		t.Errorf("expired session was not cleaned up: %+v", client.items)
	}
	wantKey := map[string]types.AttributeValue{
		"id":        &types.AttributeValueMemberS{Value: sess.ID},
		"userEmail": &types.AttributeValueMemberS{Value: sess.UserEmail},
	}
	if len(client.deletes) != 1 || !reflect.DeepEqual(client.deletes[0], wantKey) {
		t.Errorf("DeleteItem keys = %#v, want %#v", client.deletes, wantKey)
	}
}

func TestDdbSessionStore_UpdateIssueUsesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestStore(client)
	ctx := context.Background()

	sess := testSession("sid-1", "alice@example.com", time.Now().Add(time.Hour))
	if err := store.Create(ctx, sess); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := store.UpdateIssue(ctx, sess.ID, 99, 100); !errors.Is(err, ErrInvalidIssue) {
		t.Fatalf("UpdateIssue(stale) error = %v, want ErrInvalidIssue", err)
	}
	if err := store.UpdateIssue(ctx, sess.ID, sess.Issue, 2); err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Issue != 2 {
		t.Errorf("Get().Issue = %d, want 2", got.Issue)
	}

	if len(client.updates) != 2 {
		t.Fatalf("UpdateItem calls = %d, want 2", len(client.updates))
	}
	wantKey := map[string]types.AttributeValue{
		"id":        &types.AttributeValueMemberS{Value: sess.ID},
		"userEmail": &types.AttributeValueMemberS{Value: sess.UserEmail},
	}
	for i, update := range client.updates {
		if !reflect.DeepEqual(update.Key, wantKey) {
			t.Errorf("UpdateItem[%d] key = %#v, want %#v", i, update.Key, wantKey)
		}
	}
}

func TestDdbSessionStore_DeleteUsesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestStore(client)
	ctx := context.Background()

	sess := testSession("sid-1", "alice@example.com", time.Now().Add(time.Hour))
	if err := store.Create(ctx, sess); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := store.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(client.items) != 0 {
		t.Errorf("Delete() left items behind: %+v", client.items)
	}

	wantKey := map[string]types.AttributeValue{
		"id":        &types.AttributeValueMemberS{Value: sess.ID},
		"userEmail": &types.AttributeValueMemberS{Value: sess.UserEmail},
	}
	if len(client.deletes) != 1 || !reflect.DeepEqual(client.deletes[0], wantKey) {
		t.Errorf("DeleteItem keys = %#v, want %#v", client.deletes, wantKey)
	}

	// Deleting a session that is already gone is not an error.
	if err := store.Delete(ctx, sess.ID); err != nil {
		t.Errorf("Delete(absent) error = %v, want nil", err)
	}
}

func TestDdbSessionStore_CreateRequiresKeys(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestStore(client)
	ctx := context.Background()

	if err := store.Create(ctx, nil); err == nil {
		t.Error("Create(nil) should fail")
	}
	if err := store.Create(ctx, &Session{UserEmail: "alice@example.com"}); err == nil {
		t.Error("Create() without an id should fail")
	}
	if err := store.Create(ctx, &Session{ID: "sid-1"}); err == nil {
		t.Error("Create() without a user email should fail")
	}
	if len(client.items) != 0 {
		t.Errorf("invalid creates wrote %d items, want 0", len(client.items))
	}
}

func TestDdbSessionStore_Name(t *testing.T) {
	if got := newDdbTestStore(newFakeDdb()).Name(); got != "ddb" {
		t.Errorf("Name() = %q, want ddb", got)
	}
}
