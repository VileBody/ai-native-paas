package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/httpapi"
)

func TestAttachments_HTTPIdentityCannotOverrideTenantInBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/services", strings.NewReader(`{
		"tenant_id":"tenant-2","name":"primary","plan_id":"pg-small"
	}`))
	request.Header.Set("X-Principal-ID", "user-1")
	request.Header.Set("X-Tenant-ID", "tenant-1")
	request.Header.Set("Idempotency-Key", "provision-1")
	response := httptest.NewRecorder()

	(httpapi.Handler{Attachments: &application.Service{}}).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid json") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAttachments_ProviderCallbackRequiresAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/services/service-1/reconcile", nil)
	response := httptest.NewRecorder()

	(httpapi.Handler{Attachments: &application.Service{}}).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "principal headers required") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
