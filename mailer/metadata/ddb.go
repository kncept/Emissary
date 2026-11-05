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

func (d *DdbMetadata) Get(ctx context.Context, id string) (EmailMetadata, error) {
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return EmailMetadata{}, fmt.Errorf("failed to get item: %w", err)
	}

	if output.Item == nil {
		return EmailMetadata{ID: id}, nil
	}

	var metadata EmailMetadata
	if err := attributevalue.UnmarshalMap(output.Item, &metadata); err != nil {
		return EmailMetadata{}, fmt.Errorf("failed to unmarshal: %w", err)
	}
	return metadata, nil
}

func (d *DdbMetadata) SetRead(ctx context.Context, id string, read bool) error {
	metadata, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	metadata.Read = read
	return d.put(ctx, metadata)
}

func (d *DdbMetadata) SetFlagged(ctx context.Context, id string, flagged bool) error {
	metadata, err := d.Get(ctx, id)
	if err != nil {
		return err
	}
	metadata.Flagged = flagged
	return d.put(ctx, metadata)
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

func (d *DdbMetadata) List(ctx context.Context) ([]EmailMetadata, error) {
	output, err := d.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(d.tableName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan: %w", err)
	}

	var result []EmailMetadata
	if err := attributevalue.UnmarshalListOfMaps(output.Items, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal list: %w", err)
	}
	return result, nil
}