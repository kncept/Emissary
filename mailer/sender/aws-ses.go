package sender

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/kncept/Emissary/mailer/email"
)

var _ Sender = (*AwsSesSender)(nil)

// AwsSesSender sends mail through the Amazon SES v2 SendEmail API.
type AwsSesSender struct {
	client *ses.Client
	from   string
}

func NewAwsSesSender(ctx context.Context, region, from string) (*AwsSesSender, error) {
	options := make([]func(*awsconfig.LoadOptions) error, 0, 1)
	if region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &AwsSesSender{client: ses.NewFromConfig(cfg), from: from}, nil
}

func (s *AwsSesSender) Name() string {
	return "AwsSesSender"
}

func (s *AwsSesSender) From() string {
	return s.from
}

func (s *AwsSesSender) Send(ctx context.Context, message *email.Email) error {
	if s.from == "" {
		return errors.New("sender address is not configured (set EMISSARY_SES_FROM)")
	}

	body := &types.Body{}
	if message.Text != "" {
		body.Text = &types.Content{Data: aws.String(message.Text), Charset: aws.String("UTF-8")}
	}
	if message.HTML != "" {
		body.Html = &types.Content{Data: aws.String(message.HTML), Charset: aws.String("UTF-8")}
	}
	if body.Text == nil && body.Html == nil {
		body.Text = &types.Content{Data: aws.String(" "), Charset: aws.String("UTF-8")}
	}

	_, err := s.client.SendEmail(ctx, &ses.SendEmailInput{
		Source: aws.String(s.from),
		Destination: &types.Destination{
			ToAddresses:  addressStrings(message.To),
			CcAddresses:  addressStrings(message.Cc),
			BccAddresses: addressStrings(message.Bcc),
		},
		Message: &types.Message{
			Subject: &types.Content{Data: aws.String(message.Subject), Charset: aws.String("UTF-8")},
			Body:    body,
		},
	})
	if err != nil {
		return fmt.Errorf("send email with SES: %w", err)
	}
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
