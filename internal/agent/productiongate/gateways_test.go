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

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	agentapp "github.com/keir-research/ai-native-paas/internal/agent/application"
	agentdomain "github.com/keir-research/ai-native-paas/internal/agent/domain"
	"github.com/keir-research/ai-native-paas/internal/agent/productiongate"
	buildapp "github.com/keir-research/ai-native-paas/internal/build/application"
	builddomain "github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func digest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func revision() sourcev1.SourceRevision {
	return sourcev1.SourceRevision{
		ProjectID: "project-1", RepositoryID: "repo-1", Branch: "main",
		CommitSHA: strings.Repeat("a", 40), SourceRoot: "cmd/api",
	}
}

func TestHTTPCommerce_EmptyBaseURLFailsClosed(t *testing.T) {
	gateway := productiongate.NewHTTPCommerce("", "agent-api")
	decision, err := gateway.Check(context.Background(), commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy"})
	if err != nil {
		t.Fatalf("empty commerce gateway should deny without transport error: %v", err)
	}
	if decision.Allowed || decision.PolicyVersion != "network-deferred-v1" {
		t.Fatalf("decision=%+v", decision)
	}
	if _, err := gateway.GetUsage(context.Background(), "tenant-1", "period-1"); err == nil {
		t.Fatal("usage preview should remain unavailable without commerce URL")
	}
}

func TestHTTPBuilds_EmptyBaseURLFailsClosed(t *testing.T) {
	gateway := productiongate.NewHTTPBuilds("", "agent-api", digest("b"), digest("c"), "platform-v1")
	if _, err := gateway.Request(context.Background(), "tenant-1", revision(), 5, "idem-1", "corr-1"); err == nil {
		t.Fatal("build request should remain unavailable without build URL")
	}
}

func TestHTTPBuilds_RequestUsesTenantScopedBuildAPI(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/builds" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" ||
			r.Header.Get("Idempotency-Key") != "idem-1" || r.Header.Get("X-Correlation-ID") != "corr-1" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body struct {
			Source          sourcev1.SourceRevision `json:"source"`
			Config          builddomain.BuildConfig `json:"config"`
			BuilderDigest   string                  `json:"builder_digest"`
			RunImageDigest  string                  `json:"run_image_digest"`
			PlatformVersion string                  `json:"platform_version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Source != revision() || body.Config.Type != builddomain.BuildTypeAuto || body.BuilderDigest != digest("b") || body.RunImageDigest != digest("c") || body.PlatformVersion != "platform-v1" {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(buildapp.RequestBuildResult{Build: builddomain.Build{
			ID: "bld-1", TenantID: "tenant-1", Identity: "identity-1", State: buildv1.BuildQueued,
			CorrelationID: "corr-1", CreatedAt: now, UpdatedAt: now,
		}})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPBuilds(server.URL, "agent-api", digest("b"), digest("c"), "platform-v1")
	result, err := gateway.Request(context.Background(), "tenant-1", revision(), 5, "idem-1", "corr-1")
	if err != nil {
		t.Fatalf("request build: %v", err)
	}
	if result.Build.BuildID != "bld-1" || result.Build.TenantID != "tenant-1" || result.Build.State != buildv1.BuildQueued {
		t.Fatalf("result=%+v", result)
	}
}

func TestHTTPBuilds_GetMapsReleasableArtifact(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organizations/tenant-1/builds/bld-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(buildapp.RequestBuildResult{
			Build: builddomain.Build{
				ID: "bld-1", TenantID: "tenant-1", Identity: "identity-1", State: buildv1.BuildSucceeded,
				ArtifactID: "art-1", CorrelationID: "corr-1", CreatedAt: now, UpdatedAt: now,
			},
			Artifact: &builddomain.Artifact{
				ID: "art-1", TenantID: "tenant-1", BuildID: "bld-1", Repository: "registry.test/tenant-1/app",
				Digest: digest("d"), MediaType: "application/vnd.oci.image.manifest.v1+json", State: builddomain.ArtifactReleasable,
			},
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPBuilds(server.URL, "agent-api", digest("b"), digest("c"), "platform-v1")
	result, err := gateway.Get(context.Background(), "tenant-1", "bld-1")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if result.Build.Artifact == nil || result.Build.Artifact.ArtifactID != "art-1" || result.Build.Artifact.Digest != digest("d") {
		t.Fatalf("artifact not mapped: %+v", result.Build)
	}
}

func TestHTTPBuilds_MapsClientErrorsToAgentDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "build not found"}})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPBuilds(server.URL, "agent-api", digest("b"), digest("c"), "platform-v1")
	_, err := gateway.Get(context.Background(), "tenant-1", "bld-missing")
	var domainErr *agentdomain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != agentdomain.CodeNotFound || domainErr.Retryable {
		t.Fatalf("err=%#v", err)
	}
}

func TestHTTPBuilds_MapsServerErrorsToRetryableProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAVAILABLE","message":"db down"}}`))
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPBuilds(server.URL, "agent-api", digest("b"), digest("c"), "platform-v1")
	_, err := gateway.Get(context.Background(), "tenant-1", "bld-1")
	var providerErr *agentapp.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("err=%#v", err)
	}
}

