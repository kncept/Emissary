package metadata

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var metaReceivedAt = time.Date(2026, time.November, 18, 1, 2, 3, 0, time.UTC)

// fakeDdb is an in-memory stand-in for the DynamoDB operations used by
// DdbMetadata. Items are keyed by the composite (id, userId) primary key.
type fakeDdb struct {
	mu sync.Mutex

	items    map[string]map[string]types.AttributeValue
	pageSize int
	unsorted bool // return raw map order so stores must impose their own order

	gets        []map[string]types.AttributeValue
	deletes     []map[string]types.AttributeValue
	queryInputs []*dynamodb.QueryInput
}

func newFakeDdb() *fakeDdb {
	return &fakeDdb{items: make(map[string]map[string]types.AttributeValue)}
}

func metadataItemKey(id, userID string) string { return id + "\x00" + userID }

func itemKey(item map[string]types.AttributeValue) string {
	return metadataItemKey(attrString(item, "id"), attrString(item, "userId"))
}

func attrString(item map[string]types.AttributeValue, name string) string {
	value, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func (f *fakeDdb) GetItem(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.gets = append(f.gets, params.Key)
	item, ok := f.items[itemKey(params.Key)]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeDdb) PutItem(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.items[itemKey(params.Item)] = params.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDdb) DeleteItem(_ context.Context, params *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletes = append(f.deletes, params.Key)
	delete(f.items, itemKey(params.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDdb) Query(_ context.Context, params *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// The store reuses one QueryInput across pages, so keep a snapshot of each
	// call rather than a pointer the caller will mutate later.
	snapshot := *params
	f.queryInputs = append(f.queryInputs, &snapshot)

	condition := aws.ToString(params.KeyConditionExpression)
	value := attrString(params.ExpressionAttributeValues, ":userId")
	if condition != "#userId = :userId" || value == "" {
		return nil, errors.New("unsupported query")
	}

	var matches []map[string]types.AttributeValue
	for _, item := range f.items {
		if attrString(item, "userId") == value {
			matches = append(matches, item)
		}
	}
	if !f.unsorted {
		sort.Slice(matches, func(i, j int) bool {
			return attrString(matches[i], "id") < attrString(matches[j], "id")
		})
	}

	// Resume after the supplied exclusive start key, which carries the id.
	start := 0
	if params.ExclusiveStartKey != nil {
		after := attrString(params.ExclusiveStartKey, "id")
		start = len(matches)
		for i, item := range matches {
			if attrString(item, "id") == after {
				start = i + 1
				break
			}
		}
	}

	limit := len(matches)
	if f.pageSize > 0 && f.pageSize < limit {
		limit = f.pageSize
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}

	page := matches[start:end]
	output := &dynamodb.QueryOutput{Items: page}
	if end < len(matches) && len(page) > 0 {
		last := page[len(page)-1]
		output.LastEvaluatedKey = map[string]types.AttributeValue{
			"id":     last["id"],
			"userId": last["userId"],
		}
	}
	return output, nil
}

func newDdbTestMetadata(client ddbClient) *DdbMetadata {
	return &DdbMetadata{client: client, tableName: "metadata"}
}

func TestDdbMetadata_SaveUsesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestMetadata(client)
	ctx := context.Background()

	meta := NewEmailMetadata("alice@example.com", "users/alice/inbox/1.eml", KeywordSeen)
	meta.ReceivedAt = FormatReceivedAt(metaReceivedAt)
	if err := store.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if len(client.items) != 1 {
		t.Fatalf("stored %d items, want 1", len(client.items))
	}
	item := client.items[metadataItemKey(meta.ID, meta.UserID)]
	if item == nil {
		t.Fatalf("item not stored under (id=%q, userId=%q): %+v", meta.ID, meta.UserID, client.items)
	}
	if got := attrString(item, "id"); got != meta.ID {
		t.Errorf("partition key id = %q, want the mail id %q", got, meta.ID)
	}
	if got := attrString(item, "userId"); got != meta.UserID {
		t.Errorf("sort key userId = %q, want the user id %q", got, meta.UserID)
	}
}

func TestDdbMetadata_GetUsesCompositeKeyAndScopesToUser(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestMetadata(client)
	ctx := context.Background()

	meta := NewEmailMetadata("alice@example.com", "users/alice/inbox/1.eml", KeywordSeen)
	if err := store.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Get(ctx, "alice@example.com", meta.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !got.IsRead() || got.ReceivedAt != meta.ReceivedAt {
		t.Errorf("Get() = %+v, want the saved record", got)
	}
	if got.UserID != "alice@example.com" || got.ID != meta.ID {
		t.Errorf("Get() ids = %q/%q, want %q/%q", got.UserID, got.ID, meta.UserID, meta.ID)
	}

	if len(client.gets) != 1 {
		t.Fatalf("GetItem calls = %d, want 1", len(client.gets))
	}
	wantKey := map[string]types.AttributeValue{
		"id":     &types.AttributeValueMemberS{Value: meta.ID},
		"userId": &types.AttributeValueMemberS{Value: "alice@example.com"},
	}
	if !reflect.DeepEqual(client.gets[0], wantKey) {
		t.Errorf("GetItem key = %#v, want %#v", client.gets[0], wantKey)
	}

	// The same mail id for another user must not expose Alice's record.
	other, err := store.Get(ctx, "bob@example.com", meta.ID)
	if err != nil {
		t.Fatalf("Get(other) error = %v", err)
	}
	if other.IsRead() || other.ReceivedAt != "" {
		t.Errorf("Get(other) leaked metadata: %+v", other)
	}
	if other.UserID != "bob@example.com" || other.ID != meta.ID {
		t.Errorf("Get(other) ids = %q/%q, want bob/%q", other.UserID, other.ID, meta.ID)
	}
}

func TestDdbMetadata_GetMissingRecord(t *testing.T) {
	store := newDdbTestMetadata(newFakeDdb())

	got, err := store.Get(context.Background(), "alice@example.com", "absent")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.UserID != "alice@example.com" || got.ID != "absent" {
		t.Errorf("Get() ids = %q/%q, want alice/absent", got.UserID, got.ID)
	}
	if got.ReceivedAt != "" || len(got.Keywords) != 0 {
		t.Errorf("Get() for missing record = %+v, want no stored state", got)
	}
}

func TestDdbMetadata_SaveRequiresKeys(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestMetadata(client)
	ctx := context.Background()

	if err := store.Save(ctx, EmailMetadata{ID: "mail-1"}); err == nil {
		t.Error("Save() without a userId should fail")
	}
	if err := store.Save(ctx, EmailMetadata{UserID: "alice@example.com"}); err == nil {
		t.Error("Save() without an id should fail")
	}
	if len(client.items) != 0 {
		t.Errorf("invalid saves wrote %d items, want 0", len(client.items))
	}
}

func TestDdbMetadata_DeleteUsesCompositeKey(t *testing.T) {
	client := newFakeDdb()
	store := newDdbTestMetadata(client)
	ctx := context.Background()

	meta := NewEmailMetadata("alice@example.com", "mail-1", KeywordSeen)
	if err := store.Save(ctx, meta); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Deleting another user's view of the same mail id must not remove it.
	if err := store.Delete(ctx, "bob@example.com", meta.ID); err != nil {
		t.Fatalf("Delete(other) error = %v", err)
	}
	if len(client.items) != 1 {
		t.Fatalf("Delete(other) removed the item: %+v", client.items)
	}

	if err := store.Delete(ctx, "alice@example.com", meta.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(client.items) != 0 {
		t.Errorf("Delete() left items behind: %+v", client.items)
	}
	wantKey := map[string]types.AttributeValue{
		"id":     &types.AttributeValueMemberS{Value: meta.ID},
		"userId": &types.AttributeValueMemberS{Value: "alice@example.com"},
	}
	if len(client.deletes) != 2 || !reflect.DeepEqual(client.deletes[1], wantKey) {
		t.Errorf("DeleteItem keys = %#v, want second key %#v", client.deletes, wantKey)
	}

	// DynamoDB deletes are idempotent.
	if err := store.Delete(ctx, "alice@example.com", "absent"); err != nil {
		t.Errorf("Delete(absent) error = %v, want nil", err)
	}
}

func TestDdbMetadata_ListUsesUserIndexAndPaginates(t *testing.T) {
	client := newFakeDdb()
	client.pageSize = 1
	store := newDdbTestMetadata(client)
	ctx := context.Background()

	for _, meta := range []EmailMetadata{
		NewEmailMetadata("alice@example.com", "mail-1", KeywordSeen),
		NewEmailMetadata("alice@example.com", "mail-2", KeywordFlagged),
		NewEmailMetadata("bob@example.com", "mail-3", KeywordSeen),
	} {
		if err := store.Save(ctx, meta); err != nil {
			t.Fatalf("Save(%s/%s) error = %v", meta.UserID, meta.ID, err)
		}
	}

	list, err := store.List(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List(alice) returned %d records, want 2: %+v", len(list), list)
	}
	for _, meta := range list {
		if meta.UserID != "alice@example.com" {
			t.Errorf("List(alice) returned a record for %q", meta.UserID)
		}
	}
	if list[0].ID != "mail-1" || !list[0].IsRead() {
		t.Errorf("List(alice)[0] = %+v, want mail-1 with $seen", list[0])
	}
	if list[1].ID != "mail-2" || !list[1].IsFlagged() {
		t.Errorf("List(alice)[1] = %+v, want mail-2 with $flagged", list[1])
	}

	if len(client.queryInputs) != 2 {
		t.Fatalf("Query calls = %d, want 2 pages", len(client.queryInputs))
	}
	for _, input := range client.queryInputs {
		if got := aws.ToString(input.IndexName); got != metadataUserIndex {
			t.Errorf("Query index = %q, want %q", got, metadataUserIndex)
		}
		if got := aws.ToString(input.KeyConditionExpression); got != "#userId = :userId" {
			t.Errorf("Query condition = %q, want #userId = :userId", got)
		}
		if got := attrString(input.ExpressionAttributeValues, ":userId"); got != "alice@example.com" {
			t.Errorf("Query userId value = %q, want alice@example.com", got)
		}
	}
	if client.queryInputs[0].ExclusiveStartKey != nil {
		t.Error("first page must not set ExclusiveStartKey")
	}
	if client.queryInputs[1].ExclusiveStartKey == nil {
		t.Error("second page must resume from LastEvaluatedKey")
	}
}

func TestDdbMetadata_ListEmptyIsNotNil(t *testing.T) {
	store := newDdbTestMetadata(newFakeDdb())

	list, err := store.List(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if list == nil {
		t.Error("List() returned nil, want an empty slice")
	}
	if len(list) != 0 {
		t.Errorf("List() = %+v, want empty", list)
	}
}

func TestDdbMetadata_Name(t *testing.T) {
	if got := newDdbTestMetadata(newFakeDdb()).Name(); got != "ddb" {
		t.Errorf("Name() = %q, want ddb", got)
	}
}
