// Package receiver implements the inbound mail processing logic for Emissary.
// It reads raw messages from an S3 staging area, validates recipients,
// delivers messages to user mailboxes, and handles bounces for invalid recipients.
package receiver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	// Dedup records which recipients have been handled for a staging key, so
	// a Lambda retry after a partial failure cannot deliver or bounce them
	// again. Nil defaults to S3-backed markers when SESClient supports them,
	// otherwise to a process-local store.
	Dedup DedupStore
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
	if cfg.Dedup == nil {
		// A caller with a real S3 client gets durable markers automatically;
		// simpler clients (and tests) fall back to process-local state.
		if client, ok := cfg.SESClient.(DedupS3Client); ok {
			cfg.Dedup = NewS3Dedup(client, cfg.Bucket)
		} else {
			cfg.Dedup = NewMemoryDedup()
		}
	}
	return &Receiver{cfg: cfg}, nil
}

// ProcessMessage processes a single message from the S3 staging area.
// The key is the SES message ID (object key at bucket root).
// Returns an error if the message cannot be processed.
func (r *Receiver) ProcessMessage(ctx context.Context, key string) error {
	slog.Info("Receiver: processing message", "key", key)

	// Get the raw message from S3
	obj, err := r.cfg.SESClient.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// Check if object doesn't exist (already processed or deleted)
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			slog.Debug("Receiver: staging object not found (already processed?)", "key", key)
			return nil
		}
		return fmt.Errorf("failed to get object %s: %w", key, err)
	}
	defer obj.Body.Close()

	// Read the raw message with the configured size cap. Leave an oversized
	// staging object in place so it can be inspected and so Lambda retries do
	// not silently create an empty delivery.
	rawMessage, err := email.ReadLimited(obj.Body)
	if err != nil {
		return fmt.Errorf("failed to read object %s: %w", key, err)
	}

	// The staged object is the raw RFC 5322 message accepted by SES. Parse it
	// with the wire-format type so the Received headers survive: UserEmail is
	// the simplified type used for delivered .eml files and drops them. Keep
	// the original bytes alongside the parsed copy so delivery can store them
	// verbatim; rendering from UserEmail would lose MIME parts and attachments.
	wireEmail := email.ParseWireFormat(key, bytes.NewReader(rawMessage))
	parsedEmail := wireEmail.ToUserEmail()

	// Route on the SMTP envelope recipient (RCPT TO) recorded in the Received
	// header, not on the sender-controlled To/Cc/Bcc headers.
	recipients := email.EnvelopeRecipients(wireEmail)
	if len(recipients) == 0 {
		slog.Warn("Receiver: no envelope recipients found", "key", key)
		// Still delete the staging object to avoid reprocessing
		if err := r.deleteStagingObject(ctx, key); err != nil {
			return fmt.Errorf("failed to delete staging object after no recipients: %w", err)
		}
		return nil
	}

	slog.Debug("Receiver: message recipients", "key", key, "recipients", recipients)

	// Resolve and apply the rules for each recipient in one pass. Every lookup
	// is keyed by the recipient's mail identity. Dedup markers are per staging
	// key and recipient, so a retry only repeats the work that failed.
	var processingErrors []error
	dedupIDs := make([]string, 0, len(recipients))
	bounceSender := fmt.Sprintf("mailer-daemon@%s", r.cfg.Domain)

	for _, recipient := range recipients {
		id := DedupID(key, recipient)
		dedupIDs = append(dedupIDs, id)

		seen, err := r.cfg.Dedup.Seen(ctx, id)
		if err != nil {
			// Fail closed: without dedup state a retry might deliver twice.
			return fmt.Errorf("failed to check dedup state for %s: %w", recipient, err)
		}
		if seen {
			slog.Debug("Receiver: skipping already handled recipient", "key", key, "recipient", recipient)
			continue
		}

		if err := r.handleRecipient(ctx, key, recipient, parsedEmail, rawMessage, bounceSender); err != nil {
			slog.Error("Receiver: failed to handle recipient", "recipient", recipient, "err", err)
			processingErrors = append(processingErrors, err)
			continue
		}
		if err := r.cfg.Dedup.Record(ctx, id); err != nil {
			processingErrors = append(processingErrors, fmt.Errorf("failed to record dedup state for %s: %w", recipient, err))
		}
	}

	if err := errors.Join(processingErrors...); err != nil {
		// Keep the staging object so the Lambda retry can finish the failed
		// recipients. Deleting it here would lose messages permanently.
		return fmt.Errorf("processing completed with errors: %w", err)
	}

	// Only delete staging once every recipient has been handled successfully.
	// If this delete fails, the markers above let a retry delete without
	// redelivering the message.
	if err := r.deleteStagingObject(ctx, key); err != nil {
		return fmt.Errorf("failed to delete staging object after processing: %w", err)
	}

	// Markers are no longer needed once staging is gone. A cleanup failure is
	// harmless: stale markers cannot collide with a different SES message id,
	// and failing here would trigger a pointless retry of the delete.
	if clearer, ok := r.cfg.Dedup.(DedupClearer); ok {
		if err := clearer.Clear(ctx, dedupIDs...); err != nil {
			slog.Warn("Receiver: failed to clear dedup markers", "key", key, "err", err)
		}
	}

	slog.Info("Receiver: successfully processed message", "key", key, "recipients", len(recipients))
	return nil
}

