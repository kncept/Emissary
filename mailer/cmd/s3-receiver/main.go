package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mailerconfig "github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/logging"
	"github.com/kncept/Emissary/mailer/receiver"
)

func main() {
	logging.Configure()
	slog.Info(envutil.StartupMessage("s3-receiver"))

	lambda.Start(handler)
}

// isInboundEmailKey reports whether an S3 object is a raw inbound message.
// SES writes messages at the bucket root; everything with a slash is a nested
// object such as a receiver config file under receiver/, and must not be
// processed or deleted as mail.
func isInboundEmailKey(key string) bool {
	return !strings.Contains(key, "/")
}

// messageProcessor is the part of Receiver used by the Lambda entry point. It
// lets event handling be tested without constructing AWS clients.
type messageProcessor interface {
	ProcessS3Event(ctx context.Context, key string) error
}

// processEvent processes every inbound record, then returns all failures so
// Lambda marks the invocation failed and retries. Failures are joined rather
// than returned immediately so one bad message cannot hide the others.
func processEvent(ctx context.Context, rcvr messageProcessor, s3Event events.S3Event, bucket string) error {
	var errs []error
	for _, record := range s3Event.Records {
		recordBucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		if !isInboundEmailKey(key) {
			slog.Debug("Ignoring nested S3 object", "bucket", recordBucket, "key", key)
			continue
		}
		if recordBucket != bucket {
			slog.Warn("S3 object bucket does not match configured mail bucket",
				"key", key, "bucket", recordBucket, "expected_bucket", bucket)
			continue
		}

		slog.Debug("Processing S3 object", "bucket", recordBucket, "key", key)
		if err := rcvr.ProcessS3Event(ctx, key); err != nil {
			slog.Error("Failed to process message", "key", key, "err", err)
			errs = append(errs, fmt.Errorf("process %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

func handler(ctx context.Context, s3Event events.S3Event) error {
	// Load configuration from environment
	cfg, err := mailerconfig.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Apply the same MIME cap as the frontend so MAX_FILESIZE is consistent
	// across both Lambdas.
	email.SetMaxPartSize(cfg.MaxFileSize)

	// Load AWS config
	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg)

	// Vend every runtime component from its config subtype. Staging reads and
	// deletes still use the S3 client directly; delivery goes through the
	// vendored store so receiver and frontend see the same messages.
	components, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create components from configuration: %w", err)
	}

	// Deduplication markers live in the staging bucket so they survive Lambda
	// restarts and prevent an event retry from delivering mail twice.
	rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
		Bucket:      cfg.Storage.S3Bucket,
		UserManager: components.UserManager,
		Sender:      components.Sender,
		Domain:      cfg.UserManager.Domain,
		SESClient:   s3Client,
		Storage:     components.Storage,
		Metadata:    components.Metadata,
		Dedup:       receiver.NewS3Dedup(s3Client, cfg.Storage.S3Bucket),

		IdentityConfig: components.ReceiverConfig,
	})
	if err != nil {
		return fmt.Errorf("failed to create receiver: %w", err)
	}

	return processEvent(ctx, rcvr, s3Event, cfg.Storage.S3Bucket)
}
