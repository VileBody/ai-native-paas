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

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/httpapi"
	"github.com/keir-research/ai-native-paas/internal/agent/memory"
	"github.com/keir-research/ai-native-paas/internal/agent/testkit"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
)

type httpFixture struct {
	h   httpapi.Handler
	svc *application.Service
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	clock := &testkit.Clock{T: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	svc := &application.Service{Store: memory.New(), Clock: clock, IDs: ids, Source: testkit.NewSource(), Builds: testkit.NewBuilds(), Runtime: testkit.NewRuntime(), Attachments: testkit.NewAttachments(), Commerce: &testkit.Commerce{Allowed: true}, Operations: testkit.NewOperations(), Logs: &testkit.Logs{}, Usage: &testkit.Usage{}}
	scopes := make([]string, 0, len(agentv1.ToolCatalog()))
	for _, tool := range agentv1.ToolCatalog() {
		scopes = append(scopes, string(agentv1.ScopeForTool(tool)))
	}
	if _, err := svc.RegisterPrincipal(context.Background(), application.RegisterPrincipalCommand{ID: "agent-1", TenantID: "tenant-1", OnBehalfOfUserID: "user-1", Scopes: scopes, CredentialExpiresAt: clock.T.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTask(context.Background(), application.StartTaskCommand{ID: "task-1", TenantID: "tenant-1", AgentID: "agent-1", OnBehalfOfUserID: "user-1", CorrelationID: "corr-1", BudgetPolicy: agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 100, MaxDeployCount: 10, RepairThreshold: 3}}); err != nil {
		t.Fatal(err)
	}
	return &httpFixture{h: httpapi.Handler{Agent: svc, MaxBodyBytes: 1 << 20}, svc: svc}
}

func invokeBody(tool agentv1.Tool, args any, key string) []byte {
	raw, _ := json.Marshal(args)
	body, _ := json.Marshal(agentv1.InvocationRequest{APIVersion: agentv1.APIVersion, SemanticsVersion: agentv1.SemanticsVersion, TenantID: "tenant-1", AgentID: "agent-1", TaskID: "task-1", Tool: tool, Arguments: raw, IdempotencyKey: key, CorrelationID: "corr-1"})
	return body
}
func request(h http.Handler, method, path string, body []byte, tenant, principal, kind string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if tenant != "" {
		r.Header.Set("X-Tenant-ID", tenant)
		r.Header.Set("X-Principal-ID", principal)
		r.Header.Set("X-Principal-Kind", kind)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandler_HealthAndToolCatalogArePublicVersionedAndNoStore(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(f.h, http.MethodGet, "/healthz", nil, "", "", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health=%d headers=%v", w.Code, w.Header())
	}
	w = request(f.h, http.MethodGet, "/mcp/v1/tools", nil, "", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), agentv1.APIVersion) || !strings.Contains(w.Body.String(), string(agentv1.ToolDeploy)) {
		t.Fatalf("catalog=%d %s", w.Code, w.Body.String())
	}
}
func TestHandler_InvokeRequiresAuthenticatedAgent(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k"), "", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_InvokeRejectsBodyTenantMismatch(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k"), "tenant-2", "agent-1", "agent")
	if w.Code != http.StatusForbidden {
		t.Fatalf("code=%d", w.Code)
	}
}
func TestHandler_InvokeStrictlyRejectsUnknownFields(t *testing.T) {
	f := newHTTPFixture(t)
	body := strings.TrimSuffix(string(invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k")), "}") + `,"admin":true}`
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", []byte(body), "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_InvokeCreatesAndReplaysProject(t *testing.T) {
	f := newHTTPFixture(t)
	body := invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "same")
	a := request(f.h, http.MethodPost, "/mcp/v1/invoke", body, "tenant-1", "agent-1", "agent")
	b := request(f.h, http.MethodPost, "/mcp/v1/invoke", body, "tenant-1", "agent-1", "agent")
	if a.Code != http.StatusOK || b.Code != http.StatusOK || !strings.Contains(b.Body.String(), `"replayed":true`) {
		t.Fatalf("a=%d %s b=%d %s", a.Code, a.Body.String(), b.Code, b.Body.String())
	}
}
func TestHandler_BodyLimitIsEnforced(t *testing.T) {
	f := newHTTPFixture(t)
	f.h.MaxBodyBytes = 32
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", bytes.Repeat([]byte("x"), 33), "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", w.Code)
	}
}
func TestHandler_TaskStatusAndAuditAreTenantScoped(t *testing.T) {
	f := newHTTPFixture(t)
	_ = request(f.h, http.MethodPost, "/mcp/v1/invoke", invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k"), "tenant-1", "agent-1", "agent")
	w := request(f.h, http.MethodGet, "/v1/tasks/task-1/status", nil, "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "user-1") {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	w = request(f.h, http.MethodGet, "/v1/tasks/task-1/audit", nil, "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), string(agentv1.ToolCreateProject)) {
		t.Fatalf("audit=%d %s", w.Code, w.Body.String())
	}
	w = request(f.h, http.MethodGet, "/v1/tasks/task-1/status", nil, "tenant-2", "agent-1", "agent")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross tenant code=%d", w.Code)
	}
}
func TestHandler_ApprovalGrantRequiresHumanIdentity(t *testing.T) {
	f := newHTTPFixture(t)
	payload := json.RawMessage(`{"release":"release-1"}`)
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", invokeBody(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: "env-production"}, Payload: payload, TTLSeconds: 600}, "approval"), "tenant-1", "agent-1", "agent")
	var response agentv1.InvocationResponse
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	var view agentv1.ApprovalRequestView
	_ = json.Unmarshal(response.Result.Data, &view)
	w = request(f.h, http.MethodPost, "/v1/approval-requests/"+view.ApprovalRequestID+"/grant", nil, "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent grant code=%d", w.Code)
	}
	w = request(f.h, http.MethodPost, "/v1/approval-requests/"+view.ApprovalRequestID+"/grant", nil, "tenant-1", "approver-1", "user")
	if w.Code != http.StatusCreated || strings.Contains(strings.ToLower(w.Body.String()), "payload\"") {
		t.Fatalf("human grant code=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_ProviderErrorDoesNotLeakCause(t *testing.T) {
	f := newHTTPFixture(t)
	f.svc.Source = &failingSource{}
	w := request(f.h, http.MethodPost, "/mcp/v1/invoke", invokeBody(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k"), "tenant-1", "agent-1", "agent")
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "provider-root-token") {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

type failingSource struct{ testkit.Source }

func (*failingSource) CreateProject(context.Context, string, string, string, string) (application.ProjectRef, error) {
	return application.ProjectRef{}, &application.ProviderError{Message: "provider-root-token", Retryable: true, OperationID: "operation-1"}
}
