package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
	"github.com/keir-research/ai-native-paas/internal/kernel/httpapi"
	"github.com/keir-research/ai-native-paas/internal/kernel/memory"
)

type apiHarness struct {
	server  *httptest.Server
	service *kernel.Service
}

func newAPIHarness(t *testing.T) apiHarness {
	t.Helper()
	ids := kernel.NewSequenceIDGenerator()
	service, err := kernel.NewService(
		memory.NewStore(),
		kernel.NewFixedClock(time.Date(2026, 7, 12, 14, 0, 0, 0, time.UTC)),
		ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(service, ids)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return apiHarness{server: server, service: service}
}

type apiResponse struct {
	status int
	body   []byte
}

func (h apiHarness) request(t *testing.T, method, path string, body any, principal, scopes, idempotencyKey string) apiResponse {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, h.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-Principal-ID", principal)
	request.Header.Set("X-Scopes", scopes)
	request.Header.Set("X-Correlation-ID", "cor-"+strings.ReplaceAll(idempotencyKey, " ", "-"))
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return apiResponse{status: response.StatusCode, body: responseBody}
}

func decode[T any](t *testing.T, response apiResponse) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.body, &value); err != nil {
		t.Fatalf("decode response %d %s: %v", response.status, response.body, err)
	}
	return value
}

func TestAcceptance_OwnerCreatesOrganizationAndInvitesDeveloper(t *testing.T) {
	h := newAPIHarness(t)

	create := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme"}, "owner", "kernel:*", "create-acme")
	if create.status != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", create.status, create.body)
	}
	created := decode[kernel.CreateOrganizationResult](t, create)
	if created.Operation.State != kernelv1.OperationSucceeded || created.Organization.Slug != "acme" {
		t.Fatalf("created = %+v", created)
	}
	organizationID := created.Organization.OrganizationID

	operation := h.request(t, http.MethodGet, "/v1/operations/"+string(created.Operation.OperationID), nil, "owner", "kernel:*", "read-operation")
	if operation.status != http.StatusOK {
		t.Fatalf("get operation status=%d body=%s", operation.status, operation.body)
	}
	operationSnapshot := decode[kernelv1.OperationSnapshot](t, operation)
	if operationSnapshot.State != kernelv1.OperationSucceeded {
		t.Fatalf("operation state=%s", operationSnapshot.State)
	}

	invite := h.request(t, http.MethodPost, "/v1/organizations/"+string(organizationID)+"/invitations", map[string]any{
		"principal_id": "developer",
		"role":         "developer",
	}, "owner", "kernel:*", "invite-developer")
	if invite.status != http.StatusAccepted {
		t.Fatalf("invite status=%d body=%s", invite.status, invite.body)
	}

	beforeAcceptance := h.request(t, http.MethodGet, "/v1/organizations/"+string(organizationID), nil, "developer", "kernel:*", "developer-read-before")
	if beforeAcceptance.status != http.StatusForbidden {
		t.Fatalf("invitation granted access before acceptance: status=%d body=%s", beforeAcceptance.status, beforeAcceptance.body)
	}

	accept := h.request(t, http.MethodPost, "/v1/organizations/"+string(organizationID)+"/invitations/accept", nil, "developer", "kernel:*", "accept-developer")
	if accept.status != http.StatusAccepted {
		t.Fatalf("accept status=%d body=%s", accept.status, accept.body)
	}

	afterAcceptance := h.request(t, http.MethodGet, "/v1/organizations/"+string(organizationID), nil, "developer", "kernel:*", "developer-read-after")
	if afterAcceptance.status != http.StatusOK {
		t.Fatalf("developer read status=%d body=%s", afterAcceptance.status, afterAcceptance.body)
	}
	organization := decode[kernel.OrganizationSnapshot](t, afterAcceptance)
	developerActive := false
	for _, membership := range organization.Memberships {
		if membership.PrincipalID == "developer" && membership.Role == kernel.RoleDeveloper && membership.State == kernel.MembershipActive {
			developerActive = true
		}
	}
	if !developerActive {
		t.Fatalf("active developer membership not found: %+v", organization.Memberships)
	}

	manageOwner := h.request(t, http.MethodPatch, "/v1/organizations/"+string(organizationID)+"/members/owner/role", map[string]any{"role": "viewer"}, "developer", "kernel:*", "developer-manages-owner")
	if manageOwner.status != http.StatusForbidden {
		t.Fatalf("developer managed owner: status=%d body=%s", manageOwner.status, manageOwner.body)
	}

	audit := h.request(t, http.MethodGet, "/v1/organizations/"+string(organizationID)+"/audit", nil, "owner", "kernel:*", "owner-audit")
	if audit.status != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", audit.status, audit.body)
	}
	auditResponse := decode[struct {
		Items []kernelv1.AuditEnvelope `json:"items"`
	}](t, audit)
	required := map[string]bool{
		kernel.ActionOrganizationCreate: false,
		kernel.ActionMembershipInvite:   false,
		kernel.ActionMembershipAccept:   false,
		kernel.ActionOrganizationRead:   false,
		kernel.ActionMembershipManage:   false,
	}
	deniedFound := false
	for _, record := range auditResponse.Items {
		if _, exists := required[record.Action]; exists {
			required[record.Action] = true
		}
		if record.Outcome == kernelv1.AuditOutcomeDenied {
			deniedFound = true
		}
	}
	for action, found := range required {
		if !found {
			t.Errorf("audit action %q not found", action)
		}
	}
	if !deniedFound {
		t.Error("audit does not contain denied authorization")
	}

	replay := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme"}, "owner", "kernel:*", "create-acme")
	if replay.status != http.StatusAccepted {
		t.Fatalf("idempotent replay status=%d body=%s", replay.status, replay.body)
	}
	replayed := decode[kernel.CreateOrganizationResult](t, replay)
	if replayed.Organization.OrganizationID != organizationID || replayed.Operation.OperationID != created.Operation.OperationID {
		t.Fatalf("idempotent replay created a new side effect: %+v", replayed)
	}
}

