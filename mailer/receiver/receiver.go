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

	// The staged object is the raw RFC 5322 message accepted by SES. Parse it
	// with the wire-format type so the Received headers survive: UserEmail is
	// the simplified type used for delivered .eml files and drops them.
	wireEmail := email.ParseWireFormat(key, strings.NewReader(string(rawMessage)))
	parsedEmail := wireEmail.ToUserEmail()

	// Route on the SMTP envelope recipient (RCPT TO) recorded in the Received
	// header, not on the sender-controlled To/Cc/Bcc headers.
	recipients := email.EnvelopeRecipients(wireEmail)
	if len(recipients) == 0 {
		log.Printf("receiver: no envelope recipients found in message %s", key)
		// Still delete the staging object to avoid reprocessing
		if err := r.deleteStagingObject(ctx, key); err != nil {
			return fmt.Errorf("failed to delete staging object after no recipients: %w", err)
		}
		return nil
	}

	log.Printf("receiver: message %s has %d recipient(s): %s", key, len(recipients), strings.Join(recipients, ", "))

	// Resolve and apply the rules for each recipient in one pass. Every lookup
	// is keyed by the recipient's mail identity. An identity's rules are tried
	// in their configured order; processing stops at the first one that applies.
	var deliveryErrors []error
	var bounceErrors []error
	bounceSender := fmt.Sprintf("mailer-daemon@%s", r.cfg.Domain)

	for _, recipient := range recipients {
		identity := MailIdentityForRecipient(recipient)
		rules, err := r.cfg.IdentityConfig.GetByIdentity(identity)
		if err != nil {
			log.Printf("receiver: no routing rules for %s via mail identity %s: %v (bouncing)", recipient, identity, err)
			if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
				log.Printf("receiver: failed to send bounce to %s: %v", recipient, err)
				bounceErrors = append(bounceErrors, fmt.Errorf("bounce to %s failed: %w", recipient, err))
			}
			continue
		}

		applied := false
		for _, rule := range rules.Rules {
			fmt.Printf("Evaluating rule %v from mailbox %s for user %s\n", rule, rules.MailIdentity, recipient)
			switch rule {
			case DeliverToUser:
				user, err := r.cfg.UserManager.Lookup(ctx, recipient)
				if err != nil || user == nil {
					// This rule does not apply: keep trying the remaining rules
					// in order instead of deciding the recipient's fate here.
					log.Printf("receiver: recipient %s is not a user (rule %s)", recipient, rule)
					continue
				}
				if err := r.deliverToRecipient(ctx, recipient, parsedEmail); err != nil {
					log.Printf("receiver: failed to deliver to %s: %v", recipient, err)
					deliveryErrors = append(deliveryErrors, fmt.Errorf("delivery to %s failed: %w", recipient, err))
				}
				applied = true
			case DeliverToMailIdentity:
				mailbox := rules.deliveryIdentity(recipient)
				log.Printf("receiver: recipient %s routed to mail identity %s", recipient, mailbox)
				if err := r.deliverToRecipient(ctx, mailbox, parsedEmail); err != nil {
					log.Printf("receiver: failed to deliver to %s: %v", mailbox, err)
					deliveryErrors = append(deliveryErrors, fmt.Errorf("delivery to %s failed: %w", mailbox, err))
				}
				applied = true
			case Bounce:
				if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
					log.Printf("receiver: failed to send bounce to %s: %v", recipient, err)
					bounceErrors = append(bounceErrors, fmt.Errorf("bounce to %s failed: %w", recipient, err))
				} else {
					log.Printf("receiver: sent bounce for %s", recipient)
				}
				applied = true
			case Drop:
				// Explicitly configured Drop is the only path that discards a
				// recipient without a bounce. It is logged so operators can see
				// where mail was intentionally dropped.
				log.Printf("receiver: dropped message %s for %s", key, recipient)
				applied = true
			default:
				// Validate should reject unknown rules, but never lose mail if a
				// config was built without going through NewReceiver.
				log.Printf("receiver: ignoring unknown handler rule %q for %s", rule, recipient)
			}
			if applied {
				// First applicable rule wins; do not try the remaining rules.
				break
			}
		}
		if !applied {
			// No rule applied. Bounce rather than discard: dropping is only
			// ever the result of an explicit Drop rule.
			log.Printf("receiver: no handler rule matched %s; bouncing", recipient)
			if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
				log.Printf("receiver: failed to send bounce to %s: %v", recipient, err)
				bounceErrors = append(bounceErrors, fmt.Errorf("bounce to %s failed: %w", recipient, err))
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

	log.Printf("receiver: delivered message id %s  (smtp id %s) to %s", id, message.MessageID, recipient)
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
func (r *Receiver) ProcessS3Event(ctx context.Context, key string) error {
	return r.ProcessMessage(ctx, key)
}
