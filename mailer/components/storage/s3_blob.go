package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kncept/Emissary/mailer/readlimit"
)

// PutBlob implements BlobStore. Blobs live at users/<account>/_blobs/<id>,
// with a JSON metadata object beside each body.
func (s *S3Storage) PutBlob(ctx context.Context, accountID, contentType string, data []byte) (Blob, error) {
	return s.UploadBlob(ctx, accountID, "", contentType, data, time.Now())
}

// UploadBlob implements BlobStore. Bytes are immutable, so re-uploading the
// same content only rewrites its metadata object to refresh the expiry time.
func (s *S3Storage) UploadBlob(ctx context.Context, accountID, uploaderID, contentType string, data []byte, now time.Time) (Blob, error) {
	account, err := canonicalBlobAccount(accountID)
	if err != nil {
		return Blob{}, err
	}
	if err := validateBlobData(data); err != nil {
		return Blob{}, err
	}
	id := BlobID(data)
	logicalKey, err := blobObjectKey(account, id)
	if err != nil {
		return Blob{}, err
	}
	key := s.objectKey(logicalKey)
	metaKey := s.objectKey(logicalKey + blobMetadataSuffix)

	exists, err := s.ObjectExists(ctx, key)
	if err != nil {
		return Blob{}, err
	}
	if exists {
		meta, metaErr := s.readBlobMetadataS3(ctx, metaKey)
		if metaErr != nil && !errors.Is(metaErr, ErrNotFound) {
			return Blob{}, metaErr
		}
		if metaErr == nil {
			meta = refreshBlobUpload(meta, uploaderID, now)
			if err := s.writeBlobMetadataS3(ctx, metaKey, meta); err != nil {
				return Blob{}, err
			}
			return meta, nil
		}
	}

	meta := newBlobMetadata(account, uploaderID, contentType, data, now)
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return Blob{}, fmt.Errorf("failed to store blob %q: %w", key, err)
	}
	if err := s.writeBlobMetadataS3(ctx, metaKey, meta); err != nil {
		return Blob{}, err
	}
	return meta, nil
}

func (s *S3Storage) readBlobMetadataS3(ctx context.Context, key string) (Blob, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isObjectNotFound(err) {
			return Blob{}, ErrNotFound
		}
		return Blob{}, err
	}
	defer output.Body.Close()
	body, err := readlimit.ReadAll(output.Body, maxStateBytes)
	if err != nil {
		return Blob{}, err
	}
	var meta Blob
	if err := json.Unmarshal(body, &meta); err != nil {
		return Blob{}, err
	}
	return meta, nil
}

func (s *S3Storage) writeBlobMetadataS3(ctx context.Context, key string, meta Blob) error {
	body, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}); err != nil {
		return fmt.Errorf("failed to store blob metadata %q: %w", key, err)
	}
	return nil
}

// GetBlobMeta implements BlobStore without downloading the blob body.
func (s *S3Storage) GetBlobMeta(ctx context.Context, accountID, blobID string) (Blob, error) {
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return Blob{}, err
	}
	key, err := blobMetadataObjectKey(accountID, id)
	if err != nil {
		return Blob{}, err
	}
	return s.readBlobMetadataS3(ctx, s.objectKey(key))
}

// ListBlobs implements BlobStore by listing metadata objects for the account.
func (s *S3Storage) ListBlobs(ctx context.Context, accountID string) ([]Blob, error) {
	dir, err := blobDirKey(accountID)
	if err != nil {
		return nil, err
	}
	prefix := s.objectKey(dir)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	blobs := make([]Blob, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list blobs under %q: %w", prefix, err)
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasSuffix(key, blobMetadataSuffix) {
				continue
			}
			meta, err := s.readBlobMetadataS3(ctx, key)
			if err != nil {
				continue
			}
			blobs = append(blobs, meta)
		}
	}
	sortBlobsOldestFirst(blobs)
	return blobs, nil
}

// AddBlobRefs implements BlobStore. S3 has no compare-and-swap primitive in
// the small client surface used here, so this is a read-modify-write of the
// metadata object; JMAP method calls pin blobs serially in practice.
func (s *S3Storage) AddBlobRefs(ctx context.Context, accountID, blobID string, delta int) (Blob, error) {
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return Blob{}, err
	}
	key, err := blobMetadataObjectKey(accountID, id)
	if err != nil {
		return Blob{}, err
	}
	metaKey := s.objectKey(key)
	meta, err := s.readBlobMetadataS3(ctx, metaKey)
	if err != nil {
		return Blob{}, err
	}
	meta = adjustBlobRefs(meta, delta)
	if err := s.writeBlobMetadataS3(ctx, metaKey, meta); err != nil {
		return Blob{}, err
	}
	return meta, nil
}