func TestAcceptance_IdempotencyConflictAndSanitizedErrors(t *testing.T) {
	h := newAPIHarness(t)
	first := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme"}, "owner", "kernel:*", "same-key")
	if first.status != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.status, first.body)
	}
	second := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Beta"}, "owner", "kernel:*", "same-key")
	if second.status != http.StatusConflict {
		t.Fatalf("second status=%d body=%s", second.status, second.body)
	}
	public := decode[kernelv1.PublicError](t, second)
	if public.Code != kernelv1.CodeIdempotencyConflict || public.Retryable {
		t.Fatalf("public error = %+v", public)
	}
	lower := strings.ToLower(string(second.body))
	for _, forbidden := range []string{"sql", "stack", "internal hostname", "provider token"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("internal detail %q leaked in %s", forbidden, second.body)
		}
	}
}

func TestAcceptance_MutationRequiresIdempotencyKey(t *testing.T) {
	h := newAPIHarness(t)
	response := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme"}, "owner", "kernel:*", "")
	if response.status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.status, response.body)
	}
	public := decode[kernelv1.PublicError](t, response)
	if public.Code != kernelv1.CodeInvalidArgument {
		t.Fatalf("error = %+v", public)
	}
}

func TestAcceptance_UnknownJSONFieldsAreRejected(t *testing.T) {
	h := newAPIHarness(t)
	response := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Acme", "admin": true}, "owner", "kernel:*", "unknown-field")
	if response.status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.status, response.body)
	}
}

