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

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	"github.com/keir-research/ai-native-paas/internal/commerce/httpapi"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	"github.com/keir-research/ai-native-paas/internal/commerce/testkit"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

var httpBase = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

type httpFixture struct {
	handler http.Handler
	svc     *application.Service
	period  domain.BillingPeriod
	clock   *testkit.Clock
}

func httpPlanSpec() commercev1.PlanSpec {
	return commercev1.PlanSpec{
		Currency: "EUR",
		Features: map[string]bool{"deploy": true},
		Quotas:   map[string]int64{"runtime.units": 2},
		Prices:   map[commercev1.Meter]commercev1.Price{commercev1.MeterRuntimeUnitSeconds: {MinorUnits: 1, PerQuantity: 3600}},
		Included: map[commercev1.Meter]int64{},
	}
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	store := memory.New()
	clock := &testkit.Clock{T: httpBase.Add(time.Hour)}
	svc := &application.Service{Store: store, Clock: clock, IDs: &testkit.IDs{}, Ownership: application.AllowAllOwnership{}, DriftAlertThreshold: 30}
	ctx := context.Background()
	if _, err := svc.CreatePlanDefinition(ctx, application.CreatePlanDefinitionCommand{ID: "plan", Name: "Developer"}); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlanVersion(ctx, application.CreatePlanVersionCommand{ID: "plan-v1", DefinitionID: "plan", PolicyVersion: "policy-v1", Number: 1, Spec: httpPlanSpec(), EffectiveFrom: httpBase})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = svc.ActivatePlanVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, period, err := svc.StartSubscription(ctx, application.StartSubscriptionCommand{ID: "sub-1", TenantID: "tenant-1", PlanVersionID: plan.ID, PeriodID: "period-1", State: domain.SubscriptionActive, PeriodStart: httpBase, PeriodEnd: httpBase.Add(31 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return &httpFixture{handler: httpapi.Handler{Commerce: svc, MaxBodyBytes: 1 << 20}, svc: svc, period: period, clock: clock}
}

func request(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func tenantHeaders() map[string]string {
	return map[string]string{"X-Principal-ID": "user-1", "X-Tenant-ID": "tenant-1"}
}
func adminHeaders() map[string]string {
	return map[string]string{"X-Principal-ID": "admin-1", "X-Principal-Role": "platform-admin"}
}

func TestHandler_HealthDoesNotRequireAuthentication(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodGet, "/healthz", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}
func TestHandler_AdminRouteRequiresPlatformAdminRole(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/admin/plan-definitions", `{"id":"x","name":"X"}`, map[string]string{"X-Principal-ID": "user"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_TenantPathMustMatchPrincipal(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-2/entitlements/check", `{"feature":"deploy"}`, tenantHeaders())
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_RejectsUnknownJSONFields(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/entitlements/check", `{"feature":"deploy","unknown":true}`, tenantHeaders())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_RejectsOversizedBody(t *testing.T) {
	f := newHTTPFixture(t)
	h := httpapi.Handler{Commerce: f.svc, MaxBodyBytes: 16}
	w := request(t, h, http.MethodPost, "/v1/organizations/tenant-1/entitlements/check", `{"feature":"`+strings.Repeat("x", 40)+`"}`, tenantHeaders())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_EntitlementCheckUsesPathTenant(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/entitlements/check", `{"tenant_id":"attacker","feature":"deploy","at":"2026-07-01T01:00:00Z"}`, tenantHeaders())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestHandler_QuotaReserveCommitAndRelease(t *testing.T) {
	f := newHTTPFixture(t)
	body := `{"resource":"runtime.units","quantity":1,"expires_at":"2026-07-01T02:00:00Z","at":"2026-07-01T01:00:00Z"}`
	headers := tenantHeaders()
	headers["Idempotency-Key"] = "http-quota"
	created := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/quota-reservations", body, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	var q commercev1.QuotaReservation
	if err := json.NewDecoder(created.Body).Decode(&q); err != nil {
		t.Fatal(err)
	}
	committed := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/quota-reservations/"+q.ID+"/commit", `{}`, tenantHeaders())
	if committed.Code != http.StatusOK {
		t.Fatalf("commit status=%d body=%s", committed.Code, committed.Body.String())
	}
	released := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/quota-reservations/"+q.ID+"/release", `{}`, tenantHeaders())
	if released.Code != http.StatusOK {
		t.Fatalf("release status=%d body=%s", released.Code, released.Body.String())
	}
}
func TestHandler_UsageAndInvoicePreview(t *testing.T) {
	f := newHTTPFixture(t)
	body := `{"period_id":"period-1","resource_type":"application","resource_id":"app-1","meter":"runtime.unit_seconds","quantity":3600,"occurred_at":"2026-07-01T01:00:00Z","window_start":"2026-07-01T00:00:00Z","window_end":"2026-07-01T01:00:00Z"}`
	headers := tenantHeaders()
	headers["Idempotency-Key"] = "http-usage"
	first := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/usage", body, headers)
	second := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/usage", body, headers)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("first=%d second=%d", first.Code, second.Code)
	}
	preview := request(t, f.handler, http.MethodGet, "/v1/organizations/tenant-1/billing-periods/period-1/invoice-preview", "", tenantHeaders())
	if preview.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", preview.Code, preview.Body.String())
	}
	var value commercev1.InvoicePreview
	if err := json.NewDecoder(preview.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value.TotalMinorUnits != 1 || len(value.Lines) != 1 {
		t.Fatalf("preview=%+v", value)
	}
}
func TestHandler_SuspensionEmitsRetainDataIntent(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/commercial-state/suspend", `{}`, tenantHeaders())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var intent commercev1.RuntimeIntent
	if err := json.NewDecoder(w.Body).Decode(&intent); err != nil {
		t.Fatal(err)
	}
	if intent.Action != "suspend" || !intent.RetainManagedServices {
		t.Fatalf("intent=%+v", intent)
	}
}
func TestHandler_AdminCanCreateVersionedPlanAndSubscription(t *testing.T) {
	store := memory.New()
	svc := &application.Service{Store: store, Clock: &testkit.Clock{T: httpBase}, IDs: &testkit.IDs{}, Ownership: application.AllowAllOwnership{}}
	h := httpapi.Handler{Commerce: svc}
	def := request(t, h, http.MethodPost, "/v1/admin/plan-definitions", `{"id":"business","name":"Business"}`, adminHeaders())
	if def.Code != http.StatusCreated {
		t.Fatalf("definition status=%d body=%s", def.Code, def.Body.String())
	}
	command := map[string]any{
		"id": "business-v1", "definition_id": "business", "policy_version": "policy-business-v1",
		"number": 1, "spec": httpPlanSpec(), "effective_from": httpBase,
	}
	raw, _ := json.Marshal(command)
	version := request(t, h, http.MethodPost, "/v1/admin/plan-versions", string(raw), adminHeaders())
	if version.Code != http.StatusCreated {
		t.Fatalf("version status=%d body=%s", version.Code, version.Body.String())
	}
	active := request(t, h, http.MethodPost, "/v1/admin/plan-versions/business-v1/activate", `{}`, adminHeaders())
	if active.Code != http.StatusOK {
		t.Fatalf("activate status=%d body=%s", active.Code, active.Body.String())
	}
	subCommand := map[string]any{
		"id": "sub-business", "tenant_id": "tenant-business", "plan_version_id": "business-v1",
		"period_id": "period-business", "state": domain.SubscriptionActive,
		"period_start": httpBase, "period_end": httpBase.Add(30 * 24 * time.Hour),
	}
	raw, _ = json.Marshal(subCommand)
	sub := request(t, h, http.MethodPost, "/v1/admin/subscriptions", string(raw), adminHeaders())
	if sub.Code != http.StatusCreated {
		t.Fatalf("subscription status=%d body=%s", sub.Code, sub.Body.String())
	}
}
func TestHandler_ErrorEnvelopeDoesNotExposeInternalCause(t *testing.T) {
	f := newHTTPFixture(t)
	w := request(t, f.handler, http.MethodPost, "/v1/organizations/tenant-1/quota-reservations", `{"resource":"runtime.units"}`, tenantHeaders())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("cause")) || bytes.Contains(w.Body.Bytes(), []byte("stack")) {
		t.Fatalf("leaky body=%s", w.Body.String())
	}
}
