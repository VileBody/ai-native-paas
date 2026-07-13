package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/httpapi"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func apiSetup() (httpapi.Handler, *application.Service, *testkit.Provider, *testkit.Clock) {
	store := memory.New()
	p := testkit.NewProvider()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	source := &application.Service{Store: store, Provider: p, Clock: clock, IDs: ids}
	hooks := &application.WebhookService{Store: store, Provider: p, Verifier: sourcehook.Verifier{Secret: []byte("secret")}, Normalizer: sourcehook.Normalizer{}, Clock: clock, IDs: ids}
	return httpapi.Handler{Source: source, Webhooks: hooks, MaxBodyBytes: 1 << 20}, source, p, clock
}
func auth(req *http.Request, tenant string) {
	req.Header.Set("X-Principal-ID", "u1")
	req.Header.Set("X-Tenant-ID", tenant)
}
func TestHTTP_CreateProjectAndRead(t *testing.T) {
	h, _, _, _ := apiSetup()
	body := bytes.NewBufferString(`{"name":"Booking","provider_namespace_id":7}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/t1/projects", body)
	auth(req, "t1")
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result application.CreateProjectResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/organizations/t1/projects/"+result.Project.ID, nil)
	auth(get, "t1")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, get)
	if out.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
	}
}
func TestHTTP_TenantComesFromResourcePathNotBody(t *testing.T) {
	h, _, _, _ := apiSetup()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/t1/projects", strings.NewReader(`{"name":"X","provider_namespace_id":7}`))
	auth(req, "other")
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestHTTP_RejectsUnknownJSONFields(t *testing.T) {
	h, _, _, _ := apiSetup()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/t1/projects", strings.NewReader(`{"name":"X","provider_namespace_id":7,"tenant_id":"other"}`))
	auth(req, "t1")
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestHTTP_WebhookVerifiesExactRawBytes(t *testing.T) {
	h, source, p, clock := apiSetup()
	created, err := source.CreateProject(context.Background(), application.CreateProjectCommand{TenantID: "t1", ActorID: "u1", Name: "Booking", ProviderNamespaceID: 7, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := source.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	p.SetHead(repo.ProviderProjectID, "main", head)
	raw := []byte(fmt.Sprintf("{\n  \"object_kind\": \"push\",\n  \"before\": \"%s\",\n  \"after\": \"%s\",\n  \"ref\": \"refs/heads/main\",\n  \"project\": {\"id\": %d}\n}", strings.Repeat("a", 40), head, repo.ProviderProjectID))
	ts, sig := sourcehook.Sign([]byte("secret"), "evt", clock.Now(), raw)
	req := httptest.NewRequest(http.MethodPost, "/hooks/gitlab/t1", bytes.NewReader(raw))
	req.Header.Set("webhook-id", "evt")
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", sig)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestHTTP_WebhookRejectsSignatureForReencodedBody(t *testing.T) {
	h, _, _, clock := apiSetup()
	signed := []byte(`{"object_kind":"push"}`)
	delivered := []byte(`{ "object_kind": "push" }`)
	ts, sig := sourcehook.Sign([]byte("secret"), "evt", clock.Now(), signed)
	req := httptest.NewRequest(http.MethodPost, "/hooks/gitlab/t1", bytes.NewReader(delivered))
	req.Header.Set("webhook-id", "evt")
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", sig)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		b, _ := io.ReadAll(rec.Body)
		t.Fatalf("status=%d body=%s", rec.Code, b)
	}
}
func TestHTTP_WebhookBodyLimit(t *testing.T) {
	h, _, _, _ := apiSetup()
	h.MaxBodyBytes = 8
	req := httptest.NewRequest(http.MethodPost, "/hooks/gitlab/t1", strings.NewReader(strings.Repeat("x", 100)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}
func TestHTTP_StablePublicErrorDoesNotExposeCause(t *testing.T) {
	h, _, _, _ := apiSetup()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/t1/projects", strings.NewReader(`not-json-secret-value`))
	auth(req, "t1")
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "not-json-secret-value") {
		t.Fatalf("cause leaked: %s", rec.Body.String())
	}
}