func TestAcceptance_MembershipLifecycleAndOperationCancellationRoutes(t *testing.T) {
	h := newAPIHarness(t)
	create := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Lifecycle API"}, "owner", "kernel:*", "route-create")
	if create.status != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", create.status, create.body)
	}
	created := decode[kernel.CreateOrganizationResult](t, create)
	organizationID := created.Organization.OrganizationID
	base := "/v1/organizations/" + string(organizationID)

	lastOwnerDelete := h.request(t, http.MethodDelete, base+"/members/owner", nil, "owner", "kernel:*", "route-delete-last-owner")
	if lastOwnerDelete.status != http.StatusConflict {
		t.Fatalf("last owner delete status=%d body=%s", lastOwnerDelete.status, lastOwnerDelete.body)
	}
	lastOwnerError := decode[kernelv1.PublicError](t, lastOwnerDelete)
	if lastOwnerError.Code != kernelv1.CodeLastOwner {
		t.Fatalf("last owner error=%+v", lastOwnerError)
	}

	invite := h.request(t, http.MethodPost, base+"/invitations", map[string]any{
		"principal_id": "member", "role": "viewer",
	}, "owner", "kernel:*", "route-invite")
	if invite.status != http.StatusAccepted {
		t.Fatalf("invite status=%d body=%s", invite.status, invite.body)
	}
	accept := h.request(t, http.MethodPost, base+"/invitations/accept", nil, "member", "kernel:*", "route-accept")
	if accept.status != http.StatusAccepted {
		t.Fatalf("accept status=%d body=%s", accept.status, accept.body)
	}
	change := h.request(t, http.MethodPatch, base+"/members/member/role", map[string]any{"role": "developer"}, "owner", "kernel:*", "route-role")
	if change.status != http.StatusAccepted {
		t.Fatalf("change role status=%d body=%s", change.status, change.body)
	}
	suspend := h.request(t, http.MethodPost, base+"/members/member/suspend", nil, "owner", "kernel:*", "route-suspend")
	if suspend.status != http.StatusAccepted {
		t.Fatalf("suspend status=%d body=%s", suspend.status, suspend.body)
	}
	removed := h.request(t, http.MethodDelete, base+"/members/member", nil, "owner", "kernel:*", "route-remove")
	if removed.status != http.StatusAccepted {
		t.Fatalf("remove status=%d body=%s", removed.status, removed.body)
	}
	read := h.request(t, http.MethodGet, base, nil, "owner", "kernel:*", "route-read")
	if read.status != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.status, read.body)
	}
	snapshot := decode[kernel.OrganizationSnapshot](t, read)
	found := false
	for _, membership := range snapshot.Memberships {
		if membership.PrincipalID == "member" {
			found = true
			if membership.Role != kernel.RoleDeveloper || membership.State != kernel.MembershipRemoved {
				t.Fatalf("member snapshot=%+v", membership)
			}
		}
	}
	if !found {
		t.Fatal("member not found after lifecycle")
	}

	operation, err := h.service.Start(context.Background(), kernelv1.CommandMeta{
		TenantID:       organizationID,
		Principal:      kernelv1.PrincipalContext{PrincipalID: "owner", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}},
		CorrelationID:  "cor_route_operation",
		IdempotencyKey: "route-start-operation",
		Command:        "AcceptanceLongTask",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel := h.request(t, http.MethodPost, "/v1/operations/"+string(operation.OperationID)+"/cancel", nil, "owner", "kernel:*", "route-cancel")
	if cancel.status != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancel.status, cancel.body)
	}
	cancelResult := decode[kernel.CommandResult](t, cancel)
	if cancelResult.Operation.State != kernelv1.OperationCanceled {
		t.Fatalf("cancel result=%+v", cancelResult)
	}
	getOperation := h.request(t, http.MethodGet, "/v1/operations/"+string(operation.OperationID), nil, "owner", "kernel:*", "route-get-canceled")
	if getOperation.status != http.StatusOK {
		t.Fatalf("get canceled status=%d body=%s", getOperation.status, getOperation.body)
	}
	operationSnapshot := decode[kernelv1.OperationSnapshot](t, getOperation)
	if operationSnapshot.State != kernelv1.OperationCanceled {
		t.Fatalf("operation state=%s", operationSnapshot.State)
	}

	health := h.request(t, http.MethodGet, "/healthz", nil, "", "", "")
	if health.status != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.status, health.body)
	}
}

func TestAcceptance_RequestTenantHeaderCannotOverrideResourceDerivedTenant(t *testing.T) {
	h := newAPIHarness(t)
	create := h.request(t, http.MethodPost, "/v1/organizations", map[string]any{"name": "Header Safe"}, "owner", "kernel:*", "tenant-header-create")
	created := decode[kernel.CreateOrganizationResult](t, create)
	request, err := http.NewRequest(http.MethodGet, h.server.URL+"/v1/organizations/"+string(created.Organization.OrganizationID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Principal-ID", "owner")
	request.Header.Set("X-Scopes", "kernel:*")
	request.Header.Set("X-Tenant-Context", "attacker-controlled-tenant")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tenant header influenced authorization: status=%d body=%s", response.StatusCode, body)
	}
}
