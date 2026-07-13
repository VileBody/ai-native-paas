package contract_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

func TestEventEnvelope_RequiredFieldsAreSerialized(t *testing.T) {
	event := kernelv1.DomainEventEnvelope[map[string]any]{
		EventID:       "evt_1",
		Type:          "kernel.organization_created.v1",
		Version:       1,
		TenantID:      "org_1",
		AggregateID:   "org_1",
		CorrelationID: "cor_1",
		CausationID:   "cause_1",
		OccurredAt:    time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Payload:       map[string]any{"name": "Acme"},
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"event_id", "type", "version", "tenant_id", "aggregate_id", "correlation_id", "causation_id", "occurred_at", "payload"} {
		if _, exists := object[field]; !exists {
			t.Errorf("serialized event is missing %q: %s", field, encoded)
		}
	}
}

func TestEventEnvelope_UnknownFieldsAreIgnoredByV1Consumer(t *testing.T) {
	input := `{
        "event_id":"evt_1",
        "type":"kernel.organization_created.v1",
        "version":1,
        "tenant_id":"org_1",
        "aggregate_id":"org_1",
        "correlation_id":"cor_1",
        "occurred_at":"2026-07-12T12:00:00Z",
        "payload":{"name":"Acme"},
        "future_field":{"enabled":true}
    }`
	var event kernelv1.DomainEventEnvelope[map[string]any]
	if err := json.Unmarshal([]byte(input), &event); err != nil {
		t.Fatalf("v1 consumer rejected additive field: %v", err)
	}
	if event.EventID != "evt_1" || event.Payload["name"] != "Acme" {
		t.Fatalf("decoded event = %+v", event)
	}
}

func TestEventEnvelope_V1SchemaIsBackwardCompatible(t *testing.T) {
	// Optional tenant/causation fields may be absent for platform-scoped events.
	input := `{
        "event_id":"evt_1",
        "type":"kernel.audit_recorded.v1",
        "version":1,
        "aggregate_id":"audit_1",
        "correlation_id":"cor_1",
        "occurred_at":"2026-07-12T12:00:00Z",
        "payload":{}
    }`
	var event kernelv1.DomainEventEnvelope[json.RawMessage]
	if err := json.Unmarshal([]byte(input), &event); err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid historical v1 event rejected: %v", err)
	}
}

func TestEventEnvelope_DoesNotSerializeSecrets(t *testing.T) {
	event := kernelv1.DomainEventEnvelope[map[string]any]{
		EventID: "evt_1", Type: "kernel.test.v1", Version: 1,
		AggregateID: "aggregate_1", CorrelationID: "cor_1",
		OccurredAt: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Payload: map[string]any{
			"safe":         "visible",
			"access_token": "token-123",
			"nested":       map[string]any{"database_password": "hunter2"},
		},
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, secret := range []string{"token-123", "hunter2"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q leaked: %s", secret, output)
		}
	}
	if !strings.Contains(output, "visible") || strings.Count(output, "[REDACTED]") != 2 {
		t.Fatalf("unexpected redacted output: %s", output)
	}
}

func TestPrincipalContext_WildcardScopes(t *testing.T) {
	principal := kernelv1.PrincipalContext{PrincipalID: "user_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}}
	if !principal.HasScope("kernel.organization.read") {
		t.Fatal("kernel:* must match dotted kernel action")
	}
	if principal.HasScope("source.repository.read") {
		t.Fatal("kernel:* matched another domain")
	}
}
