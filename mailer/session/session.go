package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrInvalidIssue    = errors.New("invalid issue number")
)

// Session represents a user session for refresh token management.
type Session struct {
	ID        string    `dynamodbav:"id"`         // session ID (sid)
	UserEmail string    `dynamodbav:"userEmail"`  // user's email (sub)
	Issue     int       `dynamodbav:"issue"`      // current issue number
	CreatedAt time.Time `dynamodbav:"createdAt"`  // when session was created
	ExpiresAt time.Time `dynamodbav:"expiresAt"`  // when refresh token expires
}

// SessionStore defines the interface for session storage.
type SessionStore interface {
	Get(ctx context.Context, id string) (*Session, error)
	Create(ctx context.Context, s *Session) error
	UpdateIssue(ctx context.Context, id string, expectedIssue, newIssue int) error
	Delete(ctx context.Context, id string) error
	Name() string
}

// MemorySessionStore is an in-memory implementation for testing.
type MemorySessionStore struct {
	sessions map[string]*Session
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: make(map[string]*Session)}
}

func (m *MemorySessionStore) Get(ctx context.Context, id string) (*Session, error) {
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if time.Now().After(s.ExpiresAt) {
		delete(m.sessions, id)
		return nil, ErrSessionNotFound
	}
	return s, nil
}

func (m *MemorySessionStore) Create(ctx context.Context, s *Session) error {
	m.sessions[s.ID] = s
	return nil
}

func (m *MemorySessionStore) UpdateIssue(ctx context.Context, id string, expectedIssue, newIssue int) error {
	s, ok := m.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if s.Issue != expectedIssue {
		return ErrInvalidIssue
	}
	s.Issue = newIssue
	return nil
}

func (m *MemorySessionStore) Delete(ctx context.Context, id string) error {
	delete(m.sessions, id)
	return nil
}

func (m *MemorySessionStore) Name() string { return "memory" }

// DdbSessionStore is a DynamoDB-backed session store.
type DdbSessionStore struct {
	client    *dynamodb.Client
	tableName string
}

func NewDdbSessionStore(tableName, region string) (*DdbSessionStore, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &DdbSessionStore{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: tableName,
	}, nil
}

func (d *DdbSessionStore) Get(ctx context.Context, id string) (*Session, error) {
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	if output.Item == nil {
		return nil, ErrSessionNotFound
	}

	var session Session
	if err := attributevalue.UnmarshalMap(output.Item, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	if time.Now().After(session.ExpiresAt) {
		// Clean up expired session
		d.Delete(ctx, id)
		return nil, ErrSessionNotFound
	}

	return &session, nil
}

func (d *DdbSessionStore) Create(ctx context.Context, s *Session) error {
	item, err := attributevalue.MarshalMap(s)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      item,
	})
	return err
}

func (d *DdbSessionStore) UpdateIssue(ctx context.Context, id string, expectedIssue, newIssue int) error {
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		ConditionExpression: aws.String("issue = :expected"),
		UpdateExpression:    aws.String("SET issue = :newIssue"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", expectedIssue)},
			":newIssue": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", newIssue)},
		},
	})
	if err != nil {
		// Check if it's a conditional check failed exception
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return ErrInvalidIssue
		}
		return fmt.Errorf("failed to update session issue: %w", err)
	}
	return nil
}

func (d *DdbSessionStore) Delete(ctx context.Context, id string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (d *DdbSessionStore) Name() string { return "ddb" }