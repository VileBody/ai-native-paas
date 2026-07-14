package commercebudget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func TestHTTPQuotas_ReserveAndCommitUseTenantPathWithoutBearerFallback(t *testing.T) {
	now := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Authorization") != "" || request.Header.Get("X-Principal-ID") != "" || request.Header.Get("X-Tenant-ID") != "" {
			t.Fatalf("client attempted an application-header credential fallback: %#v", request.Header)
		}
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/organizations/tenant-budget/quota-reservations" || request.Header.Get("Idempotency-Key") != "workspace-command:project:task:command" {
				t.Fatalf("reserve request method=%s path=%s headers=%#v", request.Method, request.URL.Path, request.Header)
			}
			var body commercev1.QuotaRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.TenantID != "tenant-budget" || body.Resource != Resource || body.Quantity != 10 {
				t.Fatalf("reserve body=%+v", body)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(commercev1.QuotaReservation{
				ID: "reservation-1", TenantID: body.TenantID, Resource: body.Resource,
				Quantity: body.Quantity, State: "HELD", CreatedAt: now, UpdatedAt: now,
			})
		case 2:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/organizations/tenant-budget/quota-reservations/reservation-1/commit" {
				t.Fatalf("commit request method=%s path=%s", request.Method, request.URL.Path)
			}
			response.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	quotas, err := NewHTTP(HTTPConfig{BaseURL: server.URL, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := quotas.Reserve(context.Background(), commercev1.QuotaRequest{
		TenantID: "tenant-budget", Resource: Resource, Quantity: 10,
		IdempotencyKey: "workspace-command:project:task:command", At: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := quotas.Commit(context.Background(), reservation.ID); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestNewHTTP_RequiresHTTPSAndInjectedClient(t *testing.T) {
	for _, config := range []HTTPConfig{
		{BaseURL: "http://commerce.example", Client: http.DefaultClient},
		{BaseURL: "https://commerce.example", Client: nil},
		{BaseURL: "https://user:password@commerce.example", Client: http.DefaultClient},
		{BaseURL: "https://commerce.example?token=secret", Client: http.DefaultClient},
	} {
		if _, err := NewHTTP(config); err == nil {
			t.Fatalf("unsafe config accepted: base=%q client_nil=%t", config.BaseURL, config.Client == nil)
		}
	}
	if _, err := NewHTTP(HTTPConfig{BaseURL: "https://commerce.example/" + strings.Repeat("a", 1), Client: http.DefaultClient}); err != nil {
		t.Fatalf("valid HTTPS config rejected: %v", err)
	}
}