// GetBlob implements BlobStore, falling back to delivered email bytes because
// JMAP Email objects expose their raw message as a blobId.
func (s *S3Storage) GetBlob(ctx context.Context, accountID, blobID string) (Blob, []byte, error) {
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return Blob{}, nil, err
	}
	account, err := canonicalBlobAccount(accountID)
	if err != nil {
		return Blob{}, nil, err
	}
	logicalKey, err := blobObjectKey(account, id)
	if err != nil {
		return Blob{}, nil, err
	}
	key := s.objectKey(logicalKey)
	data, err := s.getRawByExactKey(ctx, key)
	if err == nil {
		meta, metaErr := s.readBlobMetadataS3(ctx, s.objectKey(logicalKey+blobMetadataSuffix))
		if metaErr != nil {
			meta = Blob{ID: id, AccountID: account, Type: defaultBlobType, Size: int64(len(data))}
		}
		return meta, data, nil
	}
	if !isObjectNotFound(err) {
		return Blob{}, nil, err
	}

	raw, err := s.GetEmailRaw(ctx, account, id)
	if err != nil {
		return Blob{}, nil, ErrNotFound
	}
	return Blob{
		ID:          id,
		AccountID:   account,
		Type:        emailBlobType,
		Size:        int64(len(raw)),
		EmailBacked: true,
	}, raw, nil
}

// OpenBlob implements BlobStore with the S3 object body as a reader.
func (s *S3Storage) OpenBlob(ctx context.Context, accountID, blobID string) (Blob, io.ReadCloser, error) {
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return Blob{}, nil, err
	}
	account, err := canonicalBlobAccount(accountID)
	if err != nil {
		return Blob{}, nil, err
	}
	logicalKey, err := blobObjectKey(account, id)
	if err != nil {
		return Blob{}, nil, err
	}
	key := s.objectKey(logicalKey)
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err == nil {
		meta, metaErr := s.readBlobMetadataS3(ctx, s.objectKey(logicalKey+blobMetadataSuffix))
		if metaErr != nil {
			meta = Blob{ID: id, AccountID: account, Type: defaultBlobType, Size: aws.ToInt64(output.ContentLength)}
		}
		return meta, output.Body, nil
	}
	if !isObjectNotFound(err) {
		return Blob{}, nil, err
	}

	raw, err := s.GetEmailRaw(ctx, account, id)
	if err != nil {
		return Blob{}, nil, ErrNotFound
	}
	return Blob{
		ID:          id,
		AccountID:   account,
		Type:        emailBlobType,
		Size:        int64(len(raw)),
		EmailBacked: true,
	}, io.NopCloser(bytes.NewReader(raw)), nil
}

// DeleteBlob implements BlobStore. Email-backed blobs are deliberately left
// untouched: destroying a message is an Email/set operation, not Blob/copy.
func (s *S3Storage) DeleteBlob(ctx context.Context, accountID, blobID string) error {
	id, err := normalizeBlobID(blobID)
	if err != nil {
		return err
	}
	account, err := canonicalBlobAccount(accountID)
	if err != nil {
		return err
	}
	logicalKey, err := blobObjectKey(account, id)
	if err != nil {
		return err
	}
	key := s.objectKey(logicalKey)
	exists, err := s.ObjectExists(ctx, key)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if meta, metaErr := s.readBlobMetadataS3(ctx, s.objectKey(logicalKey+blobMetadataSuffix)); metaErr == nil && meta.IsPinned() {
		return ErrBlobReferenced
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("failed to delete blob %q: %w", key, err)
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(logicalKey + blobMetadataSuffix)),
	}); err != nil && !isObjectNotFound(err) {
		return fmt.Errorf("failed to delete blob metadata %q: %w", key, err)
	}
	return nil
}

// DeletePushSubscriptions implements PushSubscriptionStore.
func (s *S3Storage) DeletePushSubscriptions(ctx context.Context, credentialID string) error {
	key, err := s.pushSubscriptionsKey(credentialID)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil && !isObjectNotFound(err) {
		return fmt.Errorf("failed to delete push subscriptions %q: %w", key, err)
	}
	return nil
}

func (s *S3Storage) pushSubscriptionsKey(credentialID string) (string, error) {
	key, err := sessionObjectKey(credentialID, "push-subscriptions.json")
	if err != nil {
		return "", err
	}
	return s.objectKey(key), nil
}

// LoadPushSubscriptions implements PushSubscriptionStore.
func (s *S3Storage) LoadPushSubscriptions(ctx context.Context, credentialID string) ([]PushSubscription, error) {
	key, err := s.pushSubscriptionsKey(credentialID)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isObjectNotFound(err) {
			return []PushSubscription{}, nil
		}
		return nil, fmt.Errorf("failed to get push subscriptions %q: %w", key, err)
	}
	defer output.Body.Close()
	body, err := readlimit.ReadAll(output.Body, maxStateBytes)
	if errors.Is(err, readlimit.ErrTooLarge) {
		return nil, fmt.Errorf("push subscriptions %q exceeds %d bytes", key, maxStateBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read push subscriptions %q: %w", key, err)
	}
	var subscriptions []PushSubscription
	if err := json.Unmarshal(body, &subscriptions); err != nil {
		return nil, fmt.Errorf("failed to parse push subscriptions %q: %w", key, err)
	}
	return subscriptions, nil
}

// SavePushSubscriptions implements PushSubscriptionStore.
func (s *S3Storage) SavePushSubscriptions(ctx context.Context, credentialID string, subscriptions []PushSubscription) error {
	key, err := s.pushSubscriptionsKey(credentialID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(subscriptions)
	if err != nil {
		return fmt.Errorf("marshal push subscriptions: %w", err)
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(string(body)),
	}); err != nil {
		return fmt.Errorf("failed to store push subscriptions %q: %w", key, err)
	}
	return nil
}
