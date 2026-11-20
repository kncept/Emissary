package main

import (
	"context"
	"log"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mailerconfig "github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/receiver"
)

func main() {
	log.Print(envutil.StartupMessage("s3-receiver"))

	lambda.Start(handler)
}

// isInboundEmailKey reports whether an S3 object is a raw inbound message.
// SES writes messages at the bucket root; everything with a slash is a nested
// object such as a receiver config file under receiver/, and must not be
// processed or deleted as mail.
func isInboundEmailKey(key string) bool {
	return !strings.Contains(key, "/")
}

func handler(ctx context.Context, s3Event events.S3Event) error {
	// Load configuration from environment
	cfg, err := mailerconfig.Load(ctx)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Load AWS config
	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	s3Client := s3.NewFromConfig(awsCfg)

	// Vend every runtime component from its config subtype. Staging reads and
	// deletes still use the S3 client directly; delivery goes through the
	// vendored store so receiver and frontend see the same messages.
	components, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to create components from configuration: %v", err)
	}

	// Create receiver
	rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
		Bucket:      cfg.Storage.S3Bucket,
		UserManager: components.UserManager,
		Sender:      components.Sender,
		Domain:      cfg.UserManager.Domain,
		SESClient:   s3Client,
		Storage:     components.Storage,
		Metadata:    components.Metadata,

		IdentityConfig: components.ReceiverConfig,
	})
	if err != nil {
		log.Fatalf("Failed to create receiver: %v", err)
	}

	// Process each record in the S3 event
	for _, record := range s3Event.Records {
		bucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		if !isInboundEmailKey(key) {
			log.Printf("Ignoring nested S3 object: bucket=%s key=%s", bucket, key)
			continue
		}

		if bucket != cfg.Storage.S3Bucket {
			log.Printf("Failed to process message %s: bucket mismatch: expected %s, got %s", key, cfg.Storage.S3Bucket, bucket)
			continue
		}

		log.Printf("Processing S3 object: bucket=%s key=%s", bucket, key)

		if err := rcvr.ProcessS3Event(ctx, key); err != nil {
			log.Printf("Failed to process message %s: %v", key, err)
			// Continue processing other records
			continue
		}
	}

	return nil
}
