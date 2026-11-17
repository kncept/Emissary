package main

import (
	"context"
	"log"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kncept/Emissary/mailer/email"
)

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, s3Event events.S3Event) error {
	// Load AWS config
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg)

	for _, record := range s3Event.Records {
		bucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		log.Printf("Processing S3 object: bucket=%s key=%s", bucket, key)

		// Get the object from S3
		getObjectInput := &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		}

		result, err := s3Client.GetObject(ctx, getObjectInput)
		if err != nil {
			log.Printf("Failed to get object from S3: %v", err)
			continue
		}

		// Parse the email
		parsedEmail, err := email.Parse(key, result.Body)
		if err != nil {
			log.Printf("Failed to parse email: %v", err)
			continue
		}

		// Extract recipient addresses
		var recipients []string
		for _, to := range parsedEmail.To {
			recipients = append(recipients, to.Address)
		}
		for _, cc := range parsedEmail.Cc {
			recipients = append(recipients, cc.Address)
		}
		for _, bcc := range parsedEmail.Bcc {
			recipients = append(recipients, bcc.Address)
		}

		log.Printf("File: %s, Recipients: %s", key, strings.Join(recipients, ", "))
	}

	return nil
}