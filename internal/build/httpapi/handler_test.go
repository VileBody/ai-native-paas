package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/httpapi"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
)

func buildAPI() httpapi.Handler {
	return httpapi.Handler{Build: &application.Service{Store: memory.New(), Clock: &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}, IDs: &testkit.IDs{}, Logs: logs.New()}, MaxBodyBytes: 1 << 20}
}
func buildAuth(request *http.Request, tenantID string) {
	request.Header.Set("X-Principal-ID", "agent-1")
	request.Header.Set("X-Tenant-ID", tenantID)
	request.Header.Set("X-Correlation-ID", "task-1")
}
func requestBody() string {
	return `{"source":{"project_id":"project-1","repository_id":"repo-1","branch":"main","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"config":{},"builder_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","run_image_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","platform_version":"v1"}`
}
func requestV2Body() string {
	return `{"source":{"project_id":"project-1","repository_id":"repo-1","branch":"main","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"spec":{"source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","driver":"dockerfile","definition_path":"Dockerfile","platforms":["linux/amd64"],"network_profile":"governed","cache_scope":"project-1","resource_class":"standard","timeout_seconds":900},"builder_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","run_image_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","platform_version":"v2"}`
}
func TestHTTP_BuildHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	buildAPI().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
func TestHTTP_RequestAndGetBuild(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(requestBody()))
	buildAuth(request, "tenant-1")
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result application.RequestBuildResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/organizations/tenant-1/builds/"+result.Build.ID, nil)
	buildAuth(get, "tenant-1")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, get)
	if out.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", out.Code, out.Body.String())
	}
}
func TestHTTP_RequestV2PersistsExplicitBuildSpec(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v2/organizations/tenant-1/builds", strings.NewReader(requestV2Body()))
	buildAuth(request, "tenant-1")
	request.Header.Set("Idempotency-Key", "request-v2")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result application.RequestBuildResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Build.BuildSpec == nil || result.Build.BuildSpecDigest == "" || result.Build.AutoDetectionAllowed {
		t.Fatalf("build=%+v", result.Build)
	}
}
func TestHTTP_BuildTenantIsDerivedFromPath(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(requestBody()))
	buildAuth(request, "other")
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
func TestHTTP_BuildRejectsUnknownFieldsAndDoesNotEchoCause(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(`{"unknown":"do-not-echo-secret"}`))
	buildAuth(request, "tenant-1")
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), "do-not-echo-secret") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
func TestHTTP_CancelBuild(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(requestBody()))
	buildAuth(request, "tenant-1")
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var result application.RequestBuildResult
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	cancel := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds/"+result.Build.ID+"/cancel", nil)
	buildAuth(cancel, "tenant-1")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, cancel)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), "CANCELED") {
		t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
	}
	if _, _, err := handler.Build.GetBuild(context.Background(), "tenant-1", result.Build.ID); err != nil {
		t.Fatal(err)
	}
}
func TestHTTP_RequestBuildRequiresIdempotencyAndCorrelation(t *testing.T) {
	handler := buildAPI()
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(requestBody()))
	buildAuth(request, "tenant-1")
	request.Header.Del("X-Correlation-ID")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	_ = domain.BuildConfig{}
}

func TestHTTP_RequestBodyLimitRejectsValidJSONPrefixWithTrailingBytes(t *testing.T) {
	handler := buildAPI()
	body := requestBody()
	handler.MaxBodyBytes = int64(len(body))
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/builds", strings.NewReader(body+" "))
	buildAuth(request, "tenant-1")
	request.Header.Set("Idempotency-Key", "request-too-large")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"INVALID_ARGUMENT"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