func TestHTTPCommerce_CheckUsesTenantScopedCommerceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/entitlements/check" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body commercev1.EntitlementRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.TenantID != "tenant-1" || body.Feature != "deploy" || body.Resource != "task-1" {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(commercev1.EntitlementDecision{Allowed: true, PolicyVersion: "policy-v1", PlanVersionID: "beta-v1"})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPCommerce(server.URL, "agent-api")
	decision, err := gateway.Check(context.Background(), commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", Resource: "task-1", Quantity: 1})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !decision.Allowed || decision.PolicyVersion != "policy-v1" || decision.PlanVersionID != "beta-v1" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestHTTPCommerce_GetUsageUsesTenantScopedPreviewAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organizations/tenant-1/billing-periods/period-1/invoice-preview" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(commercev1.InvoicePreview{
			TenantID: "tenant-1", PeriodID: "period-1", Currency: "RUB", PolicyVersion: "policy-v1",
			Lines: []commercev1.InvoiceLine{{ResourceType: "deployment", ResourceID: "deployment-1", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard, Quantity: 60}},
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPCommerce(server.URL, "agent-api")
	preview, err := gateway.GetUsage(context.Background(), "tenant-1", "period-1")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if preview.TenantID != "tenant-1" || preview.PeriodID != "period-1" || len(preview.Lines) != 1 || preview.Lines[0].Meter != commercev1.MeterRuntimeUnitSeconds {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestHTTPOperations_EmptyBaseURLFailsClosed(t *testing.T) {
	gateway := productiongate.NewHTTPOperations("", "agent-api")
	if _, err := gateway.Get(context.Background(), "tenant-1", "operation-1"); err == nil {
		t.Fatal("operation get should remain unavailable without kernel URL")
	}
	if err := gateway.Cancel(context.Background(), "tenant-1", "operation-1"); err == nil {
		t.Fatal("operation cancel should remain unavailable without kernel URL")
	}
}

func TestHTTPOperations_GetUsesTenantScopedKernelAPI(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/operations/operation-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" || r.Header.Get("X-Scopes") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Fatalf("read operation must not send idempotency key: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(kernelv1.OperationSnapshot{
			OperationRef: kernelv1.OperationRef{OperationID: "operation-1", TenantID: "tenant-1", State: kernelv1.OperationRunning},
			Kind:         "build_execute",
			Version:      3,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPOperations(server.URL, "agent-api")
	snapshot, err := gateway.Get(context.Background(), "tenant-1", "operation-1")
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if snapshot.OperationID != "operation-1" || snapshot.TenantID != "tenant-1" || snapshot.State != kernelv1.OperationRunning || snapshot.Kind != "build_execute" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestHTTPOperations_CancelUsesIdempotentKernelAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/operations/operation-1/cancel" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("X-Principal-Kind") != "" || r.Header.Get("Idempotency-Key") != "agent-api-post-operation-1" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Operation kernelv1.OperationRef `json:"operation"`
		}{Operation: kernelv1.OperationRef{OperationID: "operation-1", TenantID: "tenant-1", State: kernelv1.OperationCanceled}})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPOperations(server.URL, "agent-api")
	if err := gateway.Cancel(context.Background(), "tenant-1", "operation-1"); err != nil {
		t.Fatalf("cancel operation: %v", err)
	}
}

func TestHTTPOperations_RejectsCrossTenantKernelResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(kernelv1.OperationSnapshot{
			OperationRef: kernelv1.OperationRef{OperationID: "operation-1", TenantID: "tenant-2", State: kernelv1.OperationRunning},
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPOperations(server.URL, "agent-api")
	if _, err := gateway.Get(context.Background(), "tenant-1", "operation-1"); err == nil {
		t.Fatal("cross-tenant kernel response was accepted")
	}
}
