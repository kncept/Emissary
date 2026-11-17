// Package receiver implements the inbound mail processing logic for Emissary.
// It reads raw messages from an S3 staging area, validates recipients,
// delivers messages to user mailboxes, and handles bounces for invalid recipients.
package receiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/usermanager"
)

// ErrNoValidRecipients is returned when a message has no valid recipients.
var ErrNoValidRecipients = errors.New("no valid recipients")

// ReceiverConfig holds the configuration for the receiver.
type ReceiverConfig struct {
	// Bucket is the S3 bucket name for inbound messages.
	Bucket string

	// UserManager is used to validate recipient addresses.
	UserManager usermanager.UserManager

	// Sender is used to send bounce messages for invalid recipients.
	Sender sender.Sender

	// Domain is the receiving domain (used for bounce sender address).
	Domain string

	// SESClient is the S3 client for bucket operations.
	SESClient *s3.Client
}

// Receiver processes inbound messages from S3.
type Receiver struct {
	cfg ReceiverConfig
}

// NewReceiver creates a new Receiver with the given configuration.
func NewReceiver(cfg ReceiverConfig) (*Receiver, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("bucket is required")
	}
	if cfg.UserManager == nil {
		return nil, errors.New("user manager is required")
	}
	if cfg.Sender == nil {
		return nil, errors.New("sender is required")
	}
	if cfg.Domain == "" {
		return nil, errors.New("domain is required")
	}
	if cfg.SESClient == nil {
		return nil, errors.New("S3 client is required")
	}
	return &Receiver{cfg: cfg}, nil
}

// ProcessMessage processes a single message from the S3 staging area.
// The key is the SES message ID (object key at bucket root).
// Returns an error if the message cannot be processed.
func (r *Receiver) ProcessMessage(ctx context.Context, key string) error {
	log.Printf("receiver: processing message %s", key)

	// Get the raw message from S3
	obj, err := r.cfg.SESClient.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// Check if object doesn't exist (already processed or deleted)
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			log.Printf("receiver: object %s not found (already processed?)", key)
			return nil
		}
		return fmt.Errorf("failed to get object %s: %w", key, err)
	}
	defer obj.Body.Close()

	// Read the raw message
	rawMessage, err := io.ReadAll(obj.Body)
	if err != nil {
		return fmt.Errorf("failed to read object %s: %w", key, err)
	}

	// Parse the email
	parsedEmail, err := email.Parse(key, strings.NewReader(string(rawMessage)))
	if err != nil {
		return fmt.Errorf("failed to parse email %s: %w", key, err)
	}

	// Extract all recipients from To, Cc, Bcc
	recipients := r.extractRecipients(parsedEmail)
	if len(recipients) == 0 {
		log.Printf("receiver: no recipients found in message %s", key)
		// Still delete the staging object to avoid reprocessing
		if err := r.deleteStagingObject(ctx, key); err != nil {
			return fmt.Errorf("failed to delete staging object after no recipients: %w", err)
		}
		return nil
	}

	log.Printf("receiver: message %s has %d recipient(s): %s", key, len(recipients), strings.Join(recipients, ", "))

	// Validate each recipient and categorize as valid/invalid
	var validRecipients []string
	var invalidRecipients []string

	for _, recipient := range recipients {
		user, err := r.cfg.UserManager.Lookup(ctx, recipient)
		if err != nil {
			// Check if it's a "not found" type error
			if errors.Is(err, usermanager.ErrInvalidCredentials) || errors.Is(err, usermanager.ErrUserNotFound) {
				log.Printf("receiver: recipient %s not found (invalid)", recipient)
				invalidRecipients = append(invalidRecipients, recipient)
				continue
			}
			// Other errors (network, etc.) - treat as invalid to avoid losing mail
			log.Printf("receiver: error looking up %s: %v (treating as invalid)", recipient, err)
			invalidRecipients = append(invalidRecipients, recipient)
			continue
		}
		if user != nil {
			log.Printf("receiver: recipient %s is valid", recipient)
			validRecipients = append(validRecipients, recipient)
		} else {
			log.Printf("receiver: recipient %s returned nil user (invalid)", recipient)
			invalidRecipients = append(invalidRecipients, recipient)
		}
	}

	// Extract timestamp from the message (use Date header or current time)
	timestamp := parsedEmail.Date
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	// Delivery results
	var deliveryErrors []error
	var bounceErrors []error

	// Deliver to valid recipients
	for _, recipient := range validRecipients {
		if err := r.deliverToRecipient(ctx, key, rawMessage, recipient, timestamp); err != nil {
			log.Printf("receiver: failed to deliver to %s: %v", recipient, err)
			deliveryErrors = append(deliveryErrors, fmt.Errorf("delivery to %s failed: %w", recipient, err))
		} else {
			log.Printf("receiver: delivered to %s", recipient)
		}
	}

	// Send bounces for invalid recipients
	bounceSender := fmt.Sprintf("mailer-daemon@%s", r.cfg.Domain)
	for _, recipient := range invalidRecipients {
		if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
			log.Printf("receiver: failed to send bounce to %s: %v", recipient, err)
			bounceErrors = append(bounceErrors, fmt.Errorf("bounce to %s failed: %w", recipient, err))
		} else {
			log.Printf("receiver: sent bounce for %s", recipient)
		}
	}

	// Delete the staging object after all deliveries and bounces
	if err := r.deleteStagingObject(ctx, key); err != nil {
		return fmt.Errorf("failed to delete staging object after processing: %w", err)
	}

	// Return combined errors if any
	if len(deliveryErrors) > 0 || len(bounceErrors) > 0 {
		var allErrors []error
		allErrors = append(allErrors, deliveryErrors...)
		allErrors = append(allErrors, bounceErrors...)
		return fmt.Errorf("processing completed with errors: %w", errors.Join(allErrors...))
	}

	log.Printf("receiver: successfully processed message %s", key)
	return nil
}

