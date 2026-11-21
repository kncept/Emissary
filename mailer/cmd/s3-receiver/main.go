package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mailerconfig "github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/jmap/delivery"
	"github.com/kncept/Emissary/mailer/logging"
	"github.com/kncept/Emissary/mailer/receiver"
)

func main() {
	logging.Configure()
	slog.Info(envutil.StartupMessage("s3-receiver"))

	lambda.Start(handler)
}

// rawEventProcessor is the narrow receiver seam used by the Lambda adapter.
type rawEventProcessor interface {
	ProcessEvent(ctx context.Context, event receiver.InboundRawEmailEvent) error
}

// processEvent processes every inbound record, then returns all failures so
// Lambda marks the invocation failed and retries. Failures are joined rather
// than returned immediately so one bad message cannot hide the others.
func processEvent(ctx context.Context, rcvr rawEventProcessor, s3Event events.S3Event, bucket string, s3Client receiver.S3API) error {
	var errs []error
	for _, record := range s3Event.Records {
		recordBucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		if !receiver.IsInboundEmailKey(key) {
			slog.Debug("Ignoring nested S3 object", "bucket", recordBucket, "key", key)
			continue
		}
		if recordBucket != bucket {
			slog.Warn("S3 object bucket does not match configured mail bucket",
				"key", key, "bucket", recordBucket, "expected_bucket", bucket)
			continue
		}

		slog.Debug("Processing S3 object", "bucket", recordBucket, "key", key)
		event := receiver.NewRawS3Event(s3Client, bucket, key)
		if err := rcvr.ProcessEvent(ctx, event); err != nil {
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
		UserManager:    components.UserManager,
		Sender:         components.Sender,
		Domain:         cfg.UserManager.Domain,
		Storage:        components.Storage,
		Metadata:       components.Metadata,
		Dedup:          receiver.NewS3Dedup(s3Client, cfg.Storage.S3Bucket),
		IdentityConfig: components.ReceiverConfig,
		EmailDelivery:  delivery.New(components.Storage),
	})
	if err != nil {
		return fmt.Errorf("failed to create receiver: %w", err)
	}

	return processEvent(ctx, rcvr, s3Event, cfg.Storage.S3Bucket, s3Client)
}
