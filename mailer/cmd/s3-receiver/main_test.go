package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
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
			if got := isInboundEmailKey(tt.key); got != tt.want {
				t.Errorf("isInboundEmailKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

type recordingProcessor struct {
	calls []string
	errs  map[string]error
}

func (p *recordingProcessor) ProcessS3Event(_ context.Context, key string) error {
	p.calls = append(p.calls, key)
	return p.errs[key]
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
		errs: map[string]error{"bad": errors.New("delivery failed")},
	}
	event := s3EventFor("bucket", "good", "bad", "receiver/config.json", "other-bucket-key")
	event.Records[3].S3.Bucket.Name = "other-bucket"

	err := processEvent(context.Background(), processor, event, "bucket")
	if err == nil || !strings.Contains(err.Error(), "process bad") {
		t.Fatalf("processEvent() error = %v, want the bad record's failure", err)
	}
	want := []string{"good", "bad"}
	if len(processor.calls) != len(want) {
		t.Fatalf("processed keys = %v, want %v", processor.calls, want)
	}
	for i, key := range want {
		if processor.calls[i] != key {
			t.Errorf("processed key %d = %q, want %q", i, processor.calls[i], key)
		}
	}
}

func TestProcessEventNilWhenAllRecordsSucceed(t *testing.T) {
	processor := &recordingProcessor{}
	event := s3EventFor("bucket", "a", "b")
	if err := processEvent(context.Background(), processor, event, "bucket"); err != nil {
		t.Fatalf("processEvent() error = %v, want nil", err)
	}
	if len(processor.calls) != 2 {
		t.Errorf("processed keys = %v, want two records", processor.calls)
	}
}
