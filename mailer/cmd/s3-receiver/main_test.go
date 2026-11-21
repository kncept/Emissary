package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/kncept/Emissary/mailer/receiver"
)

func TestIsInboundEmailKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "01000192a3b4c5d6", want: true},
		{key: "message.eml", want: true},
		{key: "receiver/kncept.com.json", want: false},
		{key: "receiver/_.json", want: false},
		{key: "some/nested/key", want: false},
		{key: "/leading-slash", want: false},
		{key: "trailing-slash/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := receiver.IsInboundEmailKey(tt.key); got != tt.want {
				t.Errorf("receiver.IsInboundEmailKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// recordingProcessor is a test implementation of the narrow receiver seam.
type recordingProcessor struct {
	calls int
	errs  map[int]error
}

func (p *recordingProcessor) ProcessEvent(_ context.Context, _ receiver.InboundRawEmailEvent) error {
	p.calls++
	return p.errs[p.calls]
}

func s3EventFor(bucket string, keys ...string) events.S3Event {
	event := events.S3Event{}
	for _, key := range keys {
		event.Records = append(event.Records, events.S3EventRecord{
			S3: events.S3Entity{
				Bucket: events.S3Bucket{Name: bucket},
				Object: events.S3Object{Key: key},
			},
		})
	}
	return event
}

func TestProcessEventReturnsFailuresToLambda(t *testing.T) {
	processor := &recordingProcessor{
		errs: map[int]error{2: errors.New("delivery failed")},
	}
	event := s3EventFor("bucket", "good", "bad", "receiver/config.json", "other-bucket-key")
	event.Records[3].S3.Bucket.Name = "other-bucket"

	err := processEvent(context.Background(), processor, event, "bucket", nil)
	if err == nil {
		t.Fatal("processEvent() error = nil, want joined failure")
	}
	if !strings.Contains(err.Error(), "process bad: delivery failed") {
		t.Errorf("processEvent() error = %q, want failure for key bad", err)
	}
	if processor.calls != 2 {
		t.Errorf("ProcessEvent calls = %d, want 2 (only root objects in the configured bucket)", processor.calls)
	}
}

func TestProcessEventNilWhenAllRecordsSucceed(t *testing.T) {
	processor := &recordingProcessor{}
	event := s3EventFor("bucket", "a", "b")

	if err := processEvent(context.Background(), processor, event, "bucket", nil); err != nil {
		t.Fatalf("processEvent() error = %v, want nil", err)
	}
	if processor.calls != 2 {
		t.Errorf("ProcessEvent calls = %d, want 2", processor.calls)
	}
}
