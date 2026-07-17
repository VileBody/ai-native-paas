// Package productiongate provides production-safe dependency adapters for the
// Agent MCP façade. Missing optional internal service URLs remain fail-closed;
// configured URLs are called through narrow tenant-scoped HTTP clients.
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

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/application"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func unavailable(code string) error {
	return &application.ProviderError{Message: code + " dependency is not installed", Retryable: true}
}

type Source struct{}

func (Source) CreateProject(context.Context, string, string, string, string) (application.ProjectRef, error) {
	return application.ProjectRef{}, unavailable("project_mcp")
}
func (Source) GetProject(context.Context, string, string) (application.ProjectRef, error) {
	return application.ProjectRef{}, unavailable("project_mcp")
}
func (Source) ApplyPatch(context.Context, string, string, application.ApplyPatchArguments, string) (application.CommitRef, error) {
	return application.CommitRef{}, unavailable("project_mcp")
}
func (Source) CreateBranch(context.Context, string, string, application.CreateBranchArguments, string) (application.CommitRef, error) {
	return application.CommitRef{}, unavailable("project_mcp")
}
func (Source) CreateMergeRequest(context.Context, string, string, application.CreateMergeRequestArguments, string) (application.MergeRequestRef, error) {
	return application.MergeRequestRef{}, unavailable("project_mcp")
}
func (Source) Reconcile(context.Context, string, string) error { return unavailable("project_mcp") }

type Builds struct{}

func (Builds) Request(context.Context, string, sourcev1.SourceRevision, int64, string, string) (application.BuildResult, error) {
	return application.BuildResult{}, unavailable("workspace_nat")
}
func (Builds) Get(context.Context, string, string) (application.BuildResult, error) {
	return application.BuildResult{}, unavailable("workspace_nat")
}
func (Builds) Resume(context.Context, string, string) (application.BuildResult, error) {
	return application.BuildResult{}, unavailable("workspace_nat")
}

type Runtime struct{}

func (Runtime) Deploy(context.Context, runtimev1.DeployRequest, int64) (application.RuntimeResult, error) {
	return application.RuntimeResult{}, unavailable("runtime_cell")
}
func (Runtime) Get(context.Context, string, string) (application.RuntimeResult, error) {
	return application.RuntimeResult{}, unavailable("runtime_cell")
}
func (Runtime) Rollback(context.Context, string, string, string, int64, string) (application.RuntimeResult, error) {
	return application.RuntimeResult{}, unavailable("runtime_cell")
}
func (Runtime) Reconcile(context.Context, string, string) error { return unavailable("runtime_cell") }

type Attachments struct{}

func (Attachments) SetSecret(context.Context, attachmentsv1.SetSecretRequest) (attachmentsv1.SecretMetadata, error) {
	return attachmentsv1.SecretMetadata{}, unavailable("openbao_unseal")
}
func (Attachments) ListSecretMetadata(context.Context, string, string, string) ([]attachmentsv1.SecretMetadata, error) {
	return nil, unavailable("openbao_unseal")
}
func (Attachments) Provision(context.Context, attachmentsv1.ServiceRequest) (attachmentsv1.ServiceInstanceRef, error) {
	return attachmentsv1.ServiceInstanceRef{}, unavailable("runtime_cell")
}
func (Attachments) Bind(context.Context, attachmentsv1.BindRequest) (attachmentsv1.BindingRef, error) {
	return attachmentsv1.BindingRef{}, unavailable("runtime_cell")
}
func (Attachments) AddDomain(context.Context, attachmentsv1.DomainRequest) (attachmentsv1.DomainRef, error) {
	return attachmentsv1.DomainRef{}, unavailable("public_ingress")
}

type Commerce struct{}

// HTTPCommerce is the production bridge from the Agent MCP façade to the
// Commerce control-plane API. An empty BaseURL deliberately keeps mutations
// fail-closed during partial deployments.
type HTTPCommerce struct {
	BaseURL     string
	PrincipalID string
	HTTPClient  *http.Client
}

