package usermanager

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	defaultUserPageSize = 20
	maxUserPageSize     = 100

	passwordHashScheme     = "pbkdf2-sha256"
	passwordHashIterations = 210_000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32
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

// ddbUser is the DynamoDB representation of an EmissaryUser.
type ddbUser struct {
	ID             string   `dynamodbav:"id"`
	Username       string   `dynamodbav:"username"`
	Email          string   `dynamodbav:"email"`
	Admin          bool     `dynamodbav:"admin"`
	Enabled        bool     `dynamodbav:"enabled"`
	SaltedPassword string   `dynamodbav:"saltedPassword"`
	Rules          []string `dynamodbav:"rules"`
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
	if user, handled, err := m.admin.authenticate(username, password); handled {
		return user, err
	}

	address, ok := normalizeAddress(username)
	if !ok {
		return nil, ErrInvalidCredentials
	}

	user, err := m.Get(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	// Verify the password before revealing that the account is disabled, so a
	// disabled user with a wrong password is indistinguishable from any other
	// failed login.
	if !checkPassword(user.SaltedPassword, password) {
		return nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	authenticated := user.User
	return &authenticated, nil
}

func (m *DdbManager) Lookup(ctx context.Context, username string) (*User, error) {
	if m.admin.isAdmin(username) {
		user := &User{
			ID:       m.admin.username,
			Username: m.admin.username,
			Admin:    true,
		}
		if isEmail(m.admin.username) {
			user.Email = strings.ToLower(m.admin.username)
		}
		return user, nil
	}

	address, ok := normalizeAddress(username)
	if !ok {
		return nil, ErrInvalidCredentials
	}

	user, err := m.Get(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	lookup := user.User
	return &lookup, nil
}

func (m *DdbManager) List(ctx context.Context, cursor string, pageSize int) ([]*EmissaryUser, string, error) {
	if pageSize <= 0 {
		pageSize = defaultUserPageSize
	}
	if pageSize > maxUserPageSize {
		pageSize = maxUserPageSize
	}

	input := &dynamodb.ScanInput{
		TableName: aws.String(m.table),
		Limit:     aws.Int32(int32(pageSize)),
	}
	if strings.TrimSpace(cursor) != "" {
		startID, err := decodeUserCursor(cursor)
		if err != nil {
			return nil, "", err
		}
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

func (m *DdbManager) Create(ctx context.Context, user *EmissaryUser) error {
	if user == nil {
		return errors.New("user is required")
	}

	id := normalizeUserID(user.ID)
	if id == "" {
		id = normalizeUserID(user.Email)
	}
	if id == "" {
		return errors.New("user id is required")
	}
	if _, ok := normalizeAddress(id); !ok {
		return errors.New("user id must be an email address")
	}

	user.ID = id
	user.Email = normalizeUserID(user.Email)
	if user.Email == "" {
		user.Email = id
	}
	user.Username = strings.TrimSpace(user.Username)
	if user.Username == "" {
		user.Username = id
	}

	if user.SaltedPassword != "" && !isPasswordHash(user.SaltedPassword) {
		hashed, err := hashPassword(user.SaltedPassword)
		if err != nil {
			return err
		}
		user.SaltedPassword = hashed
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
	if user == nil {
		return errors.New("user is required")
	}

	id := normalizeUserID(user.ID)
	if id == "" {
		id = normalizeUserID(user.Email)
	}
	if id == "" {
		return errors.New("user id is required")
	}
	user.ID = id
	user.Email = normalizeUserID(user.Email)
	if user.Email == "" {
		user.Email = id
	}
	user.Username = strings.TrimSpace(user.Username)
	if user.Username == "" {
		user.Username = id
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

// ChangePassword verifies oldPassword when it is non-empty, then replaces the
// stored password with a salted hash of newPassword. An empty oldPassword
// skips verification; callers must have authorized the change themselves (for
// example an administrator resetting another user's password).
func (m *DdbManager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password is required")
	}

	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	if oldPassword != "" && !checkPassword(user.SaltedPassword, oldPassword) {
		return ErrInvalidCredentials
	}

	hashed, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	user.SaltedPassword = hashed
	return m.Save(ctx, user)
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

func normalizeUserID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if address, ok := normalizeAddress(value); ok {
		return address
	}
	return strings.ToLower(value)
}

func encodeUserCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeUserCursor(cursor string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || strings.TrimSpace(string(decoded)) == "" {
		return "", errors.New("invalid cursor")
	}
	return string(decoded), nil
}

func ddbUserFrom(user *EmissaryUser) ddbUser {
	return ddbUser{
		ID:             user.ID,
		Username:       user.Username,
		Email:          user.Email,
		Admin:          user.Admin,
		Enabled:        user.Enabled,
		SaltedPassword: user.SaltedPassword,
		Rules:          user.Rules,
	}
}

func (u ddbUser) emissaryUser() *EmissaryUser {
	rules := u.Rules
	if rules == nil {
		rules = []string{}
	}
	return &EmissaryUser{
		User: User{
			ID:       u.ID,
			Username: u.Username,
			Email:    u.Email,
			Admin:    u.Admin,
		},
		Enabled:        u.Enabled,
		SaltedPassword: u.SaltedPassword,
		Rules:          rules,
	}
}

func isPasswordHash(value string) bool {
	return strings.HasPrefix(value, passwordHashScheme+"$")
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	key, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}

	return strings.Join([]string{
		passwordHashScheme,
		strconv.Itoa(passwordHashIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

func checkPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != passwordHashScheme {
		return false
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}

	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
