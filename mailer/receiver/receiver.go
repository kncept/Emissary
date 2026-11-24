// Package receiver implements the inbound mail processing logic for Emissary.
// It processes raw messages from an event source, validates recipients,
// delivers messages to user mailboxes, and handles bounces for invalid recipients.
package receiver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
	"github.com/kncept/Emissary/mailer/email"
)

// ErrNoValidRecipients is returned when a message has no valid recipients.
var ErrNoValidRecipients = errors.New("no valid recipients")

// ReceiverConfig holds the configuration for the receiver.
type ReceiverConfig struct {
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

	// Storage is the configured mail store used to deliver messages. It owns
	// the generated email id and mailbox placement, so the receiver must not
	// construct destination keys itself.
	Storage storage.Storage

	// Metadata stores per-message JMAP state. The receiver creates a record
	// for each delivered message, keyed by the id returned from Storage.
	Metadata metadata.MetadataStore

	// Dedup records which recipients have been handled for a staging key, so
	// a Lambda retry after a partial failure cannot deliver or bounce them
	// again. Defaults to a process-local store.
	Dedup DedupStore

	// VacationStore holds the VacationResponse singleton and RFC 3834 reply
	// state. It defaults to the user manager's own user.json-backed store;
	// managers without one get a process-local store so loop prevention still
	// works within the process.
	VacationStore usermanager.VacationStore

	// EmailDelivery advances the RFC 8620 EmailDelivery type state after a
	// message is stored. It is optional so existing callers and tests keep
	// their behaviour; production wiring supplies a durable tracker.
	EmailDelivery DeliveryNotifier
}

// DeliveryNotifier records a successful inbound delivery for push clients.
type DeliveryNotifier interface {
	Delivered(ctx context.Context, accountID, messageID string) error
}

// Receiver processes inbound messages.
type Receiver struct {
	cfg ReceiverConfig
}

// NewReceiver creates a new Receiver with the given configuration.
func NewReceiver(cfg ReceiverConfig) (*Receiver, error) {
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
	if cfg.Metadata == nil {
		cfg.Metadata = metadata.NewMemoryMetadata()
	}
	if cfg.Dedup == nil {
		cfg.Dedup = NewMemoryDedup()
	}
	if cfg.VacationStore == nil {
		cfg.VacationStore = usermanager.VacationStoreFor(cfg.UserManager)
	}
	return &Receiver{cfg: cfg}, nil
}

// ParsedMessage holds the parsed email and its envelope recipients.
type ParsedMessage struct {
	WireEmail   *email.WireFormatEmail
	ParsedEmail *email.UserEmail
	RawMessage  []byte
	Recipients  []string
}

// ParseRawMessage parses raw email bytes into a ParsedMessage.
// It extracts envelope recipients from the Received headers and converts
// to UserEmail for delivery logic.
func ParseRawMessage(key string, rawMessage []byte) *ParsedMessage {
	// ParseWireFormat ignores key; it is kept in the signature for callers that
	// already have a storage key, but message identity is never parsed from it.
	wireEmail := email.ParseWireFormat(key, bytes.NewReader(rawMessage))
	parsedEmail := wireEmail.ToUserEmail()
	recipients := email.EnvelopeRecipients(wireEmail)
	return &ParsedMessage{
		WireEmail:   wireEmail,
		ParsedEmail: parsedEmail,
		RawMessage:  rawMessage,
		Recipients:  recipients,
	}
}

