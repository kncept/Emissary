package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	ID        string    `dynamodbav:"id"`        // session ID (sid)
	UserEmail string    `dynamodbav:"userEmail"` // user's email (sub)
	Issue     int       `dynamodbav:"issue"`     // current issue number
	CreatedAt time.Time `dynamodbav:"createdAt"` // when session was created
	ExpiresAt time.Time `dynamodbav:"expiresAt"` // when refresh token expires
}

// SessionStore defines the interface for session storage.
type SessionStore interface {
	Get(ctx context.Context, id string) (*Session, error)
	Create(ctx context.Context, s *Session) error
	UpdateIssue(ctx context.Context, id string, expectedIssue, newIssue int) error
	Delete(ctx context.Context, id string) error
	// DeleteByUser revokes every session belonging to the given user. It is
	// used after a password change so old refresh tokens cannot be exchanged.
	DeleteByUser(ctx context.Context, userEmail string) error
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

func (m *MemorySessionStore) DeleteByUser(_ context.Context, userEmail string) error {
	for id, sess := range m.sessions {
		if strings.EqualFold(sess.UserEmail, userEmail) {
			delete(m.sessions, id)
		}
	}
	return nil
}

func (m *MemorySessionStore) Name() string { return "memory" }

// ddbClient is the subset of the DynamoDB client used by DdbSessionStore. It
// is an interface so the store can be tested without AWS.
type ddbClient interface {
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, params *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

// DdbSessionStore is a DynamoDB-backed session store using a composite key:
// PK = session id (id), SK = user id (userEmail). Get only has the session id,
// so it queries the partition rather than using GetItem; every other operation
// includes both key attributes.
type DdbSessionStore struct {
	client    ddbClient
	tableName string
}

var _ SessionStore = (*DdbSessionStore)(nil)

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

// getKeys queries a session by its partition key and returns the session with
// the composite key needed for UpdateItem/DeleteItem. Get, UpdateIssue and
// Delete all use it so the consistent read, expiry cleanup and key construction
// exist in one place.
func (d *DdbSessionStore) getKeys(ctx context.Context, id string) (*Session, map[string]types.AttributeValue, error) {
	// Query by PK (id) since SK (userEmail) is not known at lookup time.
	// A session id is unique, so there will be at most one item.
	output, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(d.tableName),
		KeyConditionExpression: aws.String("#id = :id"),
		ExpressionAttributeNames: map[string]string{
			"#id": "id",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":id": &types.AttributeValueMemberS{Value: id},
		},
		ConsistentRead: aws.Bool(true),
		Limit:          aws.Int32(1),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get session: %w", err)
	}

	if len(output.Items) == 0 {
		return nil, nil, ErrSessionNotFound
	}

	var session Session
	if err := attributevalue.UnmarshalMap(output.Items[0], &session); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	keys := sessionKeys(id, session.UserEmail)
	if time.Now().After(session.ExpiresAt) {
		// Clean up expired session using both keys.
		_ = d.deleteWithKeys(ctx, keys)
		return nil, keys, ErrSessionNotFound
	}

	return &session, keys, nil
}

func sessionKeys(id, userEmail string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id":        &types.AttributeValueMemberS{Value: id},
		"userEmail": &types.AttributeValueMemberS{Value: userEmail},
	}
}

func (d *DdbSessionStore) Get(ctx context.Context, id string) (*Session, error) {
	session, _, err := d.getKeys(ctx, id)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (d *DdbSessionStore) Create(ctx context.Context, s *Session) error {
	if s == nil {
		return errors.New("session is required")
	}
	if s.ID == "" {
		return errors.New("session id is required")
	}
	if s.UserEmail == "" {
		return errors.New("session user email is required")
	}

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
	_, keys, err := d.getKeys(ctx, id)
	if err != nil {
		return err
	}

	_, err = d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(d.tableName),
		Key:                 keys,
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
	_, keys, err := d.getKeys(ctx, id)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil // Idempotent: deleting non-existent session is not an error
		}
		return err
	}
	return d.deleteWithKeys(ctx, keys)
}

// DeleteByUser removes every session belonging to userEmail. The sessions
// table is keyed by session id, so a filtered scan (with pagination) is used
// to find the sessions; there is no index on the userEmail sort key.
func (d *DdbSessionStore) DeleteByUser(ctx context.Context, userEmail string) error {
	email := strings.ToLower(strings.TrimSpace(userEmail))
	if email == "" {
		return nil
	}

	var startKey map[string]types.AttributeValue
	for {
		output, err := d.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(d.tableName),
			FilterExpression: aws.String("#userEmail = :userEmail"),
			ExpressionAttributeNames: map[string]string{
				"#userEmail": "userEmail",
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":userEmail": &types.AttributeValueMemberS{Value: email},
			},
			ExclusiveStartKey: startKey,
			ConsistentRead:    aws.Bool(true),
		})
		if err != nil {
			return fmt.Errorf("failed to scan sessions: %w", err)
		}

		for _, item := range output.Items {
			var sess Session
			if err := attributevalue.UnmarshalMap(item, &sess); err != nil {
				return fmt.Errorf("failed to unmarshal session: %w", err)
			}
			if err := d.deleteWithKeys(ctx, sessionKeys(sess.ID, sess.UserEmail)); err != nil {
				return err
			}
		}

		if len(output.LastEvaluatedKey) == 0 {
			return nil
		}
		startKey = output.LastEvaluatedKey
	}
}

// deleteWithKeys deletes a session using the composite key from getKeys.
func (d *DdbSessionStore) deleteWithKeys(ctx context.Context, keys map[string]types.AttributeValue) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key:       keys,
	})
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (d *DdbSessionStore) Name() string { return "ddb" }
