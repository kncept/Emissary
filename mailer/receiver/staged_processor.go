package receiver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kncept/Emissary/mailer/components/metadata"
	"github.com/kncept/Emissary/mailer/components/sender"
	"github.com/kncept/Emissary/mailer/components/storage"
	"github.com/kncept/Emissary/mailer/components/usermanager"
)

// StagedS3API is the S3 surface needed to read an object, delete it on success
// and write/read dedup markers.
type StagedS3API interface {
	S3API
	DedupS3Client
}

// RawEventProcessor is the narrow seam used by StagedMessageProcessor.
// *Receiver satisfies it; tests can substitute a recorder.
type RawEventProcessor interface {
	ProcessEvent(ctx context.Context, event InboundRawEmailEvent) error
}

// StagedMessageProcessor processes one raw message held in staging storage.
type StagedMessageProcessor interface {
	ProcessRawKey(ctx context.Context, bucket, key string) error
}

// StagedMessageProcessorConfig carries the durable services used to process a
// staged message. IdentityConfig is consulted on every call so rules saved by
// the support API take effect without rebuilding the handler.
type StagedMessageProcessorConfig struct {
	Client  StagedS3API
	Bucket  string
	Domain  string
	Storage storage.Storage

	Metadata    metadata.MetadataStore
	UserManager usermanager.UserManager
	Sender      sender.Sender

	IdentityConfig MailIdentityConfigSource

	// EmailDelivery advances the push EmailDelivery type state on delivery.
	EmailDelivery DeliveryNotifier

	// Processor is an optional test seam. Production leaves it nil so a fresh
	// Receiver is built with the latest rules for each message.
	Processor RawEventProcessor
}

type stagedMessageProcessor struct {
	client  StagedS3API
	bucket  string
	base    ReceiverConfig
	source  MailIdentityConfigSource
	process RawEventProcessor
}

var _ StagedMessageProcessor = (*stagedMessageProcessor)(nil)

// NewStagedMessageProcessor builds the support API's staged-message service.
// It keeps S3 reads, dedup and receiver construction out of HTTP handlers.
func NewStagedMessageProcessor(cfg StagedMessageProcessorConfig) (StagedMessageProcessor, error) {
	if cfg.Client == nil {
		return nil, errors.New("staged message processor requires an S3 client")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("staged message processor requires a bucket")
	}
	if cfg.Processor == nil && (cfg.UserManager == nil || cfg.Sender == nil || cfg.Storage == nil || strings.TrimSpace(cfg.Domain) == "") {
		return nil, errors.New("staged message processor requires receiver services")
	}

	base := ReceiverConfig{
		UserManager:   cfg.UserManager,
		Sender:        cfg.Sender,
		Domain:        cfg.Domain,
		Storage:       cfg.Storage,
		Metadata:      cfg.Metadata,
		Dedup:         NewS3Dedup(cfg.Client, cfg.Bucket),
		EmailDelivery: cfg.EmailDelivery,
	}
	return &stagedMessageProcessor{
		client:  cfg.Client,
		bucket:  cfg.Bucket,
		base:    base,
		source:  cfg.IdentityConfig,
		process: cfg.Processor,
	}, nil
}

// ProcessRawKey reads and processes one staged object, then lets the receiver
// delete it through the shared S3 event adapter.
func (p *stagedMessageProcessor) ProcessRawKey(ctx context.Context, bucket, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("staged message key is required")
	}
	if strings.TrimSpace(bucket) == "" {
		bucket = p.bucket
	}

	processor := p.process
	if processor == nil {
		cfg := p.base
		if p.source != nil {
			cfg.IdentityConfig = p.source.MailIdentityConfig().Clone()
		}
		var err error
		processor, err = NewReceiver(cfg)
		if err != nil {
			return fmt.Errorf("create receiver for staged key %s: %w", key, err)
		}
	}
	return processor.ProcessEvent(ctx, NewRawS3Event(p.client, bucket, key))
}