// processMessage is the common processing logic shared by all entry points.
// It handles recipient resolution, dedup, delivery, bounces.
// The caller is responsible for parsing and providing the key, recipients,
// parsed email, and raw message bytes.
// sesMessageID is the SES message ID for bounce correlation (distinct from the
// dedup key which is a content hash). Returns the list of dedup IDs that were
// recorded, so the caller can clear them after any storage-specific cleanup.
func (r *Receiver) processMessage(ctx context.Context, key, sesMessageID string, recipients []string, parsedEmail *email.UserEmail, rawMessage []byte, wireEmail *email.WireFormatEmail) ([]string, error) {
	if len(recipients) == 0 {
		slog.Warn("Receiver: no envelope recipients found", "key", key)
		return nil, nil
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
			return dedupIDs, fmt.Errorf("failed to check dedup state for %s: %w", recipient, err)
		}
		if seen {
			slog.Debug("Receiver: skipping already handled recipient", "key", key, "recipient", recipient)
			continue
		}

		if err := r.handleRecipient(ctx, key, sesMessageID, recipient, parsedEmail, rawMessage, bounceSender, wireEmail); err != nil {
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
		return dedupIDs, fmt.Errorf("processing completed with errors: %w", err)
	}

	slog.Info("Receiver: successfully processed message", "key", key, "recipients", len(recipients))
	return dedupIDs, nil
}

// ProcessEvent processes an inbound email event that provides raw email bytes.
// This is the new preferred entry point for processing messages, replacing
// ProcessMessage which takes a staging key.
func (r *Receiver) ProcessEvent(ctx context.Context, event InboundRawEmailEvent) error {
	// Get the raw email bytes from the event
	rawMessage, err := event.RawEmail(ctx)
	if err != nil {
		return fmt.Errorf("failed to get raw email from event: %w", err)
	}

	// Generate a content-hash key for dedup tracking (fallback for events without SES message ID)
	dedupKey := generateEventKey(rawMessage)

	// Get the SES message ID from the event for bounce correlation.
	// For S3-staged events, this is the S3 object key. If empty, we fall back
	// to the dedup key for bounce as well (legacy behavior).
	sesMessageID := event.SESMessageID()
	if sesMessageID == "" {
		sesMessageID = dedupKey
	}

	slog.Info("Receiver: processing event", "dedup_key", dedupKey, "ses_message_id", sesMessageID)

	// Parse the raw message
	parsed := ParseRawMessage(dedupKey, rawMessage)

	// Delegate to the common processing function, passing the SES message ID for bounces
	dedupIDs, err := r.processMessage(ctx, dedupKey, sesMessageID, parsed.Recipients, parsed.ParsedEmail, parsed.RawMessage, parsed.WireEmail)
	if err != nil {
		return err
	}

	// Mark the event as complete (delete from staging)
	if err := event.OnComplete(ctx); err != nil {
		return fmt.Errorf("failed to complete event: %w", err)
	}

	// Clear dedup markers on success
	if clearer, ok := r.cfg.Dedup.(DedupClearer); ok {
		if err := clearer.Clear(ctx, dedupIDs...); err != nil {
			slog.Warn("Receiver: failed to clear dedup markers", "key", dedupKey, "err", err)
		}
	}

	return nil
}

// ProcessParsedEvent processes an inbound email event that provides an already
// parsed WireFormatEmail. This avoids re-parsing when the caller has already
// parsed the message (e.g., from an SQS event with the parsed message).
func (r *Receiver) ProcessParsedEvent(ctx context.Context, event InboundEmailEvent) error {
	// Get the already-parsed wire email from the event
	wireEmail, err := event.RawEmail(ctx)
	if err != nil {
		return fmt.Errorf("failed to get parsed email from event: %w", err)
	}

	// Convert to UserEmail for delivery logic
	parsedEmail := wireEmail.ToUserEmail()

	// Extract envelope recipients from the Received headers
	recipients := email.EnvelopeRecipients(wireEmail)

	// We need a key for dedup and bounce purposes. Generate one from the Message-ID
	// or use a hash of the wire email's serialized form.
	key := generateEventKeyFromWireEmail(wireEmail)

	// For parsed events, use the same key as SES message ID (derived from Message-ID if available)
	sesMessageID := key

	slog.Info("Receiver: processing parsed event", "key", key, "ses_message_id", sesMessageID)

	// Serialize the wire email to raw bytes for storage (preserves exact MIME structure)
	rawMessage, err := wireEmail.ToWireFormat()
	if err != nil {
		return fmt.Errorf("failed to serialize wire email: %w", err)
	}

	// Delegate to the common processing function
	dedupIDs, err := r.processMessage(ctx, key, sesMessageID, recipients, parsedEmail, rawMessage, wireEmail)
	if err != nil {
		return err
	}

	// Mark the event as complete (delete from staging)
	if err := event.OnComplete(ctx); err != nil {
		return fmt.Errorf("failed to complete event: %w", err)
	}

	// Clear dedup markers on success
	if clearer, ok := r.cfg.Dedup.(DedupClearer); ok {
		if err := clearer.Clear(ctx, dedupIDs...); err != nil {
			slog.Warn("Receiver: failed to clear dedup markers", "key", key, "err", err)
		}
	}

	return nil
}

