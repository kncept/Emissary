package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// s3ConfigAPI is the subset of S3 used by the receiver config store. Keeping
// it as an interface makes the store testable without contacting AWS.
type s3ConfigAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3ConfigStore stores one JSON object per mail identity at
// s3://<bucket>/receiver/<identity>.json.
type S3ConfigStore struct {
	client s3ConfigAPI
	bucket string
}

var _ ConfigStore = (*S3ConfigStore)(nil)

func NewS3ConfigStore(bucket string) (*S3ConfigStore, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("receiver config s3 requires a bucket")
	}
	cfg, err := awscfg.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return newS3ConfigStore(s3.NewFromConfig(cfg), bucket), nil
}

func newS3ConfigStore(client s3ConfigAPI, bucket string) *S3ConfigStore {
	return &S3ConfigStore{client: client, bucket: bucket}
}

func (s *S3ConfigStore) Name() string { return "s3" }

func (s *S3ConfigStore) objectKey(identity string) (string, error) {
	if err := validateConfigKey(identity); err != nil {
		return "", err
	}
	return configPrefix + "/" + normalizeIdentity(identity) + ".json", nil
}

func (s *S3ConfigStore) Get(ctx context.Context, identity string) ([]HandlerRules, error) {
	key, err := s.objectKey(identity)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isConfigObjectNotFound(err) {
			return nil, fmt.Errorf("%w: %q", ErrIdentityConfigNotFound, identity)
		}
		return nil, fmt.Errorf("read receiver config %q: %w", identity, err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("read receiver config %q: %w", identity, err)
	}
	var rules []HandlerRules
	if err := json.Unmarshal(body, &rules); err != nil {
		return nil, fmt.Errorf("unmarshal receiver config %q: %w", identity, err)
	}
	return rules, nil
}

func (s *S3ConfigStore) Put(ctx context.Context, identity string, rules []HandlerRules) error {
	key, err := s.objectKey(identity)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal receiver config %q: %w", identity, err)
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(string(body)),
		ContentType: aws.String("application/json"),
	}); err != nil {
		return fmt.Errorf("write receiver config %q: %w", identity, err)
	}
	return nil
}

func (s *S3ConfigStore) List(ctx context.Context) ([]string, error) {
	prefix := configPrefix + "/"
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list receiver config objects: %w", err)
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			relative := strings.TrimPrefix(key, prefix)
			if relative == key || relative == "" || strings.Contains(relative, "/") || !strings.HasSuffix(relative, ".json") {
				continue
			}
			keys = append(keys, strings.TrimSuffix(relative, ".json"))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func isConfigObjectNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}
