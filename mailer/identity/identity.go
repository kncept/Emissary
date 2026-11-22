// Package identity resolves the email identities an installation is allowed
// to send from. Identities are currently derived from SES: they are the
// verified addresses and domains returned by ListIdentities in the configured
// region/account.
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
)

var (
	// ErrNotAllowed means the address is not covered by any verified identity.
	ErrNotAllowed = errors.New("from address is not a verified email identity")
	// ErrSendForbidden means sending is temporarily disabled for this account,
	// independently of the From address. JMAP maps it to forbiddenToSend.
	ErrSendForbidden = errors.New("sending is not currently permitted")
)

// Lister is the narrow SES operation needed to enumerate identities. Keeping
// it an interface makes the validator testable without AWS credentials.
type Lister interface {
	ListIdentities(ctx context.Context, params *ses.ListIdentitiesInput, optFns ...func(*ses.Options)) (*ses.ListIdentitiesOutput, error)
}

// LazyLister defers AWS configuration and SES client creation until the first
// ListIdentities call. Programmatic callers and local installations therefore
// need no SES credentials just to construct a handler.
type LazyLister struct {
	mu     sync.Mutex
	lister Lister
}

var _ Lister = (*LazyLister)(nil)

// NewLazyLister returns a lazily initialised SES identity lister.
func NewLazyLister() *LazyLister {
	return &LazyLister{}
}

// ListIdentities implements Lister.
func (l *LazyLister) ListIdentities(ctx context.Context, params *ses.ListIdentitiesInput, optFns ...func(*ses.Options)) (*ses.ListIdentitiesOutput, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lister == nil {
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("load AWS config for SES identities: %w", err)
		}
		l.lister = ses.NewFromConfig(cfg)
	}
	return l.lister.ListIdentities(ctx, params, optFns...)
}

const defaultTTL = 5 * time.Minute

// Validator checks From addresses against the account's verified SES
// identities. Identities are loaded lazily and cached for a short period so a
// submission does not need an SES round trip, while newly verified identities
// become usable without a restart.
type Validator struct {
	lister Lister
	ttl    time.Duration

	mu         sync.Mutex
	identities map[string]struct{}
	expires    time.Time
}

// NewValidator returns a validator backed by lister with the default TTL.
func NewValidator(lister Lister) *Validator {
	return NewValidatorWithTTL(lister, defaultTTL)
}

// NewValidatorWithTTL returns a validator with an explicit cache lifetime.
func NewValidatorWithTTL(lister Lister, ttl time.Duration) *Validator {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Validator{lister: lister, ttl: ttl}
}

// Validate reports whether from may be used as the sender of a message. An
// address is allowed when the full address or its domain is a verified SES
// identity. Listing failures are returned to the caller: a temporary SES
// outage must not turn into either an accidental allow or a confusing
// "not verified" rejection.
func (v *Validator) Validate(ctx context.Context, from string) error {
	parsed, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil {
		return fmt.Errorf("%w: invalid From address %q", ErrNotAllowed, from)
	}
	identities, err := v.list(ctx)
	if err != nil {
		return err
	}
	at := strings.LastIndex(parsed.Address, "@")
	if at < 0 {
		return fmt.Errorf("%w: %q has no domain", ErrNotAllowed, parsed.Address)
	}
	domain := strings.ToLower(parsed.Address[at+1:])
	address := strings.ToLower(parsed.Address)
	if _, ok := identities[address]; ok {
		return nil
	}
	if _, ok := identities[domain]; ok {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNotAllowed, parsed.Address)
}

// list loads the identities, refreshing the cache when it expires. The mutex
// is held across the API call so a burst of submissions triggers one refresh.
func (v *Validator) list(ctx context.Context) (map[string]struct{}, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.identities != nil && time.Now().Before(v.expires) {
		return v.identities, nil
	}
	if v.lister == nil {
		return nil, errors.New("no email identity lister configured")
	}

	found := make(map[string]struct{})
	paginator := ses.NewListIdentitiesPaginator(v.lister, &ses.ListIdentitiesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list verified email identities: %w", err)
		}
		for _, identity := range page.Identities {
			identity = strings.ToLower(strings.TrimSpace(identity))
			if identity != "" {
				found[identity] = struct{}{}
			}
		}
	}
	v.identities = found
	v.expires = time.Now().Add(v.ttl)
	return found, nil
}
