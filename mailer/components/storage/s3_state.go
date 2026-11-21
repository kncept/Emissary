package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/kncept/Emissary/mailer/readlimit"
)

// LoadJMAPState implements JMAPStateStore. S3's ETag is used as the revision
// token so a stale Lambda invocation cannot overwrite a newer counter.
func (s *S3Storage) LoadJMAPState(ctx context.Context, accountID string) ([]byte, string, error) {
	key, err := jmapStateKey(accountID)
	if err != nil {
		return nil, "", err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("failed to get JMAP state %q: %w", key, err)
	}
	defer output.Body.Close()

	data, err := readlimit.ReadAll(output.Body, maxStateBytes)
	if errors.Is(err, readlimit.ErrTooLarge) {
		return nil, "", fmt.Errorf("JMAP state %q exceeds %d bytes", key, maxStateBytes)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to read JMAP state %q: %w", key, err)
	}
	revision := ""
	if output.ETag != nil {
		revision = *output.ETag
	}
	return data, revision, nil
}

// SaveJMAPState implements JMAPStateStore using S3 conditional writes. A
// missing object requires the empty revision and an If-None-Match of "*"; an
// existing object requires an exact ETag match.
func (s *S3Storage) SaveJMAPState(ctx context.Context, accountID string, data []byte, revision string) (string, error) {
	key, err := jmapStateKey(accountID)
	if err != nil {
		return "", err
	}
	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(string(data)),
		ContentType: aws.String("application/json"),
	}
	if revision == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(revision)
	}
	output, err := s.client.PutObject(ctx, input)
	if err != nil {
		if isStatePreconditionFailure(err) {
			return "", ErrJMAPStateConflict
		}
		return "", jmapStateSaveError(key, err)
	}
	newRevision := ""
	if output != nil && output.ETag != nil {
		newRevision = *output.ETag
	}
	return newRevision, nil
}

func isStatePreconditionFailure(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "PreconditionFailed", "ConditionalRequestConflict", "PreconditionFailedException":
		return true
	}
	return false
}
