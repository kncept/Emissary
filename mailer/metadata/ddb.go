package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var _ Metadata = (*DdbMetadata)(nil)

// DdbMetadata stores read state in a DynamoDB table with a string partition
// key named "id".
type DdbMetadata struct {
	client *dynamodb.Client
	table  string
}

func NewDdbMetadata(ctx context.Context, region, table string) (*DdbMetadata, error) {
	if table == "" {
		return nil, errors.New("DynamoDB table is not configured (set EMISSARY_DDB_TABLE)")
	}
	options := make([]func(*awsconfig.LoadOptions) error, 0, 1)
	if region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &DdbMetadata{client: dynamodb.NewFromConfig(cfg), table: table}, nil
}

func (*DdbMetadata) Name() string {
	return "DdbMetadata"
}

func (m *DdbMetadata) IsRead(ctx context.Context, emailID string) (bool, error) {
	output, err := m.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(m.table),
		Key:       key(emailID),
	})
	if err != nil {
		return false, fmt.Errorf("get email metadata: %w", err)
	}
	if output.Item == nil {
		return false, nil
	}
	value, ok := output.Item["read"].(*types.AttributeValueMemberBOOL)
	return ok && value.Value, nil
}

func (m *DdbMetadata) MarkRead(ctx context.Context, emailID string) error {
	_, err := m.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(m.table),
		Item: map[string]types.AttributeValue{
			"id":   &types.AttributeValueMemberS{Value: emailID},
			"read": &types.AttributeValueMemberBOOL{Value: true},
		},
	})
	if err != nil {
		return fmt.Errorf("mark email read: %w", err)
	}
	return nil
}

func (m *DdbMetadata) MarkUnread(ctx context.Context, emailID string) error {
	_, err := m.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(m.table),
		Key:       key(emailID),
	})
	if err != nil {
		return fmt.Errorf("mark email unread: %w", err)
	}
	return nil
}

func key(emailID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberS{Value: emailID},
	}
}
