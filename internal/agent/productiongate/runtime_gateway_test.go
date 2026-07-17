package productiongate_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentapp "github.com/keir-research/ai-native-paas/internal/agent/application"
	agentdomain "github.com/keir-research/ai-native-paas/internal/agent/domain"
	"github.com/keir-research/ai-native-paas/internal/agent/productiongate"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func runtimeDeployRequest() runtimev1.DeployRequest {
	return runtimev1.DeployRequest{
		TenantID:      "tenant-1",
		ApplicationID: "app-1",
		EnvironmentID: "env-1",
		Artifact: buildv1.ArtifactRef{
			ArtifactID: "artifact-1",
			Repository: "registry.test/tenant-1/app",
			Digest:     "sha256:" + strings.Repeat("a", 64),
			MediaType:  "application/vnd.oci.image.manifest.v1+json",
		},
		Configuration: runtimev1.ReleaseConfig{
			Region:    "eu1",
			Isolation: runtimev1.IsolationSandboxed,
			Unit:      "u1",
			Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 1}},
		},
		IdempotencyKey: "deploy-1",
		ActorID:        "agent-1",
	}
}

func TestHTTPRuntime_EmptyBaseURLFailsClosed(t *testing.T) {
	gateway := productiongate.NewHTTPRuntime("", "agent-api")
	if _, err := gateway.Deploy(context.Background(), runtimeDeployRequest(), 4); err == nil {
		t.Fatal("deploy should remain unavailable without runtime URL")
	}
	if _, err := gateway.Get(context.Background(), "tenant-1", "deployment-1"); err == nil {
		t.Fatal("status should remain unavailable without runtime URL")
	}
	if _, err := gateway.Rollback(context.Background(), "tenant-1", "env-1", "release-1", 5, "rollback-1"); err == nil {
		t.Fatal("rollback should remain unavailable without runtime URL")
	}
}

func TestHTTPRuntime_DeployUsesTenantScopedRuntimeAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/applications/app-1/environments/env-1/deployments" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" ||
			r.Header.Get("X-Principal-Kind") != "" || r.Header.Get("Idempotency-Key") != "deploy-1" ||
			r.Header.Get("X-Correlation-ID") != "agent-api-runtime-deploy-1" || r.Header.Get("X-Expected-Environment-Revision") != "4" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body struct {
			Artifact      buildv1.ArtifactRef     `json:"artifact"`
			Configuration runtimev1.ReleaseConfig `json:"configuration"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		request := runtimeDeployRequest()
		if body.Artifact != request.Artifact || body.Configuration.Region != "eu1" || body.Configuration.Unit != "u1" {
			t.Fatalf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(runtimev1.DeploymentRef{
			DeploymentID: "deployment-1", ReleaseID: "release-1", Phase: runtimev1.DeploymentGitCommitted,
			GitOpsRevision: strings.Repeat("b", 40),
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPRuntime(server.URL, "agent-api")
	result, err := gateway.Deploy(context.Background(), runtimeDeployRequest(), 4)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if result.Deployment.DeploymentID != "deployment-1" || result.Deployment.ReleaseID != "release-1" || result.Deployment.Phase != runtimev1.DeploymentGitCommitted {
		t.Fatalf("result=%+v", result)
	}
}

func TestHTTPRuntime_GetVerifiesDeploymentIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organizations/tenant-1/deployments/deployment-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("X-Principal-ID") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(runtimev1.RuntimeStatus{
			DeploymentID: "deployment-other", Phase: runtimev1.DeploymentReady, ActiveRelease: "release-1", URL: "https://app.example.test", ReadyReplicas: 1,
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPRuntime(server.URL, "agent-api")
	_, err := gateway.Get(context.Background(), "tenant-1", "deployment-1")
	var providerErr *agentapp.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("mismatched response must fail closed: %#v", err)
	}
}

func TestHTTPRuntime_RollbackUsesIdempotentRuntimeAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/tenant-1/deployments/deployment-1/rollback" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Tenant-ID") != "tenant-1" || r.Header.Get("Idempotency-Key") != "rollback-1" ||
			r.Header.Get("X-Correlation-ID") != "agent-api-runtime-rollback-1" || r.Header.Get("X-Expected-Environment-Revision") != "7" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["target_release_id"] != "release-1" || body["critical_override"] != false {
			t.Fatalf("body=%v", body)
		}
		_ = json.NewEncoder(w).Encode(runtimev1.DeploymentRef{
			DeploymentID: "deployment-2", ReleaseID: "release-2", Phase: runtimev1.DeploymentGitCommitted,
		})
	}))
	defer server.Close()

	gateway := productiongate.NewHTTPRuntime(server.URL, "agent-api")
	result, err := gateway.Rollback(context.Background(), "tenant-1", "deployment-1", "release-1", 7, "rollback-1")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if result.Deployment.DeploymentID != "deployment-2" || result.Deployment.ReleaseID != "release-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestHTTPRuntime_MapsClientAndServerErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"deployment not found"}}`))
		}))
		defer server.Close()
		_, err := productiongate.NewHTTPRuntime(server.URL, "agent-api").Get(context.Background(), "tenant-1", "deployment-1")
		var domainErr *agentdomain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != agentdomain.CodeNotFound || domainErr.Retryable {
			t.Fatalf("err=%#v", err)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAVAILABLE","message":"runtime down"}}`))
		}))
		defer server.Close()
		_, err := productiongate.NewHTTPRuntime(server.URL, "agent-api").Get(context.Background(), "tenant-1", "deployment-1")
		var providerErr *agentapp.ProviderError
		if !errors.As(err, &providerErr) || !providerErr.Retryable {
			t.Fatalf("err=%#v", err)
		}
	})
}
