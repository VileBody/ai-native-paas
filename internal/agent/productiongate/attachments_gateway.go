package productiongate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	agentapp "github.com/keir-research/ai-native-paas/internal/agent/application"
	agentdomain "github.com/keir-research/ai-native-paas/internal/agent/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

// HTTPAttachments bridges the Agent MCP facade to attachments-api. An empty
// URL deliberately preserves the production fail-closed adapters in
// gateways.go. Secret values are only ever written to the request body and no
// upstream response or error message is reflected back to the caller.
type HTTPAttachments struct {
	BaseURL     string
	PrincipalID string
	HTTPClient  *http.Client
}

var _ attachmentsv1.Service = (*HTTPAttachments)(nil)

func NewHTTPAttachments(baseURL, principalID string) *HTTPAttachments {
	return &HTTPAttachments{
		BaseURL:     baseURL,
		PrincipalID: principalID,
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *HTTPAttachments) SetSecret(ctx context.Context, request attachmentsv1.SetSecretRequest) (attachmentsv1.SecretMetadata, error) {
	if !a.configured() {
		return (Attachments{}).SetSecret(ctx, request)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.Name = strings.TrimSpace(request.Name)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !attachmentPathScope(request.TenantID, request.ApplicationID, request.EnvironmentID) || request.Name == "" || request.Value == "" || request.IdempotencyKey == "" {
		return attachmentsv1.SecretMetadata{}, attachmentArgumentError()
	}
	body := struct {
		Name  string                    `json:"name"`
		Scope attachmentsv1.SecretScope `json:"scope"`
		Phase attachmentsv1.SecretPhase `json:"phase"`
		Value string                    `json:"value"`
	}{
		Name: request.Name, Scope: attachmentsv1.SecretScopeRuntime,
		Phase: attachmentsv1.SecretPhaseRuntime, Value: request.Value,
	}
	var response struct {
		Secret attachmentsv1.SecretMetadata `json:"secret"`
	}
	path := a.environmentPath(request.TenantID, request.ApplicationID, request.EnvironmentID, "secrets")
	if err := a.do(ctx, http.MethodPost, path, request.TenantID, request.IdempotencyKey, body, &response); err != nil {
		return attachmentsv1.SecretMetadata{}, err
	}
	if response.Secret.Name != request.Name || response.Secret.Validate() != nil {
		return attachmentsv1.SecretMetadata{}, unavailable("attachments_gateway")
	}
	return response.Secret, nil
}

func (a *HTTPAttachments) ListSecretMetadata(ctx context.Context, tenant, applicationID, environmentID string) ([]attachmentsv1.SecretMetadata, error) {
	if !a.configured() {
		return (Attachments{}).ListSecretMetadata(ctx, tenant, applicationID, environmentID)
	}
	tenant = strings.TrimSpace(tenant)
	applicationID = strings.TrimSpace(applicationID)
	environmentID = strings.TrimSpace(environmentID)
	if !attachmentPathScope(tenant, applicationID, environmentID) {
		return nil, attachmentArgumentError()
	}
	var response struct {
		Secrets []attachmentsv1.SecretMetadata `json:"secrets"`
	}
	if err := a.do(ctx, http.MethodGet, a.environmentPath(tenant, applicationID, environmentID, "secrets"), tenant, "", nil, &response); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(response.Secrets))
	for _, metadata := range response.Secrets {
		if metadata.Validate() != nil {
			return nil, unavailable("attachments_gateway")
		}
		key := metadata.Name + "\x00" + string(metadata.Scope)
		if _, duplicate := seen[key]; duplicate {
			return nil, unavailable("attachments_gateway")
		}
		seen[key] = struct{}{}
	}
	return response.Secrets, nil
}

func (a *HTTPAttachments) Provision(ctx context.Context, request attachmentsv1.ServiceRequest) (attachmentsv1.ServiceInstanceRef, error) {
	if !a.configured() {
		return (Attachments{}).Provision(ctx, request)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ServiceType = strings.ToLower(strings.TrimSpace(request.ServiceType))
	request.Plan = strings.TrimSpace(request.Plan)
	request.Name = strings.TrimSpace(request.Name)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !attachmentPathScope(request.TenantID) || request.Plan == "" || request.Name == "" || request.IdempotencyKey == "" {
		return attachmentsv1.ServiceInstanceRef{}, attachmentArgumentError()
	}
	body := struct {
		Name   string `json:"name"`
		PlanID string `json:"plan_id"`
	}{Name: request.Name, PlanID: request.Plan}
	var response attachmentServiceResponse
	path := "/v1/organizations/" + request.TenantID + "/services"
	if err := a.do(ctx, http.MethodPost, path, request.TenantID, request.IdempotencyKey, body, &response); err != nil {
		return attachmentsv1.ServiceInstanceRef{}, err
	}
	if response.ID == "" || response.TenantID != request.TenantID || response.PlanID != request.Plan || response.State == "" || (request.ServiceType != "" && string(response.Type) != request.ServiceType) {
		return attachmentsv1.ServiceInstanceRef{}, unavailable("attachments_gateway")
	}
	return attachmentsv1.ServiceInstanceRef{
		ServiceInstanceID: response.ID, InstanceID: response.ID, TenantID: response.TenantID,
		PlanID: response.PlanID, Type: response.Type, State: response.State,
	}, nil
}

func (a *HTTPAttachments) Bind(ctx context.Context, request attachmentsv1.BindRequest) (attachmentsv1.BindingRef, error) {
	if !a.configured() {
		return (Attachments{}).Bind(ctx, request)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ServiceInstanceID = strings.TrimSpace(request.ServiceInstanceID)
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !attachmentPathScope(request.TenantID, request.ApplicationID, request.EnvironmentID, request.ServiceInstanceID) || request.IdempotencyKey == "" {
		return attachmentsv1.BindingRef{}, attachmentArgumentError()
	}
	body := struct {
		InstanceID   string   `json:"instance_id"`
		Capabilities []string `json:"capabilities"`
	}{InstanceID: request.ServiceInstanceID, Capabilities: []string{"read"}}
	var response struct {
		Binding struct {
			ID            string `json:"id"`
			TenantID      string `json:"tenant_id"`
			ApplicationID string `json:"application_id"`
			EnvironmentID string `json:"environment_id"`
			InstanceID    string `json:"instance_id"`
			State         string `json:"state"`
		} `json:"binding"`
		Snapshot struct {
			SnapshotID    string `json:"snapshot_id"`
			EnvironmentID string `json:"environment_id"`
		} `json:"attachment_snapshot"`
	}
	path := a.environmentPath(request.TenantID, request.ApplicationID, request.EnvironmentID, "bindings")
	if err := a.do(ctx, http.MethodPost, path, request.TenantID, request.IdempotencyKey, body, &response); err != nil {
		return attachmentsv1.BindingRef{}, err
	}
	binding := response.Binding
	if binding.ID == "" || binding.State == "" || binding.TenantID != request.TenantID || binding.ApplicationID != request.ApplicationID || binding.EnvironmentID != request.EnvironmentID || binding.InstanceID != request.ServiceInstanceID || response.Snapshot.SnapshotID == "" || response.Snapshot.EnvironmentID != request.EnvironmentID {
		return attachmentsv1.BindingRef{}, unavailable("attachments_gateway")
	}
	return attachmentsv1.BindingRef{BindingID: binding.ID, SnapshotID: response.Snapshot.SnapshotID, State: binding.State}, nil
}

func (a *HTTPAttachments) AddDomain(ctx context.Context, request attachmentsv1.DomainRequest) (attachmentsv1.DomainRef, error) {
	if !a.configured() {
		return (Attachments{}).AddDomain(ctx, request)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.Hostname = strings.TrimSpace(request.Hostname)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if !attachmentPathScope(request.TenantID, request.ApplicationID, request.EnvironmentID) || request.IdempotencyKey == "" {
		return attachmentsv1.DomainRef{}, attachmentArgumentError()
	}
	body := struct {
		Hostname string `json:"hostname,omitempty"`
	}{Hostname: request.Hostname}
	var response struct {
		ID            string `json:"id"`
		TenantID      string `json:"tenant_id"`
		ApplicationID string `json:"application_id"`
		EnvironmentID string `json:"environment_id"`
		Hostname      string `json:"hostname"`
		State         string `json:"state"`
	}
	path := a.environmentPath(request.TenantID, request.ApplicationID, request.EnvironmentID, "domains")
	if err := a.do(ctx, http.MethodPost, path, request.TenantID, request.IdempotencyKey, body, &response); err != nil {
		return attachmentsv1.DomainRef{}, err
	}
	if response.ID == "" || response.Hostname == "" || response.State == "" || response.TenantID != request.TenantID || response.ApplicationID != request.ApplicationID || response.EnvironmentID != request.EnvironmentID {
		return attachmentsv1.DomainRef{}, unavailable("attachments_gateway")
	}
	if request.Hostname != "" && canonicalHostname(response.Hostname) != canonicalHostname(request.Hostname) {
		return attachmentsv1.DomainRef{}, unavailable("attachments_gateway")
	}
	return attachmentsv1.DomainRef{DomainClaimID: response.ID, Hostname: response.Hostname, State: response.State}, nil
}

type attachmentServiceResponse struct {
	ID       string                    `json:"id"`
	TenantID string                    `json:"tenant_id"`
	PlanID   string                    `json:"plan_id"`
	Type     attachmentsv1.ServiceType `json:"type"`
	State    string                    `json:"state"`
}

func (a *HTTPAttachments) configured() bool {
	return a != nil && strings.TrimSpace(a.BaseURL) != ""
}

func (a *HTTPAttachments) do(ctx context.Context, method, path, tenant, idempotencyKey string, body, out any) error {
	endpoint, err := a.endpoint(path)
	if err != nil {
		return unavailable("attachments_gateway")
	}
	var reader io.Reader
	var raw []byte
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return unavailable("attachments_gateway")
		}
		defer zeroAttachmentBytes(raw)
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return unavailable("attachments_gateway")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Tenant-ID", tenant)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return unavailable("attachments_gateway")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return attachmentStatusError(response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return unavailable("attachments_gateway")
	}
	return nil
}

func (a *HTTPAttachments) endpoint(path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(a.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid attachments base url")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (a *HTTPAttachments) environmentPath(tenant, applicationID, environmentID, leaf string) string {
	return "/v1/organizations/" + tenant + "/applications/" + applicationID + "/environments/" + environmentID + "/" + leaf
}

func (a *HTTPAttachments) principal() string {
	if a != nil && strings.TrimSpace(a.PrincipalID) != "" {
		return strings.TrimSpace(a.PrincipalID)
	}
	return "agent-api"
}

func attachmentPathScope(values ...string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "/?#\\") {
			return false
		}
	}
	return true
}

func attachmentArgumentError() error {
	return agentdomain.NewError(agentdomain.CodeInvalidArgument, "invalid attachments request")
}

func attachmentStatusError(status int) error {
	const message = "attachments gateway rejected request"
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return agentdomain.NewError(agentdomain.CodeInvalidArgument, message)
	case http.StatusUnauthorized, http.StatusForbidden:
		return agentdomain.NewError(agentdomain.CodePermissionDenied, message)
	case http.StatusNotFound:
		return agentdomain.NewError(agentdomain.CodeNotFound, message)
	case http.StatusConflict, http.StatusPreconditionFailed:
		return agentdomain.NewError(agentdomain.CodeConflict, message)
	default:
		return &agentapp.ProviderError{Message: "attachments gateway returned non-success", Retryable: status == http.StatusTooManyRequests || status >= 500}
	}
}

func canonicalHostname(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func zeroAttachmentBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
