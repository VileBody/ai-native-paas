package productiongate_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapp "github.com/keir-research/ai-native-paas/internal/agent/application"
	agentdomain "github.com/keir-research/ai-native-paas/internal/agent/domain"
	"github.com/keir-research/ai-native-paas/internal/agent/productiongate"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func TestHTTPAttachments_EmptyBaseURLFailsClosed(t *testing.T) {
	gateway := productiongate.NewHTTPAttachments("", "agent-api")
	_, err := gateway.SetSecret(context.Background(), attachmentsv1.SetSecretRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Name: "API_KEY", Value: "secret", IdempotencyKey: "idem-1",
	})
	var providerErr *agentapp.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable || !strings.Contains(providerErr.Error(), "openbao_unseal") {
		t.Fatalf("err=%#v", err)
	}
}

func TestHTTPAttachments_SetSecretUsesScopedWriteOnlyAPI(t *testing.T) {
	const sentinel = "secret-must-never-escape"
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/applications/app-1/environments/env-1/secrets" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" || r.Header.Get("Idempotency-Key") != "idem-secret" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body struct {
			Name  string                    `json:"name"`
			Scope attachmentsv1.SecretScope `json:"scope"`
			Phase attachmentsv1.SecretPhase `json:"phase"`
			Value string                    `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "API_KEY" || body.Scope != attachmentsv1.SecretScopeRuntime || body.Phase != attachmentsv1.SecretPhaseRuntime || body.Value != sentinel {
			t.Fatalf("body metadata mismatch: name=%q scope=%q phase=%q value_matches=%t", body.Name, body.Scope, body.Phase, body.Value == sentinel)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secret":              attachmentsv1.SecretMetadata{Name: "API_KEY", Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime, Version: 1, UpdatedAt: now},
			"attachment_snapshot": map[string]any{"snapshot_id": "snap-1", "environment_id": "env-1", "version": 1},
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	metadata, err := gateway.SetSecret(context.Background(), attachmentsv1.SetSecretRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Name: "API_KEY", Value: sentinel, IdempotencyKey: "idem-secret", ActorID: "agent-1",
	})
	if err != nil {
		t.Fatalf("set secret: %v", err)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "API_KEY" || metadata.Version != 1 || strings.Contains(string(raw), sentinel) {
		t.Fatalf("metadata=%+v", metadata)
	}
}

func TestHTTPAttachments_DoesNotReflectSecretFromUpstreamError(t *testing.T) {
	const sentinel = "upstream-echoed-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "INVALID_ARGUMENT", "message": sentinel}})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	_, err := gateway.SetSecret(context.Background(), attachmentsv1.SetSecretRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Name: "API_KEY", Value: sentinel, IdempotencyKey: "idem-secret",
	})
	var domainErr *agentdomain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != agentdomain.CodeInvalidArgument || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err=%#v", err)
	}
}

func TestHTTPAttachments_ListSecretMetadataRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []map[string]any{{"name": "API_KEY", "version": 0}}})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	if _, err := gateway.ListSecretMetadata(context.Background(), "tenant-1", "app-1", "env-1"); err == nil {
		t.Fatal("malformed upstream secret metadata was accepted")
	}
}

func TestHTTPAttachments_ProvisionRejectsCrossTenantResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/organizations/tenant-1/services" || r.Header.Get("X-Tenant-ID") != "tenant-1" {
			t.Fatalf("request scope path=%s headers=%v", r.URL.Path, r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "svc-1", "tenant_id": "tenant-2", "plan_id": "pg-small", "type": "postgresql", "state": "REQUESTED",
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	_, err := gateway.Provision(context.Background(), attachmentsv1.ServiceRequest{
		TenantID: "tenant-1", ServiceType: "postgresql", Plan: "pg-small", Name: "primary", IdempotencyKey: "idem-provision",
	})
	var providerErr *agentapp.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("cross-tenant response err=%#v", err)
	}
}

func TestHTTPAttachments_BindVerifiesApplicationEnvironmentAndInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/applications/app-1/environments/env-1/bindings" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			InstanceID   string   `json:"instance_id"`
			Capabilities []string `json:"capabilities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.InstanceID != "svc-1" || len(body.Capabilities) != 1 || body.Capabilities[0] != "read" {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"binding": map[string]any{
				"id": "bind-1", "tenant_id": "tenant-1", "application_id": "app-1",
				"environment_id": "env-1", "instance_id": "svc-1", "state": "ACTIVE",
			},
			"attachment_snapshot": map[string]any{"snapshot_id": "snap-1", "environment_id": "env-1", "version": 2},
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	binding, err := gateway.Bind(context.Background(), attachmentsv1.BindRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		ServiceInstanceID: "svc-1", IdempotencyKey: "idem-bind",
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if binding.BindingID != "bind-1" || binding.SnapshotID != "snap-1" || binding.State != "ACTIVE" {
		t.Fatalf("binding=%+v", binding)
	}
}

func TestHTTPAttachments_AddDomainVerifiesScopedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/applications/app-1/environments/env-1/domains" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "domain-1", "tenant_id": "tenant-1", "application_id": "app-1", "environment_id": "env-1",
			"hostname": "App.Example.COM.", "state": "AWAITING_VERIFICATION", "challenge_value": "not-forwarded",
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	domain, err := gateway.AddDomain(context.Background(), attachmentsv1.DomainRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Hostname: "app.example.com", IdempotencyKey: "idem-domain",
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}
	if domain.DomainClaimID != "domain-1" || domain.State != "AWAITING_VERIFICATION" || strings.Contains(domain.Hostname, "not-forwarded") {
		t.Fatalf("domain=%+v", domain)
	}
}

func TestHTTPAttachments_MapsRetryableUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPAttachments(server.URL, "agent-api")
	_, err := gateway.Provision(context.Background(), attachmentsv1.ServiceRequest{
		TenantID: "tenant-1", ServiceType: "redis", Plan: "redis-small", Name: "cache", IdempotencyKey: "idem-provision",
	})
	var providerErr *agentapp.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("err=%#v", err)
	}
}