// handleRecipient applies the first applicable routing rule to one envelope
// recipient. rawMessage is the original wire bytes that accompany parsedEmail;
// it is passed to delivery so the stored message is byte-exact. A returned
// error means the recipient still needs processing on a retry; callers record
// a dedup marker only when this returns nil.
func (r *Receiver) handleRecipient(ctx context.Context, key, recipient string, parsedEmail *email.UserEmail, rawMessage []byte, bounceSender string) error {
	identity := MailIdentityForRecipient(recipient)
	rules, err := r.cfg.IdentityConfig.GetByIdentity(identity)
	if err != nil {
		slog.Warn("Receiver: no routing rules; bouncing", "recipient", recipient, "identity", identity, "err", err)
		if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
			slog.Error("Receiver: failed to send bounce", "recipient", recipient, "err", err)
			return fmt.Errorf("bounce to %s failed: %w", recipient, err)
		}
		return nil
	}

	applied := false
	for _, rule := range rules.Rules {
		slog.Debug("Receiver: evaluating rule", "rule", rule, "mailbox", rules.MailIdentity, "recipient", recipient)
		switch rule {
		case DeliverToUser:
			user, err := r.cfg.UserManager.Lookup(ctx, recipient)
			if err != nil || user == nil {
				// This rule does not apply: keep trying the remaining rules
				// in order instead of deciding the recipient's fate here.
				slog.Debug("Receiver: recipient is not a user", "recipient", recipient, "rule", rule)
				continue
			}
			if err := r.deliverToRecipient(ctx, recipient, parsedEmail, rawMessage); err != nil {
				slog.Error("Receiver: failed to deliver", "recipient", recipient, "err", err)
				return fmt.Errorf("delivery to %s failed: %w", recipient, err)
			}
			applied = true
		case DeliverToMailIdentity:
			mailbox := rules.deliveryIdentity(recipient)
			slog.Debug("Receiver: routed to mail identity", "recipient", recipient, "mailbox", mailbox)
			if err := r.deliverToRecipient(ctx, mailbox, parsedEmail, rawMessage); err != nil {
				slog.Error("Receiver: failed to deliver", "recipient", mailbox, "err", err)
				return fmt.Errorf("delivery to %s failed: %w", mailbox, err)
			}
			applied = true
		case Bounce:
			if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
				slog.Error("Receiver: failed to send bounce", "recipient", recipient, "err", err)
				return fmt.Errorf("bounce to %s failed: %w", recipient, err)
			}
			slog.Info("Receiver: sent bounce", "recipient", recipient)
			applied = true
		case Drop:
			// Explicitly configured Drop is the only path that discards a
			// recipient without a bounce. It is logged so operators can see
			// where mail was intentionally dropped.
			slog.Warn("Receiver: dropped message for recipient", "key", key, "recipient", recipient)
			applied = true
		default:
			// Validate should reject unknown rules, but never lose mail if a
			// config was built without going through NewReceiver.
			slog.Warn("Receiver: ignoring unknown handler rule", "rule", rule, "recipient", recipient)
		}
		if applied {
			// First applicable rule wins; do not try the remaining rules.
			break
		}
	}
	if !applied {
		// No rule applied. Bounce rather than discard: dropping is only
		// ever the result of an explicit Drop rule.
		slog.Warn("Receiver: no handler rule matched; bouncing", "recipient", recipient)
		if err := r.sendBounce(ctx, key, parsedEmail, recipient, bounceSender); err != nil {
			slog.Error("Receiver: failed to send bounce", "recipient", recipient, "err", err)
			return fmt.Errorf("bounce to %s failed: %w", recipient, err)
		}
	}
	return nil
}

// deliverToRecipient stores the message using the configured storage. Storage
// owns the generated id and mailbox placement; the receiver only uses the
// returned id to initialize the message's JMAP metadata. The original wire
// bytes are passed through so the store keeps headers, MIME parts and
// attachments intact instead of re-rendering the parsed copy.
func (r *Receiver) deliverToRecipient(ctx context.Context, recipient string, message *email.UserEmail, raw []byte) error {
	id, err := r.cfg.Storage.AddEmail(ctx, recipient, message, raw)
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

	slog.Debug("Receiver: delivered message", "id", id, "smtp_id", message.MessageID, "recipient", recipient)
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
	slog.Debug("Receiver: deleted staging object", "key", key)
	return nil
}

// ProcessS3Event processes an S3 event notification (from Lambda).
// This is the main entry point for the s3-receiver Lambda.
func (r *Receiver) ProcessS3Event(ctx context.Context, key string) error {
	return r.ProcessMessage(ctx, key)
}