// extractRecipients extracts all unique recipient addresses from To, Cc, and Bcc headers.
func (r *Receiver) extractRecipients(parsedEmail *email.UserEmail) []string {
	seen := make(map[string]bool)
	var recipients []string

	for _, addr := range parsedEmail.To {
		normalized := strings.ToLower(strings.TrimSpace(addr.Address))
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			recipients = append(recipients, normalized)
		}
	}
	for _, addr := range parsedEmail.Cc {
		normalized := strings.ToLower(strings.TrimSpace(addr.Address))
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			recipients = append(recipients, normalized)
		}
	}
	for _, addr := range parsedEmail.Bcc {
		normalized := strings.ToLower(strings.TrimSpace(addr.Address))
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			recipients = append(recipients, normalized)
		}
	}

	return recipients
}

// deliverToRecipient copies the raw message to the recipient's mailbox in S3.
func (r *Receiver) deliverToRecipient(ctx context.Context, originalKey string, rawMessage []byte, recipient string, timestamp time.Time) error {
	// Format timestamp as ISO 8601 UTC (e.g., 2026-11-17T10:30:45Z)
	ts := timestamp.UTC().Format(time.RFC3339)

	// SES message ID is the original key (staging object key)
	sesMessageID := originalKey

	// Construct the destination key: users/<recipient>/inbox/<timestamp>_<ses-message-id>.eml
	destKey := fmt.Sprintf("users/%s/inbox/%s_%s.eml", recipient, ts, sesMessageID)

	// Copy the object to the destination
	_, err := r.cfg.SESClient.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(destKey),
		Body:   strings.NewReader(string(rawMessage)),
	})
	if err != nil {
		return fmt.Errorf("failed to copy to %s: %w", destKey, err)
	}

	log.Printf("receiver: copied %s to %s", originalKey, destKey)
	return nil
}

// sendBounce sends a bounce message for an invalid recipient using SES SendBounce API.
func (r *Receiver) sendBounce(ctx context.Context, originalKey string, parsedEmail *email.UserEmail, invalidRecipient, bounceSender string) error {
	// Use SES SendBounce API for proper bounce handling
	// originalKey is the SES message ID from the staging object
	return r.cfg.Sender.SendBounce(ctx, originalKey, bounceSender, invalidRecipient, "UserUnknown", parsedEmail)
}

// joinAddresses joins a slice of addresses into a comma-separated string.
func (r *Receiver) joinAddresses(addrs []email.Address) string {
	var parts []string
	for _, a := range addrs {
		parts = append(parts, a.Address)
	}
	return strings.Join(parts, ", ")
}

// deleteStagingObject deletes the staging object from S3.
func (r *Receiver) deleteStagingObject(ctx context.Context, key string) error {
	_, err := r.cfg.SESClient.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete staging object %s: %w", key, err)
	}
	log.Printf("receiver: deleted staging object %s", key)
	return nil
}

// ProcessS3Event processes an S3 event notification (from Lambda).
// This is the main entry point for the s3-receiver Lambda.
func (r *Receiver) ProcessS3Event(ctx context.Context, bucket, key string) error {
	// Verify the bucket matches our configured bucket
	if bucket != r.cfg.Bucket {
		return fmt.Errorf("bucket mismatch: expected %s, got %s", r.cfg.Bucket, bucket)
	}

	// Process the message
	return r.ProcessMessage(ctx, key)
}