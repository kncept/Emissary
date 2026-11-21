package usermanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ddbClient is the subset of the DynamoDB client used by DdbManager. It is an
// interface so the implementation can be tested without AWS.
type ddbClient interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, params *dynamodb.DeleteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Scan(ctx context.Context, params *dynamodb.ScanInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

// DdbManager is the Emissary-mastered user manager backed by a DynamoDB table.
// The table uses the user's email address as its "id" partition key.
type DdbManager struct {
	admin  adminAuth
	table  string
	region string

	mu     sync.Mutex
	client ddbClient
}

var _ EmissaryAuthManager = (*DdbManager)(nil)

// ddbUser is the DynamoDB representation of an EmissaryUser. The "id"
// partition key holds the user's email address; there is no other user id.
type ddbUser struct {
	ID               string               `dynamodbav:"id"`
	Email            string               `dynamodbav:"email"`
	Enabled          bool                 `dynamodbav:"enabled"`
	SaltedPassword   string               `dynamodbav:"saltedPassword"`
	Salt             string               `dynamodbav:"salt"`
	Rules            []string             `dynamodbav:"rules"`
	UserManager      bool                 `dynamodbav:"userManager"`
	SystemManager    bool                 `dynamodbav:"systemManager"`
	SchemaVersion    string               `dynamodbav:"schemaVersion"`
	VacationResponse *ddbVacationResponse `dynamodbav:"vacationResponse,omitempty"`
}

// ddbVacationResponse is the DynamoDB representation of the vacation settings
// embedded in the user record. A nil pointer decodes to the default, disabled
// singleton for legacy users.
type ddbVacationResponse struct {
	IsEnabled bool                 `dynamodbav:"isEnabled"`
	FromDate  *time.Time           `dynamodbav:"fromDate,omitempty"`
	ToDate    *time.Time           `dynamodbav:"toDate,omitempty"`
	Subject   *string              `dynamodbav:"subject,omitempty"`
	TextBody  *string              `dynamodbav:"textBody,omitempty"`
	HTMLBody  *string              `dynamodbav:"htmlBody,omitempty"`
	Responded map[string]time.Time `dynamodbav:"responded,omitempty"`
}

func NewDdb(cfg Config) *DdbManager {
	return &DdbManager{
		admin:  newAdminAuth(cfg),
		table:  cfg.DdbTable,
		region: cfg.Region,
	}
}

func (m *DdbManager) Name() string { return "ddb" }

func (m *DdbManager) Authenticate(ctx context.Context, username, password string) (*User, error) {
	return authenticateUser(ctx, m, m.admin, username, password)
}

func (m *DdbManager) Lookup(ctx context.Context, username string) (*User, error) {
	return lookupUser(ctx, m, m.admin, username)
}

func (m *DdbManager) List(ctx context.Context, cursor string, pageSize int) ([]*EmissaryUser, string, error) {
	startID, pageSize, err := pageParams(cursor, pageSize, false)
	if err != nil {
		return nil, "", err
	}

	input := &dynamodb.ScanInput{
		TableName: aws.String(m.table),
		Limit:     aws.Int32(int32(pageSize)),
	}
	if startID != "" {
		input.ExclusiveStartKey = userKey(startID)
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return nil, "", err
	}
	output, err := client.Scan(ctx, input)
	if err != nil {
		return nil, "", fmt.Errorf("failed to scan users: %w", err)
	}

	items := make([]ddbUser, 0, len(output.Items))
	if err := attributevalue.UnmarshalListOfMaps(output.Items, &items); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal users: %w", err)
	}

	users := make([]*EmissaryUser, 0, len(items))
	for i := range items {
		users = append(users, items[i].emissaryUser())
	}

	nextCursor := ""
	if len(output.LastEvaluatedKey) > 0 {
		if value, ok := output.LastEvaluatedKey["id"].(*types.AttributeValueMemberS); ok {
			nextCursor = encodeUserCursor(value.Value)
		}
	}

	return users, nextCursor, nil
}

func (m *DdbManager) Autocomplete(ctx context.Context, q string) ([]string, error) {
	return autocompleteFromList(ctx, m, q)
}