// generateEventKeyFromWireEmail generates a deterministic key from a parsed
// WireFormatEmail for use in dedup and bounce tracking.
func generateEventKeyFromWireEmail(wfe *email.WireFormatEmail) string {
	// Use Message-ID if available, otherwise hash the serialized form
	if wfe.MessageID != "" {
		// Sanitize Message-ID for use as a key
		id := strings.Trim(wfe.MessageID, "<>")
		id = strings.ReplaceAll(id, "@", "-at-")
		id = strings.ReplaceAll(id, ".", "-dot-")
		return "event-" + id
	}
	// Fallback: serialize and hash
	data, _ := wfe.ToWireFormat()
	return generateEventKey(data)
}

// generateEventKey generates a deterministic key from raw message bytes
// for use in dedup and bounce tracking when no SES message ID is available.
func generateEventKey(rawMessage []byte) string {
	// Use a hash of the raw message as the key
	// This is a simple implementation; in practice, SES provides a message ID
	return fmt.Sprintf("event-%x", sha256Sum(rawMessage)[:16])
}

// sha256Sum returns the SHA256 hash of the input data.
func sha256Sum(data []byte) []byte {
	hash := sha256.New()
	hash.Write(data)
	return hash.Sum(nil)
}

// handleRecipient applies the first applicable routing rule to one envelope
// recipient. rawMessage is the original wire bytes that accompany parsedEmail;
// it is passed to delivery so the stored message is byte-exact. A returned
// error means the recipient still needs processing on a retry; callers record
// a dedup marker only when this returns nil.
// sesMessageID is the SES message ID for bounce correlation (distinct from the
// dedup key which is a content hash).
func (r *Receiver) handleRecipient(ctx context.Context, key, sesMessageID, recipient string, parsedEmail *email.UserEmail, rawMessage []byte, bounceSender string, wireEmail *email.WireFormatEmail) error {
	identity := MailIdentityForRecipient(recipient)
	rules, err := r.cfg.IdentityConfig.GetByIdentity(identity)
	if err != nil {
		slog.Warn("Receiver: no routing rules; bouncing", "recipient", recipient, "identity", identity, "err", err)
		if err := r.sendBounce(ctx, sesMessageID, parsedEmail, recipient, bounceSender); err != nil {
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
			var err error
			applied, err = r.deliverToKnownUser(ctx, recipient, parsedEmail, rawMessage, wireEmail)
			if err != nil {
				return err
			}
		case DeliverToMailIdentity:
			mailbox := rules.deliveryIdentity(recipient)
			slog.Debug("Receiver: routed to mail identity", "recipient", recipient, "mailbox", mailbox)
			if err := r.deliverToRecipient(ctx, mailbox, parsedEmail, rawMessage); err != nil {
				slog.Error("Receiver: failed to deliver", "recipient", mailbox, "err", err)
				return fmt.Errorf("delivery to %s failed: %w", mailbox, err)
			}
			// The mailbox owner's vacation settings apply to mail delivered
			// through a mail identity just as they do to a user delivery.
			r.maybeSendVacationReply(ctx, mailbox, parsedEmail, wireEmail)
			applied = true
		case Bounce:
			if err := r.sendBounce(ctx, sesMessageID, parsedEmail, recipient, bounceSender); err != nil {
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
			// DeliverTo:<address> carries its saved target in the rule itself.
			// Like DeliverToUser it applies only when that user exists.
			if target, ok := rule.deliverToTarget(); ok {
				var err error
				applied, err = r.deliverToKnownUser(ctx, target, parsedEmail, rawMessage, wireEmail)
				if err != nil {
					return err
				}
				break
			}
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
		if err := r.sendBounce(ctx, sesMessageID, parsedEmail, recipient, bounceSender); err != nil {
			slog.Error("Receiver: failed to send bounce", "recipient", recipient, "err", err)
			return fmt.Errorf("bounce to %s failed: %w", recipient, err)
		}
	}
	return nil
}

// deliverToKnownUser applies a rule addressed to a user account: DeliverToUser
// names the envelope recipient, while DeliverTo:<address> names a saved target.
// A missing account means the rule does not apply, so the caller keeps walking
// the ordered rule list instead of bouncing immediately.
func (r *Receiver) deliverToKnownUser(ctx context.Context, user string, parsedEmail *email.UserEmail, rawMessage []byte, wireEmail *email.WireFormatEmail) (bool, error) {
	account, err := r.cfg.UserManager.Lookup(ctx, user)
	if err != nil || account == nil {
		// This rule does not apply: keep trying the remaining rules in order.
		slog.Debug("Receiver: recipient is not a user", "user", user, "err", err)
		return false, nil
	}
	if err := r.deliverToRecipient(ctx, user, parsedEmail, rawMessage); err != nil {
		slog.Error("Receiver: failed to deliver", "user", user, "err", err)
		return false, fmt.Errorf("delivery to %s failed: %w", user, err)
	}
	// RFC 8621 section 8: a vacation response is sent when a message is
	// delivered to the user's mail store, subject to RFC 3834 rules.
	r.maybeSendVacationReply(ctx, user, parsedEmail, wireEmail)
	return true, nil
}

// deliverToRecipient stores the message using the configured storage. Storage
// owns the generated id and mailbox placement; the receiver only uses the
// returned id to initialize the message's JMAP metadata. The original wire
// bytes are passed through so the store keeps headers, MIME parts and
// attachments intact instead of re-rendering the parsed copy.
func (r *Receiver) deliverToRecipient(ctx context.Context, recipient string, message *email.UserEmail, raw []byte) error {
	id, err := r.cfg.Storage.AddEmail(ctx, recipient, "inbox", message, raw)
	if err != nil {
		return fmt.Errorf("failed to add email for %s: %w", recipient, err)
	}

	// JMAP metadata is created alongside the stored message. The id must be
	// the one returned by Storage.AddEmail, not a key we construct here, so
	// metadata stays aligned with the stored message. NewEmailMetadataForMessage
	// stamps the server-owned receivedAt and records the inbox and subject; a
	// new delivery has no keywords and is unread and unflagged until a JMAP
	// client changes it.
	if err := r.cfg.Metadata.Save(ctx, metadata.NewEmailMetadataForMessage(recipient, id, "inbox", message.Subject)); err != nil {
		return fmt.Errorf("failed to save metadata for %s: %w", id, err)
	}

	// Make the raw message downloadable through Email.blobId. Blob registration
	// is best effort: delivery has already succeeded and a blob failure must not
	// trigger a retry that would deliver the same message twice.
	blobRaw := raw
	if len(blobRaw) == 0 {
		blobRaw = email.RawFromUserEmail(message)
	}
	if _, err := storage.RegisterEmailBlobs(ctx, r.cfg.Storage, recipient, blobRaw); err != nil {
		slog.Warn("Receiver: could not register email blob", "id", id, "err", err)
	}

	// Push state is best effort: the message is already durable, so a failure
	// here must not trigger a Lambda retry that would deliver it twice. Clients
	// still discover the new mail through the normal Email state token.
	if r.cfg.EmailDelivery != nil {
		if err := r.cfg.EmailDelivery.Delivered(ctx, recipient, id); err != nil {
			slog.Warn("Receiver: could not record EmailDelivery state", "id", id, "err", err)
		}
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
