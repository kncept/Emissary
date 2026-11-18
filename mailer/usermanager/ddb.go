package usermanager

import (
	"context"
	"crypto/pbkdf2"
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
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultUserPageSize = 20
	maxUserPageSize     = 100

	// Password hash scheme constants (legacy, kept for migration)
	passwordHashScheme     = "pbkdf2-sha256"
	passwordHashIterations = 210_000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32

	// bcrypt settings (default for new passwords)
	bcryptCost = 12

	// argon2 settings
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
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
	ID             string   `dynamodbav:"id"`
	Email          string   `dynamodbav:"email"`
	Enabled        bool     `dynamodbav:"enabled"`
	SaltedPassword string   `dynamodbav:"saltedPassword"`
	Salt           string   `dynamodbav:"salt"`
	Rules          []string `dynamodbav:"rules"`
	UserManager    bool     `dynamodbav:"userManager"`
	SystemManager  bool     `dynamodbav:"systemManager"`
	SchemaVersion  string   `dynamodbav:"schemaVersion"`
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
	if !checkPassword(user.Salt, user.SaltedPassword, password) {
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
		return m.admin.user(), nil
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

func (m *DdbManager) Autocomplete(ctx context.Context, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []string{}, nil
	}

	qLower := strings.ToLower(q)
	matches := make([]string, 0, 10)
	cursor := ""

	for len(matches) < 10 {
		users, next, err := m.List(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if strings.Contains(strings.ToLower(u.Email), qLower) {
				matches = append(matches, u.Email)
				if len(matches) >= 10 {
					break
				}
			}
		}
		if next == "" || len(users) == 0 {
			break
		}
		cursor = next
	}

	return matches, nil
}

func (m *DdbManager) Create(ctx context.Context, user *EmissaryUser) error {
	if user == nil {
		return errors.New("user is required")
	}

	id := normalizeUserID(user.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	if _, ok := normalizeAddress(id); !ok {
		return errors.New("user email must be a valid address")
	}
	user.Email = id

	if user.SaltedPassword != "" && user.Salt == "" {
		// Plaintext password provided, hash it with default (bcrypt)
		salt, hashed, err := hashPassword(user.SaltedPassword)
		if err != nil {
			return err
		}
		user.Salt = salt
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

	id := normalizeUserID(user.Email)
	if id == "" {
		return errors.New("user email is required")
	}
	user.Email = id

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
// example a System Manager resetting another user's password).
func (m *DdbManager) ChangePassword(ctx context.Context, email, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password is required")
	}

	user, err := m.Get(ctx, email)
	if err != nil {
		return err
	}
	if oldPassword != "" && !checkPassword(user.Salt, user.SaltedPassword, oldPassword) {
		return ErrInvalidCredentials
	}

	salt, hashed, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	user.Salt = salt
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
	schemaVersion := user.SchemaVersion
	if schemaVersion == "" {
		schemaVersion = "1.0.0"
	}
	return ddbUser{
		ID:             user.Email,
		Email:          user.Email,
		Enabled:        user.Enabled,
		SaltedPassword: user.SaltedPassword,
		Salt:           user.Salt,
		Rules:          user.Rules,
		UserManager:    user.UserManager,
		SystemManager:  user.SystemManager,
		SchemaVersion:  schemaVersion,
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
		Enabled:        u.Enabled,
		SaltedPassword: u.SaltedPassword,
		Salt:           u.Salt,
		Rules:          rules,
		UserManager:    u.UserManager,
		SchemaVersion:  u.SchemaVersion,
	}
}

// hashPassword generates a new password hash using bcrypt (default).
// Returns the salt (with b- prefix) and the hash, or an error.
func hashPassword(password string) (salt, hash string, err error) {
	// Default to bcrypt with b- prefix
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return "b-", string(hashBytes), nil
}

// checkPassword verifies a password against the stored hash using the
// salt prefix to determine the algorithm.
func checkPassword(salt, stored, password string) bool {
	switch {
	case salt == "":
		// Plaintext comparison (insecure, for migration/testing only)
		return subtle.ConstantTimeCompare([]byte(stored), []byte(password)) == 1

	case strings.HasPrefix(salt, "b-"):
		// bcrypt
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil

	case strings.HasPrefix(salt, "a2-"):
		// argon2id
		return verifyArgon2(salt, stored, password)

	case strings.HasPrefix(salt, "p2-"):
		// PBKDF2-SHA256 (legacy format)
		return checkPBKDF2(salt, stored, password)

	default:
		// Unknown prefix - treat as invalid
		return false
	}
}

// verifyArgon2 verifies an argon2id hash.
// The salt format is: a2-$time$memory$threads$keyLen$saltBase64
// The stored hash is the base64-encoded argon2 output.
func verifyArgon2(salt, stored, password string) bool {
	// Parse salt: a2-$time$memory$threads$keyLen$salt
	if !strings.HasPrefix(salt, "a2-") {
		return false
	}
	parts := strings.Split(salt[3:], "$")
	if len(parts) != 5 {
		return false
	}
	time, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return false
	}
	memory, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return false
	}
	threads, err := strconv.ParseUint(parts[2], 10, 8)
	if err != nil {
		return false
	}
	keyLen, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return false
	}
	saltBytes, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expected, err := base64.RawStdEncoding.DecodeString(stored)
	if err != nil {
		return false
	}

	actual := argon2.IDKey([]byte(password), saltBytes, uint32(time), uint32(memory), uint8(threads), uint32(keyLen))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// checkPBKDF2 verifies a legacy PBKDF2-SHA256 hash.
// The salt format is: p2-$iterations$saltBase64
// The stored hash is the base64-encoded PBKDF2 output.
func checkPBKDF2(salt, stored, password string) bool {
	// Parse salt: p2-$iterations$saltBase64
	if !strings.HasPrefix(salt, "p2-") {
		return false
	}
	parts := strings.Split(salt[3:], "$")
	if len(parts) != 2 {
		return false
	}
	iterations, err := strconv.Atoi(parts[0])
	if err != nil || iterations <= 0 {
		return false
	}
	saltBytes, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	expected, err := base64.RawStdEncoding.DecodeString(stored)
	if err != nil {
		return false
	}

	actual, err := pbkdf2.Key(sha256.New, password, saltBytes, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
