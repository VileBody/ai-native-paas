package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/httpapi"
	"github.com/keir-research/ai-native-paas/internal/runtime/memory"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type gitOpsFake struct {
	bundles map[string]application.RenderedBundle
}

func (g *gitOpsFake) Commit(_ context.Context, req application.CommitRequest) (application.CommitResult, error) {
	if g.bundles == nil {
		g.bundles = map[string]application.RenderedBundle{}
	}
	g.bundles[req.Bundle.ReleaseID] = req.Bundle
	return application.CommitResult{CommitSHA: strings.Repeat("a", 40), Path: req.Bundle.Path, ManifestHash: req.Bundle.ManifestHash}, nil
}
func (g *gitOpsFake) FindByRelease(_ context.Context, _ string, releaseID string) (application.CommitResult, bool, error) {
	bundle, ok := g.bundles[releaseID]
	return application.CommitResult{CommitSHA: strings.Repeat("a", 40), Path: bundle.Path, ManifestHash: bundle.ManifestHash}, ok, nil
}

func runtimeHandler(t *testing.T) (*httpapi.Handler, application.Service, string, string) {
	t.Helper()
	store := memory.New()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	service := application.Service{
		Store: store, Artifacts: &testkit.ArtifactPolicy{Allowed: true}, Renderer: gitops.Renderer{}, GitOps: &gitOpsFake{},
		Units: application.StaticUnitCatalog{"u1": 1}, Clock: clock, IDs: &testkit.IDs{}, Scheduler: application.DeterministicScheduler{},
	}
	if _, err := service.RegisterCell(context.Background(), application.RegisterCellRequest{ID: "cell-a", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100, GitOpsRepository: "https://git.example.invalid/runtime.git", ClusterServer: "https://kubernetes.default.svc", ArgoProject: "runtime-cell", IngressDomain: "apps.eu1.example.invalid"}); err != nil {
		t.Fatal(err)
	}
	app, env, err := service.CreateApplication(context.Background(), application.CreateApplicationRequest{TenantID: "tenant-1", ProjectID: "project-1", Name: "booking", ActorID: "user-1", IdempotencyKey: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	handler := &httpapi.Handler{Runtime: &service, MaxBodyBytes: 4096}
	return handler, service, app.ID, env.ID
}

func request(t *testing.T, handler http.Handler, method, path, tenant, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
		req.Header.Set("X-Principal-ID", "user-1")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestHandler_HealthDoesNotRequireTenantHeaders(t *testing.T) {
	handler, _, _, _ := runtimeHandler(t)
	response := request(t, handler, http.MethodGet, "/healthz", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandler_CreateApplicationDerivesTenantAndActorFromAuthenticatedContext(t *testing.T) {
	handler, _, _, _ := runtimeHandler(t)
	response := request(t, handler, http.MethodPost, "/v1/organizations/tenant-1/applications", "tenant-1", `{"project_id":"project-2","name":"payments"}`, map[string]string{"Idempotency-Key": "create-payments"})
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"TenantID":"tenant-1"`) || !strings.Contains(response.Body.String(), `"ProjectID":"project-2"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestHandler_RejectsCrossTenantPath(t *testing.T) {
	handler, _, _, _ := runtimeHandler(t)
	response := request(t, handler, http.MethodPost, "/v1/organizations/tenant-2/applications", "tenant-1", `{"project_id":"project-2","name":"payments"}`, map[string]string{"Idempotency-Key": "x"})
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandler_RejectsUnknownJSONFields(t *testing.T) {
	handler, _, _, _ := runtimeHandler(t)
	response := request(t, handler, http.MethodPost, "/v1/organizations/tenant-1/applications", "tenant-1", `{"project_id":"project-2","name":"payments","tenant_id":"tenant-2"}`, map[string]string{"Idempotency-Key": "unknown"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandler_DeployAcceptsOnlyArtifactAndReleaseConfigurationFromBody(t *testing.T) {
	handler, service, appID, envID := runtimeHandler(t)
	body, err := json.Marshal(map[string]any{"artifact": testkit.Artifact("a"), "configuration": testkit.Config()})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/tenant-1/applications/" + appID + "/environments/" + envID + "/deployments"
	response := request(t, handler, http.MethodPost, path, "tenant-1", string(body), map[string]string{"Idempotency-Key": "deploy-1"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var ref runtimev1.DeploymentRef
	if err := json.Unmarshal(response.Body.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	status, err := service.StatusForTenant(context.Background(), "tenant-1", ref.DeploymentID)
	if err != nil || status.Phase != runtimev1.DeploymentGitCommitted {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestHandler_StatusDoesNotRevealAnotherTenantDeployment(t *testing.T) {
	handler, service, appID, envID := runtimeHandler(t)
	ref, err := service.Deploy(context.Background(), runtimev1.DeployRequest{TenantID: "tenant-1", ApplicationID: appID, EnvironmentID: envID, Artifact: testkit.Artifact("a"), Configuration: testkit.Config(), IdempotencyKey: "deploy-hidden", ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodGet, "/v1/organizations/tenant-2/deployments/"+ref.DeploymentID, "tenant-2", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandler_RollbackByDeploymentMatchesAgentContract(t *testing.T) {
	handler, service, appID, envID := runtimeHandler(t)
	target, err := service.Deploy(context.Background(), runtimev1.DeployRequest{
		TenantID: "tenant-1", ApplicationID: appID, EnvironmentID: envID,
		Artifact: testkit.Artifact("a"), Configuration: testkit.Config(), IdempotencyKey: "deploy-rollback-target", ActorID: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"target_release_id": target.ReleaseID})
	response := request(t, handler, http.MethodPost, "/v1/organizations/tenant-1/deployments/"+target.DeploymentID+"/rollback", "tenant-1", string(body), map[string]string{"Idempotency-Key": "rollback-by-deployment"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var rollback runtimev1.DeploymentRef
	if err := json.Unmarshal(response.Body.Bytes(), &rollback); err != nil || rollback.ReleaseID == target.ReleaseID || rollback.DeploymentID == "" {
		t.Fatalf("rollback=%+v err=%v", rollback, err)
	}
}

func TestHandler_RejectsMalformedExpectedEnvironmentRevision(t *testing.T) {
	handler, _, appID, envID := runtimeHandler(t)
	body, _ := json.Marshal(map[string]any{"artifact": testkit.Artifact("a"), "configuration": testkit.Config()})
	path := "/v1/organizations/tenant-1/applications/" + appID + "/environments/" + envID + "/deployments"
	response := request(t, handler, http.MethodPost, path, "tenant-1", string(body), map[string]string{
		"Idempotency-Key": "deploy-invalid-revision", "X-Expected-Environment-Revision": "not-a-number",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandler_EnforcesBodyLimit(t *testing.T) {
	handler, _, _, _ := runtimeHandler(t)
	handler.MaxBodyBytes = 16
	response := request(t, handler, http.MethodPost, "/v1/organizations/tenant-1/applications", "tenant-1", `{"project_id":"project-2","name":"payments"}`, map[string]string{"Idempotency-Key": "large"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
