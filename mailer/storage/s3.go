package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Storage struct {
	client   *s3.Client
	bucket   string
	prefix   string
}

type Email struct {
	ID        string
	From      string
	To        string
	Subject   string
	Body      string
	Timestamp string
	Raw       string
}

func NewS3Storage(bucket, prefix, region string) (*S3Storage, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &S3Storage{
		client: s3.NewFromConfig(cfg),
		bucket: bucket,
		prefix: strings.TrimPrefix(prefix, "/"),
	}, nil
}

func (s *S3Storage) ListEmails(ctx context.Context) ([]Email, error) {
	var emails []Email
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.prefix),
	})

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range output.Contents {
			if obj.Key == nil || strings.HasSuffix(*obj.Key, "/") {
				continue
			}
			email, err := s.GetEmail(ctx, *obj.Key)
			if err != nil {
				continue
			}
			emails = append(emails, email)
		}
	}
	return emails, nil
}

func (s *S3Storage) GetEmail(ctx context.Context, key string) (Email, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return Email{}, fmt.Errorf("failed to get object: %w", err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return Email{}, fmt.Errorf("failed to read body: %w", err)
	}

	return parseEmail(key, string(body)), nil
}

func (s *S3Storage) PutEmail(ctx context.Context, email Email) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + "/" + email.ID + ".eml"),
		Body:   strings.NewReader(email.Raw),
	})
	return err
}

func parseEmail(key, raw string) Email {
	lines := strings.Split(raw, "\n")
	email := Email{
		ID:   strings.TrimSuffix(strings.TrimPrefix(key, ""), ".eml"),
		Raw:  raw,
	}

	bodyStarted := false
	var bodyLines []string
	for _, line := range lines {
		if !bodyStarted && line == "" {
			bodyStarted = true
			continue
		}
		if !bodyStarted {
			if strings.HasPrefix(strings.ToLower(line), "from:") {
				email.From = strings.TrimSpace(line[5:])
			} else if strings.HasPrefix(strings.ToLower(line), "to:") {
				email.To = strings.TrimSpace(line[3:])
			} else if strings.HasPrefix(strings.ToLower(line), "subject:") {
				email.Subject = strings.TrimSpace(line[8:])
			} else if strings.HasPrefix(strings.ToLower(line), "date:") {
				email.Timestamp = strings.TrimSpace(line[5:])
			}
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	email.Body = strings.Join(bodyLines, "\n")
	return email
}

func (s *S3Storage) DeleteEmail(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

type SESClient struct {
	client *s3.Client
}

func NewSESClient(region string) (*SESClient, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &SESClient{client: s3.NewFromConfig(cfg)}, nil
}

func (s *SESClient) SendEmail(ctx context.Context, from, to, subject, body string) error {
	return fmt.Errorf("SES sending not fully implemented - use AWS SES v2 SDK")
}