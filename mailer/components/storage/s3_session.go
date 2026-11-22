package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/readlimit"
)

// PutDraft implements DraftStorage. Drafts are written as JSON under
// _storage/<sid>/drafts/ so they survive across Lambda invocations and are
// never returned by mailbox listings.
func (s *S3Storage) PutDraft(ctx context.Context, sessionID, creationID string, message *email.UserEmail) (string, error) {
	relative, err := draftKey(creationID)
	if err != nil {
		return "", err
	}
	key, err := sessionObjectKey(sessionID, relative)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal draft: %w", err)
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(string(body)),
	}); err != nil {
		return "", fmt.Errorf("failed to store draft %q: %w", key, err)
	}
	return relative, nil
}

// GetDraft implements DraftStorage.
func (s *S3Storage) GetDraft(ctx context.Context, sessionID, id string) (*email.UserEmail, error) {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get draft %q: %w", key, err)
	}
	defer output.Body.Close()

	body, err := email.ReadLimited(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read draft %q: %w", key, err)
	}
	var message email.UserEmail
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("failed to parse draft %q: %w", key, err)
	}
	return &message, nil
}

// DeleteDraft implements DraftStorage. It is idempotent.
func (s *S3Storage) DeleteDraft(ctx context.Context, sessionID, id string) error {
	key, err := sessionObjectKey(sessionID, id)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("failed to delete draft %q: %w", key, err)
	}
	return nil
}

// LoadCreatedIDs implements SessionStateStore. A session with no stored map
// simply returns an empty one and an empty revision. The S3 ETag is the
// optimistic-concurrency token used by SaveCreatedIDs.
func (s *S3Storage) LoadCreatedIDs(ctx context.Context, sessionID string) (map[string]string, string, error) {
	key, err := sessionCreatedIDsKey(sessionID)
	if err != nil {
		return nil, "", err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return map[string]string{}, "", nil
		}
		return nil, "", fmt.Errorf("failed to get created ids %q: %w", key, err)
	}
	defer output.Body.Close()

	body, err := readlimit.ReadAll(output.Body, maxStateBytes)
	if errors.Is(err, readlimit.ErrTooLarge) {
		return nil, "", fmt.Errorf("created ids %q exceeds %d bytes", key, maxStateBytes)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to read created ids %q: %w", key, err)
	}
	ids := make(map[string]string)
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, "", fmt.Errorf("failed to parse created ids %q: %w", key, err)
	}
	revision := ""
	if output.ETag != nil {
		revision = *output.ETag
	}
	return ids, revision, nil
}

// SaveCreatedIDs implements SessionStateStore with an ETag conditional write,
// so concurrent Lambda invocations cannot silently lose each other's creation
// ids. An empty revision creates the object only if it does not exist yet.
func (s *S3Storage) SaveCreatedIDs(ctx context.Context, sessionID string, ids map[string]string, revision string) (string, error) {
	key, err := sessionCreatedIDsKey(sessionID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal created ids: %w", err)
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(string(body)),
	}
	if revision == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(revision)
	}
	output, err := s.client.PutObject(ctx, input)
	if err != nil {
		if isStatePreconditionFailure(err) {
			return "", ErrSessionStateConflict
		}
		return "", fmt.Errorf("failed to store created ids %q: %w", key, err)
	}
	newRevision := ""
	if output != nil && output.ETag != nil {
		newRevision = *output.ETag
	}
	return newRevision, nil
}
