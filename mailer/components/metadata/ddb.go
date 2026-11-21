package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// metadataUserIndex is the global secondary index that supports List by user.
// The base table is keyed by message id (partition) and user id (sort), so a
// per-user listing cannot query the base table directly.
const metadataUserIndex = "userId-index"

// ddbClient is the subset of the DynamoDB client used by DdbMetadata. It is an
// interface so the store can be tested without AWS.
type ddbClient interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, params *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// DdbMetadata is a MetadataStore backed by a DynamoDB table with a composite
// key: partition key = message id (id), sort key = user id (userId). Keeping
// the owner in the key means a lookup for the wrong user cannot return another
// user's metadata.
type DdbMetadata struct {
	client    ddbClient
	tableName string
}

var _ MetadataStore = (*DdbMetadata)(nil)

func NewDdbMetadata(tableName, region string) (*DdbMetadata, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &DdbMetadata{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: tableName,
	}, nil
}

// metadataKey returns the composite primary key for a user's mail record.
func metadataKey(userID, mailID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id":     &types.AttributeValueMemberS{Value: mailID},
		"userId": &types.AttributeValueMemberS{Value: userID},
	}
}

func (d *DdbMetadata) Get(ctx context.Context, userID, mailID string) (EmailMetadata, error) {
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.tableName),
		Key:            metadataKey(userID, mailID),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return EmailMetadata{}, fmt.Errorf("failed to get item: %w", err)
	}

	if output.Item == nil {
		// Older messages may have no metadata record.
		return EmptyEmailMetadata(userID, mailID), nil
	}

	var metadata EmailMetadata
	if err := attributevalue.UnmarshalMap(output.Item, &metadata); err != nil {
		return EmailMetadata{}, fmt.Errorf("failed to unmarshal: %w", err)
	}
	return metadata.Normalize(userID, mailID), nil
}

func (d *DdbMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	if metadata.UserID == "" {
		return errors.New("metadata userId is required")
	}
	if metadata.ID == "" {
		return errors.New("metadata id is required")
	}
	return d.put(ctx, metadata)
}

// Delete removes the item for the given user/mail pair. DynamoDB deletes are
// idempotent, so an absent record is not an error.
func (d *DdbMetadata) Delete(ctx context.Context, userID, mailID string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key:       metadataKey(userID, mailID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete item: %w", err)
	}
	return nil
}

func (d *DdbMetadata) put(ctx context.Context, metadata EmailMetadata) error {
	item, err := attributevalue.MarshalMap(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}

	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to save item: %w", err)
	}
	return nil
}

// List returns every metadata record owned by userID. The base table is keyed
// by message id, so this queries the userId-index global secondary index.
func (d *DdbMetadata) List(ctx context.Context, userID string) ([]EmailMetadata, error) {
	input := &dynamodb.QueryInput{
		TableName:              aws.String(d.tableName),
		IndexName:              aws.String(metadataUserIndex),
		KeyConditionExpression: aws.String("#userId = :userId"),
		ExpressionAttributeNames: map[string]string{
			"#userId": "userId",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":userId": &types.AttributeValueMemberS{Value: userID},
		},
	}

	result := make([]EmailMetadata, 0)
	for {
		output, err := d.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to query: %w", err)
		}

		var page []EmailMetadata
		if err := attributevalue.UnmarshalListOfMaps(output.Items, &page); err != nil {
			return nil, fmt.Errorf("failed to unmarshal list: %w", err)
		}
		for i := range page {
			page[i] = page[i].Normalize(userID, page[i].ID)
		}
		result = append(result, page...)

		if len(output.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = output.LastEvaluatedKey
	}

	sortMetadata(result)
	return result, nil
}

func (d *DdbMetadata) Name() string { return "ddb" }
