package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	mailerconfig "github.com/kncept/Emissary/mailer/config"
	"github.com/kncept/Emissary/mailer/receiver"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/usermanager"
)

func main() {
	lambda.Start(handler)
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
		mailSender, err = sender.NewAwsSesSender(ctx, cfg.Sender.From)
		if err != nil {
			log.Fatalf("Failed to create SES sender: %v", err)
		}
	default:
		mailSender = sender.NewLogSender(cfg.Sender.From)
	}

	// Create receiver
	rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
		Bucket:       cfg.Storage.S3Bucket,
		UserManager:  userManager,
		Sender:       mailSender,
		Domain:       cfg.UserManager.Domain,
		SESClient:    s3Client,
	})
	if err != nil {
		log.Fatalf("Failed to create receiver: %v", err)
	}

	// Process each record in the S3 event
	for _, record := range s3Event.Records {
		bucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		log.Printf("Processing S3 object: bucket=%s key=%s", bucket, key)

		if err := rcvr.ProcessS3Event(ctx, bucket, key); err != nil {
			log.Printf("Failed to process message %s: %v", key, err)
			// Continue processing other records
			continue
		}
	}

	return nil
}