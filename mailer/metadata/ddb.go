package metadata

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type DdbMetadata struct {
	client    *dynamodb.Client
	tableName string
}

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

func (d *DdbMetadata) Get(ctx context.Context, userID, mailID string) (EmailMetadata, error) {
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"userId": &types.AttributeValueMemberS{Value: userID},
			"id":     &types.AttributeValueMemberS{Value: mailID},
		},
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
	if metadata.UserID == "" {
		metadata.UserID = userID
	}
	if metadata.ID == "" {
		metadata.ID = mailID
	}
	return metadata.Clone(), nil
}

func (d *DdbMetadata) Save(ctx context.Context, metadata EmailMetadata) error {
	return d.put(ctx, metadata)
}

// Delete removes the item for the given user/mail pair. DynamoDB deletes are
// idempotent, so an absent record is not an error.
func (d *DdbMetadata) Delete(ctx context.Context, userID, mailID string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"userId": &types.AttributeValueMemberS{Value: userID},
			"id":     &types.AttributeValueMemberS{Value: mailID},
		},
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
	return err
}

func (d *DdbMetadata) List(ctx context.Context, userID string) ([]EmailMetadata, error) {
	input := &dynamodb.QueryInput{
		TableName:              aws.String(d.tableName),
		KeyConditionExpression: aws.String("#userId = :userId"),
		ExpressionAttributeNames: map[string]string{
			"#userId": "userId",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":userId": &types.AttributeValueMemberS{Value: userID},
		},
	}

	var result []EmailMetadata
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
			if page[i].UserID == "" {
				page[i].UserID = userID
			}
		}
		result = append(result, page...)

		if len(output.LastEvaluatedKey) == 0 {
			break
		}
		input.ExclusiveStartKey = output.LastEvaluatedKey
	}

	if result == nil {
		result = []EmailMetadata{}
	}
	return result, nil
}

func (d *DdbMetadata) Name() string { return "ddb" }
