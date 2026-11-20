package identity

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
)

type fakeLister struct {
	pages []*ses.ListIdentitiesOutput
	err   error
	calls int
}

func (f *fakeLister) ListIdentities(_ context.Context, in *ses.ListIdentitiesInput, _ ...func(*ses.Options)) (*ses.ListIdentitiesOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	index := 0
	if in.NextToken != nil {
		index, _ = strconv.Atoi(*in.NextToken)
	}
	if index >= len(f.pages) {
		return &ses.ListIdentitiesOutput{}, nil
	}
	page := *f.pages[index]
	if index+1 < len(f.pages) {
		page.NextToken = aws.String(strconv.Itoa(index + 1))
	} else {
		page.NextToken = nil
	}
	return &page, nil
}

func TestValidateMatchesDomainAndAddressIdentities(t *testing.T) {
	lister := &fakeLister{pages: []*ses.ListIdentitiesOutput{
		{Identities: []string{"example.com"}},
		{Identities: []string{"Ops@Other.example"}},
	}}
	v := NewValidator(lister)

	for _, from := range []string{
		"alice@example.com",
		"Alice@Example.COM",
		"ops@other.example",
	} {
		if err := v.Validate(context.Background(), from); err != nil {
			t.Errorf("Validate(%q) = %v, want allowed", from, err)
		}
	}
	for _, from := range []string{
		"alice@other.example", // only a single address, not the domain, is verified
		"alice@notverified.com",
	} {
		if err := v.Validate(context.Background(), from); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Validate(%q) = %v, want ErrNotAllowed", from, err)
		}
	}
	// One call per page for the initial load; no further calls while cached.
	if lister.calls != 2 {
		t.Errorf("ListIdentities calls = %d, want 2 (one per page, then cached)", lister.calls)
	}
}

func TestValidateRefreshesAfterTTL(t *testing.T) {
	lister := &fakeLister{pages: []*ses.ListIdentitiesOutput{{Identities: []string{"example.com"}}}}
	v := NewValidatorWithTTL(lister, time.Millisecond)
	ctx := context.Background()
	if err := v.Validate(ctx, "alice@example.com"); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := v.Validate(ctx, "alice@example.com"); err != nil {
		t.Fatalf("Validate() after TTL = %v", err)
	}
	if lister.calls != 2 {
		t.Errorf("ListIdentities calls = %d, want 2", lister.calls)
	}
}

func TestValidatePropagatesListingFailure(t *testing.T) {
	want := errors.New("SES unavailable")
	v := NewValidator(&fakeLister{err: want})
	err := v.Validate(context.Background(), "alice@example.com")
	if !errors.Is(err, want) {
		t.Fatalf("Validate() error = %v, want %v", err, want)
	}
	if errors.Is(err, ErrNotAllowed) {
		t.Error("a listing failure must not be reported as a rejected identity")
	}
}

func TestValidateRejectsMalformedFrom(t *testing.T) {
	v := NewValidator(&fakeLister{pages: []*ses.ListIdentitiesOutput{{Identities: []string{"example.com"}}}})
	if err := v.Validate(context.Background(), "not-an-address"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Validate() = %v, want ErrNotAllowed", err)
	}
}
