package sender

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*AwsSesSender)(nil)

// AwsSesSender sends mail through the Amazon SES v2 SendRawEmail API. The
// message is serialized with the shared wire builder so the headers match the
// direct-MTA sender exactly (including In-Reply-To and References, which the
// structured SendEmail API cannot express); the envelope recipients are passed
// separately as Destinations.
type AwsSesSender struct {
	client *ses.Client
}

func NewAwsSesSender(ctx context.Context) (*AwsSesSender, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &AwsSesSender{client: ses.NewFromConfig(cfg)}, nil
}

func (s *AwsSesSender) Name() string {
	return "AwsSesSender"
}

func (s *AwsSesSender) Send(ctx context.Context, message *email.UserEmail) error {
	source := message.From.Address
	if source == "" {
		return errors.New("message has no From address")
	}

	wire := BuildWireMessage(message, source, helloNameFromAddress(source))
	if len(wire) == 0 {
		return errors.New("failed to build wire message")
	}

	// The envelope carries every recipient, including Bcc (which never appears
	// in the raw message headers).
	var envelope []email.Address
	for _, list := range [][]email.Address{message.To, message.Cc, message.Bcc} {
		envelope = append(envelope, list...)
	}

	slog.Debug("SES: sending raw email", "from", source, "recipients", len(envelope), "subject", message.Subject)
	_, err := s.client.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(source),
		Destinations: addressStrings(envelope),
		RawMessage: &types.RawMessage{
			Data: wire,
		},
	})
	if err != nil {
		return fmt.Errorf("send raw email with SES: %w", err)
	}
	slog.Debug("SES: raw email sent", "from", source, "recipients", len(envelope))
	return nil
}

func addressStrings(addresses []email.Address) []string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.Address != "" {
			values = append(values, address.Address)
		}
	}
	return values
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
