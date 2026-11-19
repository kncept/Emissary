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
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

func main() {
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

	// Create user manager
	userManager, err := usermanager.New(usermanager.Config{
		Type:             cfg.UserManager.Type,
		AdminUsername:    cfg.Server.Username,
		AdminPassword:    cfg.Server.Password,
		Domain:           cfg.UserManager.Domain,
		DdbTable:         cfg.UserTableName(),
		Region:           cfg.UserManager.Region,
		S3Bucket:         cfg.Storage.S3Bucket,
		FilesystemDir:    cfg.FilesystemUsersDir(),
		LDAPURL:          cfg.UserManager.LDAPURL,
		LDAPBindDN:       cfg.UserManager.LDAPBindDN,
		LDAPBindPassword: cfg.UserManager.LDAPBindPassword,
		LDAPBaseDN:       cfg.UserManager.LDAPBaseDN,
		LDAPUserFilter:   cfg.UserManager.LDAPUserFilter,
		LDAPCacheTTL:     cfg.UserManager.LDAPCacheTTL,
	})
	if err != nil {
		log.Fatalf("Failed to create user manager: %v", err)
	}

	// Create sender
	var mailSender sender.Sender
	switch cfg.Sender.Type {
	case "aws-ses", "ses":
		mailSender, err = sender.NewAwsSesSender(ctx)
		if err != nil {
			log.Fatalf("Failed to create SES sender: %v", err)
		}
	case "mta":
		mailSender = sender.NewMtaSender(cfg.Sender.MTAPort)
	default:
		mailSender = sender.NewLogSender()
	}

	// Create the configured storage used for delivery. Staging reads and
	// deletes still use the S3 client directly; delivery goes through this
	// store so receiver and frontend see the same messages.
	var emailStore storage.Storage
	switch strings.ToLower(strings.TrimSpace(cfg.Storage.Type)) {
	case "memory":
		emailStore = storage.NewMemoryStorage()
	case "filesystem":
		emailStore, err = storage.NewFileStorage(cfg.FilesystemMailDir())
		if err != nil {
			log.Fatalf("Failed to create email storage: %v", err)
		}
	default:
		emailStore, err = storage.NewS3Storage(cfg.Storage.S3Bucket)
		if err != nil {
			log.Fatalf("Failed to create email storage: %v", err)
		}
	}

	// Create the metadata store used to record JMAP keywords for deliveries.
	metaStore, err := metadata.NewStore(cfg.Metadata.Type, cfg.Metadata.FilePath, cfg.MetadataTableName(), cfg.Metadata.Region)
	if err != nil {
		log.Fatalf("Failed to create metadata store: %v", err)
	}

	// Load inbound routing rules. The backend is selected by RECEIVER;
	// an empty store is seeded with the base fallback rules.
	configStore, err := receiver.NewConfigStore(receiver.ConfigStoreOptions{
		Type:          cfg.Receiver.Type,
		FilesystemDir: cfg.Receiver.FilesystemDir,
		Bucket:        cfg.Receiver.S3Bucket,
		TableName:     cfg.ReceiverTableName(),
		Region:        cfg.Receiver.Region,
	})
	if err != nil {
		log.Fatalf("Failed to create receiver config store: %v", err)
	}
	identityConfig, err := receiver.LoadMailIdentityConfig(ctx, configStore)
	if err != nil {
		log.Fatalf("Failed to load receiver config: %v", err)
	}

	// Create receiver
	rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
		Bucket:      cfg.Storage.S3Bucket,
		UserManager: userManager,
		Sender:      mailSender,
		Domain:      cfg.UserManager.Domain,
		SESClient:   s3Client,
		Storage:     emailStore,
		Metadata:    metaStore,

		IdentityConfig: identityConfig,
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

		log.Printf("Processing S3 object: bucket=%s key=%s", bucket, key)

		if err := rcvr.ProcessS3Event(ctx, bucket, key); err != nil {
			log.Printf("Failed to process message %s: %v", key, err)
			// Continue processing other records
			continue
		}
	}

	return nil
}