func NewHTTPCommerce(baseURL, principalID string) *HTTPCommerce {
	return &HTTPCommerce{BaseURL: baseURL, PrincipalID: principalID, HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

func (Commerce) Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	return commercev1.EntitlementDecision{Allowed: false, Reason: "control-plane service gateways are not installed", PolicyVersion: "network-deferred-v1"}, nil
}

func (c *HTTPCommerce) Check(ctx context.Context, request commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	if !c.configured() {
		return (Commerce{}).Check(ctx, request)
	}
	tenant := strings.TrimSpace(request.TenantID)
	if tenant == "" {
		return commercev1.EntitlementDecision{}, unavailable("commerce_gateway")
	}
	var out commercev1.EntitlementDecision
	if err := c.do(ctx, http.MethodPost, "/v1/organizations/"+url.PathEscape(tenant)+"/entitlements/check", tenant, request, &out); err != nil {
		return commercev1.EntitlementDecision{}, err
	}
	return out, nil
}

type Operations struct{}

func (Operations) Get(context.Context, string, string) (kernelv1.OperationSnapshot, error) {
	return kernelv1.OperationSnapshot{}, unavailable("kernel_gateway")
}
func (Operations) Cancel(context.Context, string, string) error { return unavailable("kernel_gateway") }

// HTTPOperations is the production bridge from the Agent MCP façade to the
// Kernel operation API. It remains fail-closed until KERNEL_API_URL is wired.
type HTTPOperations struct {
	BaseURL     string
	PrincipalID string
	HTTPClient  *http.Client
}

func NewHTTPOperations(baseURL, principalID string) *HTTPOperations {
	return &HTTPOperations{BaseURL: baseURL, PrincipalID: principalID, HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

func (o *HTTPOperations) Get(ctx context.Context, tenant, operationID string) (kernelv1.OperationSnapshot, error) {
	if !o.configured() {
		return (Operations{}).Get(ctx, tenant, operationID)
	}
	tenant, operationID = strings.TrimSpace(tenant), strings.TrimSpace(operationID)
	if tenant == "" || operationID == "" {
		return kernelv1.OperationSnapshot{}, unavailable("kernel_gateway")
	}
	var out kernelv1.OperationSnapshot
	if err := o.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(operationID), tenant, operationID, nil, &out); err != nil {
		return kernelv1.OperationSnapshot{}, err
	}
	if string(out.TenantID) != tenant || string(out.OperationID) != operationID {
		return kernelv1.OperationSnapshot{}, unavailable("kernel_gateway")
	}
	return out, nil
}

func (o *HTTPOperations) Cancel(ctx context.Context, tenant, operationID string) error {
	if !o.configured() {
		return (Operations{}).Cancel(ctx, tenant, operationID)
	}
	tenant, operationID = strings.TrimSpace(tenant), strings.TrimSpace(operationID)
	if tenant == "" || operationID == "" {
		return unavailable("kernel_gateway")
	}
	var out struct {
		Operation kernelv1.OperationRef `json:"operation"`
	}
	if err := o.do(ctx, http.MethodPost, "/v1/operations/"+url.PathEscape(operationID)+"/cancel", tenant, operationID, nil, &out); err != nil {
		return err
	}
	if string(out.Operation.TenantID) != tenant || string(out.Operation.OperationID) != operationID {
		return unavailable("kernel_gateway")
	}
	return nil
}

type Logs struct{}

func (Logs) GetLogs(context.Context, string, string, int) ([]string, error) {
	return nil, unavailable("workspace_nat")
}

type Usage struct{}

func (Usage) GetUsage(context.Context, string, string) (commercev1.InvoicePreview, error) {
	return commercev1.InvoicePreview{}, unavailable("commerce_gateway")
}

func (c *HTTPCommerce) GetUsage(ctx context.Context, tenant, period string) (commercev1.InvoicePreview, error) {
	if !c.configured() {
		return commercev1.InvoicePreview{}, unavailable("commerce_gateway")
	}
	tenant, period = strings.TrimSpace(tenant), strings.TrimSpace(period)
	if tenant == "" || period == "" {
		return commercev1.InvoicePreview{}, unavailable("commerce_gateway")
	}
	var out commercev1.InvoicePreview
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+url.PathEscape(tenant)+"/billing-periods/"+url.PathEscape(period)+"/invoice-preview", tenant, nil, &out); err != nil {
		return commercev1.InvoicePreview{}, err
	}
	return out, nil
}

func (c *HTTPCommerce) configured() bool {
	return c != nil && strings.TrimSpace(c.BaseURL) != ""
}

func (c *HTTPCommerce) do(ctx context.Context, method, path, tenant string, body, out any) error {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return unavailable("commerce_gateway")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return unavailable("commerce_gateway")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return unavailable("commerce_gateway")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Tenant-ID", tenant)
	request.Header.Set("X-Principal-ID", c.principal())
	request.Header.Set("X-Principal-Kind", "service")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return unavailable("commerce_gateway")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &application.ProviderError{Message: "commerce gateway returned non-success", Retryable: response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return unavailable("commerce_gateway")
	}
	return nil
}

func (c *HTTPCommerce) endpoint(path string) (string, error) {
	raw := strings.TrimSpace(c.BaseURL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid commerce base url")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (c *HTTPCommerce) principal() string {
	if c != nil && strings.TrimSpace(c.PrincipalID) != "" {
		return strings.TrimSpace(c.PrincipalID)
	}
	return "agent-api"
}

func (o *HTTPOperations) configured() bool {
	return o != nil && strings.TrimSpace(o.BaseURL) != ""
}

func (o *HTTPOperations) do(ctx context.Context, method, path, tenant, operationID string, body, out any) error {
	endpoint, err := o.endpoint(path)
	if err != nil {
		return unavailable("kernel_gateway")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return unavailable("kernel_gateway")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return unavailable("kernel_gateway")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Tenant-ID", tenant)
	request.Header.Set("X-Principal-ID", o.principal())
	request.Header.Set("X-Principal-Kind", "service")
	request.Header.Set("X-Scopes", "kernel.operation.read kernel.operation.cancel")
	request.Header.Set("X-Correlation-ID", "agent-api-operation-"+operationID)
	if method != http.MethodGet {
		request.Header.Set("Idempotency-Key", "agent-api-"+strings.ToLower(method)+"-"+operationID)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return unavailable("kernel_gateway")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &application.ProviderError{Message: "kernel gateway returned non-success", Retryable: response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return unavailable("kernel_gateway")
	}
	return nil
}

func (o *HTTPOperations) endpoint(path string) (string, error) {
	raw := strings.TrimSpace(o.BaseURL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid kernel base url")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (o *HTTPOperations) principal() string {
	if o != nil && strings.TrimSpace(o.PrincipalID) != "" {
		return strings.TrimSpace(o.PrincipalID)
	}
	return "agent-api"
}