func (m *DdbManager) Create(ctx context.Context, user *EmissaryUser) error {
	if err := prepareForCreate(user); err != nil {
		return err
	}

	item, err := attributevalue.MarshalMap(ddbUserFrom(user))
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(m.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		var condition *types.ConditionalCheckFailedException
		if errors.As(err, &condition) {
			return ErrUserExists
		}
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (m *DdbManager) Save(ctx context.Context, user *EmissaryUser) error {
	if err := prepareForSave(user); err != nil {
		return err
	}

	item, err := attributevalue.MarshalMap(ddbUserFrom(user))
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(m.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_exists(id)"),
	})
	if err != nil {
		var condition *types.ConditionalCheckFailedException
		if errors.As(err, &condition) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to save user: %w", err)
	}
	return nil
}

func (m *DdbManager) Get(ctx context.Context, email string) (*EmissaryUser, error) {
	id := normalizeUserID(email)
	if id == "" {
		return nil, ErrUserNotFound
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return nil, err
	}
	output, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(m.table),
		Key:       userKey(id),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if output.Item == nil {
		return nil, ErrUserNotFound
	}

	var item ddbUser
	if err := attributevalue.UnmarshalMap(output.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}
	return item.emissaryUser(), nil
}

func (m *DdbManager) Delete(ctx context.Context, email string) error {
	id := normalizeUserID(email)
	if id == "" {
		return ErrUserNotFound
	}

	client, err := m.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:           aws.String(m.table),
		Key:                 userKey(id),
		ConditionExpression: aws.String("attribute_exists(id)"),
	})
	if err != nil {
		var condition *types.ConditionalCheckFailedException
		if errors.As(err, &condition) {
			return ErrUserNotFound
		}
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

func (m *DdbManager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
	return changePassword(ctx, m, email, oldPassword, newPassword)
}

func (m *DdbManager) getClient(ctx context.Context) (ddbClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client != nil {
		return m.client, nil
	}

	var options []func(*awsconfig.LoadOptions) error
	if strings.TrimSpace(m.region) != "" {
		options = append(options, awsconfig.WithRegion(m.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	m.client = dynamodb.NewFromConfig(cfg)
	return m.client, nil
}

func userKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberS{Value: id},
	}
}

func ddbUserFrom(user *EmissaryUser) ddbUser {
	schemaVersion := user.SchemaVersion
	if schemaVersion == "" {
		schemaVersion = "1.0.0"
	}
	return ddbUser{
		ID:               user.Email,
		Email:            user.Email,
		Enabled:          user.Enabled,
		SaltedPassword:   user.SaltedPassword,
		Salt:             user.Salt,
		Rules:            user.Rules,
		UserManager:      user.UserManager,
		SystemManager:    user.SystemManager,
		SchemaVersion:    schemaVersion,
		VacationResponse: ddbVacationResponseFrom(user.VacationResponse),
	}
}

func ddbVacationResponseFrom(vacation VacationResponse) *ddbVacationResponse {
	if !vacation.IsEnabled && vacation.FromDate == nil && vacation.ToDate == nil &&
		vacation.Subject == nil && vacation.TextBody == nil && vacation.HTMLBody == nil &&
		len(vacation.Responded) == 0 {
		return nil
	}
	return &ddbVacationResponse{
		IsEnabled: vacation.IsEnabled,
		FromDate:  vacation.FromDate,
		ToDate:    vacation.ToDate,
		Subject:   vacation.Subject,
		TextBody:  vacation.TextBody,
		HTMLBody:  vacation.HTMLBody,
		Responded: vacation.Responded,
	}
}

func (d *ddbVacationResponse) vacationResponse() VacationResponse {
	if d == nil {
		return VacationResponse{}
	}
	return VacationResponse{
		IsEnabled: d.IsEnabled,
		FromDate:  d.FromDate,
		ToDate:    d.ToDate,
		Subject:   d.Subject,
		TextBody:  d.TextBody,
		HTMLBody:  d.HTMLBody,
		Responded: d.Responded,
	}
}

func (u ddbUser) emissaryUser() *EmissaryUser {
	rules := u.Rules
	if rules == nil {
		rules = []string{}
	}
	// Older records may only carry the partition key, which is the email.
	address := u.Email
	if address == "" {
		address = u.ID
	}
	return &EmissaryUser{
		User: User{
			Email:         address,
			SystemManager: u.SystemManager,
		},
		Enabled:          u.Enabled,
		SaltedPassword:   u.SaltedPassword,
		Salt:             u.Salt,
		Rules:            rules,
		UserManager:      u.UserManager,
		SchemaVersion:    u.SchemaVersion,
		VacationResponse: u.VacationResponse.vacationResponse(),
	}
}
