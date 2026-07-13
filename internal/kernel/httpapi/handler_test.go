package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
	"github.com/keir-research/ai-native-paas/internal/kernel/memory"
)

func TestPrincipalFromHeaders_IgnoresRequestTenantContext(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Principal-ID", "user_1")
	request.Header.Set("X-Principal-Kind", "USER")
	request.Header.Set("X-Scopes", "kernel:*, source:read")
	request.Header.Set("X-Tenant-Context", "attacker-controlled-tenant")

	principal, err := principalFromHeaders(request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.TenantID != "" {
		t.Fatalf("request-provided tenant was trusted: %q", principal.TenantID)
	}
	if principal.PrincipalID != "user_1" || principal.Kind != kernelv1.PrincipalKindUser || len(principal.Scopes) != 2 {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestPrincipalFromHeaders_RejectsMissingOrInvalidPrincipal(t *testing.T) {
	missing := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := principalFromHeaders(missing); kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("missing principal error = %v", err)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/", nil)
	invalid.Header.Set("X-Principal-ID", "user_1")
	invalid.Header.Set("X-Principal-Kind", "robot")
	if _, err := principalFromHeaders(invalid); kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("invalid principal error = %v", err)
	}
}

func TestDecodeJSON_RejectsMalformedTrailingUnknownAndOversizedBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"name":`},
		{name: "trailing", body: `{"name":"Acme"}{"name":"Beta"}`},
		{name: "unknown", body: `{"name":"Acme","admin":true}`},
		{name: "oversized", body: `{"name":"` + strings.Repeat("x", maxRequestBody) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			var destination struct {
				Name string `json:"name"`
			}
			if err := decodeJSON(recorder, request, &destination); kernel.ErrorCode(err) != kernelv1.CodeInvalidArgument {
				t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
			}
		})
	}
}

func TestDecodeJSON_AcceptsExactlyOneStrictObject(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Acme"}`))
	recorder := httptest.NewRecorder()
	var destination struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(recorder, request, &destination); err != nil {
		t.Fatal(err)
	}
	if destination.Name != "Acme" {
		t.Fatalf("name = %q", destination.Name)
	}
}

func TestRecoveryMiddleware_ReturnsSanitizedInternalError(t *testing.T) {
	handler := recoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("postgres://admin:secret@internal-db/platform")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	var public kernelv1.PublicError
	if err := json.Unmarshal(recorder.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.Code != kernelv1.CodeInternal || !public.Retryable {
		t.Fatalf("public error = %+v", public)
	}
	lower := strings.ToLower(recorder.Body.String())
	if strings.Contains(lower, "secret") || strings.Contains(lower, "internal-db") || strings.Contains(lower, "postgres://") {
		t.Fatalf("panic detail leaked: %s", recorder.Body.String())
	}
}

func TestNewHandler_ValidatesDependenciesAndHealthEndpoint(t *testing.T) {
	ids := kernel.NewSequenceIDGenerator()
	if _, err := NewHandler(nil, ids); err == nil {
		t.Fatal("nil service was accepted")
	}
	service, err := kernel.NewService(memory.NewStore(), kernel.NewFixedClock(time.Unix(0, 0)), ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHandler(service, nil); err == nil {
		t.Fatal("nil id generator was accepted")
	}
	handler, err := NewHandler(service, ids)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"status":"ok"`)) {
		t.Fatalf("health response = %d %s", recorder.Code, recorder.Body.String())
	}
}
