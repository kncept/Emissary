package receiver

import (
	"context"

	"github.com/kncept/Emissary/mailer/email"
)

type InboundEmailEvent interface {

	// the raw MIME format email this refers to
	RawEmail(ctx context.Context) (*email.WireFormatEmail, error)

	// OnComplete should delete the email from any temporary storage locations
	OnComplete(ctx context.Context) error
}

type InboundRawEmailEvent interface {

	// RAW email bytes, will be parsed in the processor
	RawEmail(ctx context.Context) ([]byte, error)

	// OnComplete should delete the email from any temporary storage locations
	OnComplete(ctx context.Context) error
}
