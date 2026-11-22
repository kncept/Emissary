package sender

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/smithy-go"
	"github.com/kncept/Emissary/mailer/email"
)

// ErrRateLimited reports that a provider refused a send because the caller (or
// the service as a whole) exceeded its sending rate. JMAP maps this to a
// rateLimit SetError rather than a generic serverFail.
var ErrRateLimited = errors.New("sender rate limit exceeded")

// ErrMessageTooLarge reports that a provider refused a send because the wire
// message exceeded its size limit. JMAP maps this to tooLarge with the local
// maxSize advertised on the SetError.
var ErrMessageTooLarge = errors.New("message too large")

// ClassifySendError normalises provider errors into the two sentinels JMAP
// needs for RFC 8621 SetErrors. Unrecognised errors are returned unchanged so
// callers still report them as serverFail and keep the provider's detail.
func ClassifySendError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch code {
		case "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded",
			"Throttling", "RequestThrottledException", "LimitExceededException":
			return fmt.Errorf("%w: %s", ErrRateLimited, apiErr.ErrorMessage())
		}
		if strings.Contains(strings.ToLower(apiErr.ErrorMessage()), "too large") {
			return fmt.Errorf("%w: %s", ErrMessageTooLarge, apiErr.ErrorMessage())
		}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "too large") || strings.Contains(message, "message size exceeds") {
		return fmt.Errorf("%w: %s", ErrMessageTooLarge, err.Error())
	}
	return err
}

// headerRecipients returns the To, Cc and Bcc addresses in delivery order.
// Senders fall back to these only when no explicit JMAP envelope is supplied.
func headerRecipients(message *email.UserEmail) []email.Address {
	if message == nil {
		return nil
	}
	var recipients []email.Address
	for _, list := range [][]email.Address{message.To, message.Cc, message.Bcc} {
		recipients = append(recipients, list...)
	}
	return recipients
}

// WireSize returns the number of octets this sender would put on the wire for
// message. EmailSubmission/set uses it to enforce the same size limit that a
// provider sees before persisting a submission that cannot be sent.
func WireSize(message *email.UserEmail) int {
	if message == nil {
		return 0
	}
	from := message.From.Address
	return len(BuildWireMessage(message, from, helloNameFromAddress(from)))
}
