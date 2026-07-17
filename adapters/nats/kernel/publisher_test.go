package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/nats-io/nats.go/jetstream"
)

type publisherSpy struct {
	subject string
	payload []byte
	opts    []jetstream.PublishOpt
}

func (s *publisherSpy) Publish(_ context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	s.subject = subject
	s.payload = append([]byte(nil), payload...)
	s.opts = append([]jetstream.PublishOpt(nil), opts...)
	return &jetstream.PubAck{Stream: DefaultStream, Sequence: 1}, nil
}

func TestPublisherUsesFixedSubjectAndRedactedEnvelope(t *testing.T) {
	spy := &publisherSpy{}
	publisher := NewPublisher(spy, Config{})
	event := kernelv1.DomainEventEnvelope[json.RawMessage]{
		EventID:       "event-1",
		Type:          "operation.started.v2",
		Version:       2,
		TenantID:      "tenant-1",
		AggregateID:   "operation-1",
		CorrelationID: "correlation-1",
		OccurredAt:    time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		Payload:       json.RawMessage(`{"token":"sentinel-secret","safe":"visible"}`),
	}

	if err := publisher.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if spy.subject != DefaultSubject {
		t.Fatalf("subject = %q", spy.subject)
	}
	if len(spy.opts) != 2 {
		t.Fatalf("publish options = %d, want msg id and expected stream", len(spy.opts))
	}
	var persisted map[string]any
	if err := json.Unmarshal(spy.payload, &persisted); err != nil {
		t.Fatal(err)
	}
	payload := persisted["payload"].(map[string]any)
	if payload["token"] != "[REDACTED]" || payload["safe"] != "visible" {
		t.Fatalf("payload was not safely marshaled: %#v", payload)
	}
}

func TestPublisherRejectsInvalidEventBeforeJetStream(t *testing.T) {
	spy := &publisherSpy{}
	publisher := NewPublisher(spy, Config{})
	if err := publisher.Publish(context.Background(), kernelv1.DomainEventEnvelope[json.RawMessage]{}); err == nil {
		t.Fatal("invalid event was published")
	}
	if spy.subject != "" {
		t.Fatal("JetStream was called for an invalid event")
	}
}
