package receiver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ddbConfigAPI is the subset of DynamoDB used by the receiver config store.
// Keeping it as an interface makes the store testable without contacting AWS.
type ddbConfigAPI interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, params *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

// DdbConfigStore stores one DynamoDB item per mail identity. The table needs
// only a string partition key named "id"; the rules live in a "rules" list
// attribute. Table names are derived from TABLE_PREFIX by mailer/config.
type DdbConfigStore struct {
	client    ddbConfigAPI
	tableName string
}

var _ ConfigStore = (*DdbConfigStore)(nil)

// receiverConfigItem keeps the existing DynamoDB shape: the partition key is
// the identity and "rules" is the ordered list of rules for it. HandlerRules
// repeats the key in memory for the API's flattened response, but the table
// does not need to store it twice.
type receiverConfigItem struct {
	Identity string        `dynamodbav:"id"`
	Rules    []HandlerRule `dynamodbav:"rules"`
}

func NewDdbConfigStore(tableName, region string) (*DdbConfigStore, error) {
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("receiver config ddb requires a table")
	}
	cfg, err := awscfg.LoadDefaultConfig(context.TODO(), awscfg.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return newDdbConfigStore(dynamodb.NewFromConfig(cfg), tableName), nil
}

func newDdbConfigStore(client ddbConfigAPI, tableName string) *DdbConfigStore {
	return &DdbConfigStore{client: client, tableName: tableName}
}

func (d *DdbConfigStore) Name() string { return "ddb" }

func (d *DdbConfigStore) Get(ctx context.Context, identity string) (HandlerRules, error) {
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: normalizeIdentity(identity)},
		},
	})
	if err != nil {
		return HandlerRules{}, fmt.Errorf("read receiver config %q: %w", identity, err)
	}
	if len(output.Item) == 0 {
		return HandlerRules{}, fmt.Errorf("%w: %q", ErrIdentityConfigNotFound, identity)
	}
	var item receiverConfigItem
	if err := attributevalue.UnmarshalMap(output.Item, &item); err != nil {
		return HandlerRules{}, fmt.Errorf("unmarshal receiver config %q: %w", identity, err)
	}
	return HandlerRules{MailIdentity: item.Identity, Rules: item.Rules}, nil
}

func (d *DdbConfigStore) Put(ctx context.Context, identity string, rules HandlerRules) error {
	if err := validateConfigKey(identity); err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(receiverConfigItem{
		Identity: normalizeIdentity(identity),
		Rules:    append([]HandlerRule(nil), rules.Rules...),
	})
	if err != nil {
		return fmt.Errorf("marshal receiver config %q: %w", identity, err)
	}
	if _, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("write receiver config %q: %w", identity, err)
	}
	return nil
}

func (d *DdbConfigStore) Delete(ctx context.Context, identity string) error {
	if err := validateDeletableConfigKey(identity); err != nil {
		return err
	}
	if _, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: normalizeIdentity(identity)},
		},
	}); err != nil {
		return fmt.Errorf("delete receiver config %q: %w", identity, err)
	}
	return nil
}

func (d *DdbConfigStore) List(ctx context.Context) ([]string, error) {
	input := &dynamodb.ScanInput{
		TableName:                aws.String(d.tableName),
		ProjectionExpression:     aws.String("#id"),
		ExpressionAttributeNames: map[string]string{"#id": "id"},
	}
	var keys []string
	for {
		output, err := d.client.Scan(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("list receiver config: %w", err)
		}
		for _, row := range output.Items {
			var item receiverConfigItem
			if err := attributevalue.UnmarshalMap(row, &item); err != nil {
				return nil, fmt.Errorf("unmarshal receiver config key: %w", err)
			}
			if item.Identity != "" {
				keys = append(keys, item.Identity)
			}
		}
		if len(output.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = output.LastEvaluatedKey
	}
	sort.Strings(keys)
	return keys, nil
}
