package sender

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*AwsSesSender)(nil)

// sesAPI is the narrow SES seam used by AwsSesSender. It lets tests assert the
// envelope Source without calling AWS.
type sesAPI interface {
	SendRawEmail(ctx context.Context, params *ses.SendRawEmailInput, optFns ...func(*ses.Options)) (*ses.SendRawEmailOutput, error)
	SendBounce(ctx context.Context, params *ses.SendBounceInput, optFns ...func(*ses.Options)) (*ses.SendBounceOutput, error)
}

// AwsSesSender sends mail through the Amazon SES SendRawEmail API. The
// message is serialized with the shared wire builder so the headers match the
// direct-MTA sender exactly (including In-Reply-To and References, which the
// structured SendEmail API cannot express); the envelope recipients are passed
// separately as Destinations.
type AwsSesSender struct {
	client sesAPI
}

func NewAwsSesSender(ctx context.Context) (*AwsSesSender, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &AwsSesSender{client: ses.NewFromConfig(cfg)}, nil
}

// isAutomaticReply reports whether the message is an RFC 3834 automatic
// response. Any Auto-Submitted value other than "no" marks an automatic
// message; vacation replies use "auto-replied".
func isAutomaticReply(message *email.UserEmail) bool {
	value := strings.ToLower(strings.TrimSpace(message.AutoSubmitted))
	return value != "" && value != "no"
}

// sesEnvelopeSource returns the value for SES's Source parameter, which sets
// the SMTP MAIL FROM (reverse path). Automatic replies must use the null
// reverse path so a bounce or a reply to the auto-reply cannot create a mail
// loop. SES interprets an empty Source as MAIL FROM:<>; the visible From
// header is unaffected because it comes from the raw message.
func sesEnvelopeSource(message *email.UserEmail) string {
	if isAutomaticReply(message) {
		return ""
	}
	return message.From.Address
}

func (s *AwsSesSender) Name() string {
	return "AwsSesSender"
}

func (s *AwsSesSender) Send(ctx context.Context, message *email.UserEmail) error {
	// The From header still needs a real address; only the envelope reverse
	// path is nulled for automatic replies.
	from := message.From.Address
	if from == "" {
		return errors.New("message has no From address")
	}
	source := sesEnvelopeSource(message)

	wire := BuildWireMessage(message, from, helloNameFromAddress(from))
	if len(wire) == 0 {
		return errors.New("failed to build wire message")
	}

	// The envelope carries every recipient, including Bcc (which never appears
	// in the raw message headers). A JMAP-supplied envelope is authoritative,
	// so SMTP-only rcptTo addresses are delivered too.
	destinations := email.BareAddresses(headerRecipients(message))
	if message.SubmissionEnvelope != nil && len(message.SubmissionEnvelope.RcptTo) > 0 {
		destinations = destinations[:0]
		for _, recipient := range message.SubmissionEnvelope.RcptTo {
			if address := bareAddress(recipient.Email); address != "" {
				destinations = append(destinations, address)
			}
			if len(recipient.Parameters) > 0 {
				slog.Warn("SES: ESMTP envelope parameters are not supported; address only",
					"recipient", recipient.Email)
			}
		}
	}
	if message.SubmissionEnvelope != nil && strings.TrimSpace(message.SubmissionEnvelope.MailFrom.Email) != "" && !isAutomaticReply(message) {
		if address := bareAddress(message.SubmissionEnvelope.MailFrom.Email); address != "" {
			source = address
		}
	}

	slog.Debug("SES: sending raw email", "from", from, "source", source, "recipients", len(destinations), "subject", message.Subject)
	_, err := s.client.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(source),
		Destinations: destinations,
		RawMessage: &types.RawMessage{
			Data: wire,
		},
	})
	if err != nil {
		return ClassifySendError(fmt.Errorf("send raw email with SES: %w", err))
	}
	slog.Debug("SES: raw email sent", "from", from, "source", source, "recipients", len(destinations))
	return nil
}

func (s *AwsSesSender) SendBounce(ctx context.Context, originalMessageID, bounceSender, recipient, bounceType string, originalMessage *email.UserEmail) error {
	// Convert bounceType to SES bounce type
	var sesBounceType types.BounceType
	switch bounceType {
	case "UserUnknown", "user_unknown":
		sesBounceType = types.BounceTypeDoesNotExist
	case "ContentRejected", "content_rejected":
		sesBounceType = types.BounceTypeContentRejected
	case "AttachmentRejected", "attachment_rejected":
		sesBounceType = types.BounceTypeMessageTooLarge
	default:
		sesBounceType = types.BounceTypeDoesNotExist
	}

	slog.Debug("SES: sending bounce", "original_message_id", originalMessageID, "recipient", recipient, "bounce_type", bounceType)
	_, err := s.client.SendBounce(ctx, &ses.SendBounceInput{
		OriginalMessageId: aws.String(originalMessageID),
		BounceSender:      aws.String(bounceSender),
		BouncedRecipientInfoList: []types.BouncedRecipientInfo{
			{
				Recipient:  aws.String(recipient),
				BounceType: sesBounceType,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("send bounce with SES: %w", err)
	}
	slog.Debug("SES: bounce sent", "original_message_id", originalMessageID, "recipient", recipient)
	return nil
}
