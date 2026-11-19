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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/storage"
	"github.com/kncept/Emissary/mailer/usermanager"
)

// ErrNoValidRecipients is returned when a message has no valid recipients.
var ErrNoValidRecipients = errors.New("no valid recipients")

// S3Client is the subset of the S3 client used by the receiver for the SES
// staging area. Delivery no longer writes through this interface: it goes via
// the configured storage. Staging read and delete are still S3 operations and
// will be decoupled separately.
type S3Client interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

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

	// IdentityConfig decides how each recipient is handled. It is normally
	// built by LoadMailIdentityConfig. When nil it defaults to the base
	// fallback rules: deliver to existing users, bounce everyone else.
	IdentityConfig MailIdentityConfig

	// SESClient is the S3 client for staging-area operations (read and delete).
	SESClient S3Client

	// Storage is the configured mail store used to deliver messages. It owns
	// the generated email id and mailbox placement, so the receiver must not
	// construct destination keys itself.
	Storage storage.Storage

	// Metadata stores per-message JMAP state. The receiver creates a record
	// for each delivered message, keyed by the id returned from Storage.
	Metadata metadata.MetadataStore
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
	if cfg.Storage == nil {
		return nil, errors.New("storage is required")
	}
	if cfg.Domain == "" {
		return nil, errors.New("domain is required")
	}
	if cfg.IdentityConfig == nil {
		cfg.IdentityConfig = BaseMailIdentityConfig()
	}
	if err := cfg.IdentityConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid mail identity config: %w", err)
	}
	if cfg.SESClient == nil {
		return nil, errors.New("S3 client is required")
	}
	if cfg.Metadata == nil {
		cfg.Metadata = metadata.NewMemoryMetadata()
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

	// Resolve an action for each recipient by evaluating its configured
	// handler rules in order. The first rule that applies wins.
	var plans []recipientPlan
	for _, recipient := range recipients {
		rules, err := r.cfg.IdentityConfig.GetByRecipient(recipient)
		if err != nil {
			log.Printf("receiver: no routing rules for %s: %v (bouncing)", recipient, err)
			plans = append(plans, recipientPlan{recipient: recipient, action: Bounce})
			continue
		}
		plan, ok := r.planRecipient(ctx, recipient, rules)
		if !ok {
			// No rule handled the recipient. Bounce instead of silently
			// dropping the message, which is what the default fallback does.
			log.Printf("receiver: no handler rule matched %s; bouncing", recipient)
			plan = recipientPlan{recipient: recipient, action: Bounce}
		}
		plans = append(plans, plan)
	}

	// Delivery results
	var deliveryErrors []error
	var bounceErrors []error

	bounceSender := fmt.Sprintf("mailer-daemon@%s", r.cfg.Domain)
	for _, plan := range plans {
		switch plan.action {
		case DeliverToUser, DeliverToMailIdentity:
			if err := r.deliverToRecipient(ctx, plan.mailbox, parsedEmail); err != nil {
				log.Printf("receiver: failed to deliver to %s: %v", plan.mailbox, err)
				deliveryErrors = append(deliveryErrors, fmt.Errorf("delivery to %s failed: %w", plan.mailbox, err))
			}
		case Bounce:
			if err := r.sendBounce(ctx, key, parsedEmail, plan.recipient, bounceSender); err != nil {
				log.Printf("receiver: failed to send bounce to %s: %v", plan.recipient, err)
				bounceErrors = append(bounceErrors, fmt.Errorf("bounce to %s failed: %w", plan.recipient, err))
			} else {
				log.Printf("receiver: sent bounce for %s", plan.recipient)
			}
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

// recipientPlan is the terminal action selected for one recipient after
// evaluating its handler rules. mailbox is the delivery target for the
// DeliverToMailIdentity rule.
type recipientPlan struct {
	recipient string
	action    HandlerRule
	mailbox   string
}

// planRecipient evaluates the identity's rules in order and returns the first
// action that applies. DeliverToUser only applies when the user exists; a
// failed lookup falls through to the next rule instead of deciding the outcome.
func (r *Receiver) planRecipient(ctx context.Context, recipient string, rules HandlerRules) (recipientPlan, bool) {
	for _, rule := range rules.Rules {
		switch rule {
		case DeliverToUser:
			user, err := r.cfg.UserManager.Lookup(ctx, recipient)
			if err != nil || user == nil {
				log.Printf("receiver: recipient %s is not a user (rule %s)", recipient, rule)
				continue
			}
			return recipientPlan{recipient: recipient, action: DeliverToUser, mailbox: recipient}, true
		case DeliverToMailIdentity:
			identity := rules.deliveryIdentity(recipient)
			log.Printf("receiver: recipient %s routed to mail identity %s", recipient, identity)
			return recipientPlan{recipient: recipient, action: DeliverToMailIdentity, mailbox: identity}, true
		case Bounce:
			return recipientPlan{recipient: recipient, action: Bounce}, true
		default:
			// Validate should reject unknown rules, but never lose mail if a
			// config was built without going through NewReceiver.
			log.Printf("receiver: ignoring unknown handler rule %q for %s", rule, recipient)
		}
	}
	return recipientPlan{}, false
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

// deliverToRecipient stores the parsed message using the configured storage.
// Storage owns the generated id and mailbox placement; the receiver only uses
// the returned id to initialize the message's JMAP metadata.
func (r *Receiver) deliverToRecipient(ctx context.Context, recipient string, message *email.UserEmail) error {
	id, err := r.cfg.Storage.AddEmail(ctx, recipient, message)
	if err != nil {
		return fmt.Errorf("failed to add email for %s: %w", recipient, err)
	}

	// JMAP metadata is created alongside the stored message. The id must be
	// the one returned by Storage.AddEmail, not a key we construct here, so
	// metadata stays aligned with the stored message. NewEmailMetadata stamps
	// the server-owned receivedAt at delivery time; a new delivery has no
	// keywords and is unread and unflagged until a JMAP client changes it.
	if err := r.cfg.Metadata.Save(ctx, metadata.NewEmailMetadata(recipient, id)); err != nil {
		return fmt.Errorf("failed to save metadata for %s: %w", id, err)
	}

	log.Printf("receiver: delivered message %s to %s", id, recipient)
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
