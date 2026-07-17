package productiongate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/productiongate"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

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

func TestHTTPCommerce_CheckUsesTenantScopedCommerceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/entitlements/check" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "agent-api" || r.Header.Get("X-Principal-Kind") != "service" {
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
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "agent-api" || r.Header.Get("X-Principal-Kind") != "service" {
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
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "agent-api" || r.Header.Get("X-Principal-Kind") != "service" || r.Header.Get("X-Scopes") != "kernel.operation.read kernel.operation.cancel" {
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
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "agent-api" || r.Header.Get("X-Principal-Kind") != "service" || r.Header.Get("Idempotency-Key") != "agent-api-post-operation-1" {
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
