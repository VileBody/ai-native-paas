package productiongate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	agentapp "github.com/keir-research/ai-native-paas/internal/agent/application"
	agentdomain "github.com/keir-research/ai-native-paas/internal/agent/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

// HTTPRuntime bridges the Agent MCP facade to runtime-api. An empty BaseURL
// deliberately preserves Runtime's fail-closed production behavior.
type HTTPRuntime struct {
	BaseURL     string
	PrincipalID string
	HTTPClient  *http.Client
}

var _ agentapp.RuntimeGateway = (*HTTPRuntime)(nil)

func NewHTTPRuntime(baseURL, principalID string) *HTTPRuntime {
	return &HTTPRuntime{
		BaseURL:     baseURL,
		PrincipalID: principalID,
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *HTTPRuntime) Deploy(ctx context.Context, request runtimev1.DeployRequest, expectedRevision int64) (agentapp.RuntimeResult, error) {
	if !g.configured() {
		return (Runtime{}).Deploy(ctx, request, expectedRevision)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.ActorID = strings.TrimSpace(request.ActorID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if expectedRevision < 0 || request.Validate() != nil {
		return agentapp.RuntimeResult{}, agentdomain.NewError(agentdomain.CodeInvalidArgument, "invalid runtime deploy request")
	}

	body := runtimeDeployBody{Artifact: request.Artifact, Configuration: request.Configuration}
	path := "/v1/organizations/" + url.PathEscape(request.TenantID) +
		"/applications/" + url.PathEscape(request.ApplicationID) +
		"/environments/" + url.PathEscape(request.EnvironmentID) + "/deployments"
	var deployment runtimev1.DeploymentRef
	if err := g.do(ctx, http.MethodPost, path, request.TenantID, request.IdempotencyKey,
		"agent-api-runtime-"+request.IdempotencyKey, expectedRevision, body, &deployment); err != nil {
		return agentapp.RuntimeResult{}, err
	}
	if !validRuntimeDeploymentRef(deployment) {
		return agentapp.RuntimeResult{}, unavailable("runtime_gateway")
	}
	return agentapp.RuntimeResult{Deployment: deployment}, nil
}

func (g *HTTPRuntime) Get(ctx context.Context, tenant, deploymentID string) (agentapp.RuntimeResult, error) {
	if !g.configured() {
		return (Runtime{}).Get(ctx, tenant, deploymentID)
	}
	tenant = strings.TrimSpace(tenant)
	deploymentID = strings.TrimSpace(deploymentID)
	if tenant == "" || deploymentID == "" {
		return agentapp.RuntimeResult{}, agentdomain.NewError(agentdomain.CodeInvalidArgument, "invalid runtime status request")
	}

	path := "/v1/organizations/" + url.PathEscape(tenant) + "/deployments/" + url.PathEscape(deploymentID)
	var status runtimev1.RuntimeStatus
	if err := g.do(ctx, http.MethodGet, path, tenant, "", "agent-api-runtime-status-"+deploymentID, 0, nil, &status); err != nil {
		return agentapp.RuntimeResult{}, err
	}
	if status.DeploymentID != deploymentID || !validRuntimeStatus(status) {
		return agentapp.RuntimeResult{}, unavailable("runtime_gateway")
	}
	deployment := runtimev1.DeploymentRef{
		DeploymentID:   status.DeploymentID,
		ReleaseID:      status.ActiveRelease,
		Phase:          status.Phase,
		GitOpsRevision: status.GitOpsRevision,
	}
	return agentapp.RuntimeResult{Deployment: deployment, Status: &status}, nil
}

func (g *HTTPRuntime) Rollback(ctx context.Context, tenant, deploymentID, targetReleaseID string, expectedRevision int64, idempotencyKey string) (agentapp.RuntimeResult, error) {
	if !g.configured() {
		return (Runtime{}).Rollback(ctx, tenant, deploymentID, targetReleaseID, expectedRevision, idempotencyKey)
	}
	tenant = strings.TrimSpace(tenant)
	deploymentID = strings.TrimSpace(deploymentID)
	targetReleaseID = strings.TrimSpace(targetReleaseID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if tenant == "" || deploymentID == "" || targetReleaseID == "" || idempotencyKey == "" || expectedRevision < 0 {
		return agentapp.RuntimeResult{}, agentdomain.NewError(agentdomain.CodeInvalidArgument, "invalid runtime rollback request")
	}

	path := "/v1/organizations/" + url.PathEscape(tenant) + "/deployments/" + url.PathEscape(deploymentID) + "/rollback"
	body := runtimeRollbackBody{TargetReleaseID: targetReleaseID}
	var deployment runtimev1.DeploymentRef
	if err := g.do(ctx, http.MethodPost, path, tenant, idempotencyKey,
		"agent-api-runtime-"+idempotencyKey, expectedRevision, body, &deployment); err != nil {
		return agentapp.RuntimeResult{}, err
	}
	if !validRuntimeDeploymentRef(deployment) {
		return agentapp.RuntimeResult{}, unavailable("runtime_gateway")
	}
	return agentapp.RuntimeResult{Deployment: deployment}, nil
}

// runtime-api performs lost-response recovery inside its idempotent deploy and
// rollback commands. Reconcile verifies the known deployment when the caller
// has one; callers may then safely retry the original command with the same key.
func (g *HTTPRuntime) Reconcile(ctx context.Context, tenant, deploymentID string) error {
	if !g.configured() {
		return (Runtime{}).Reconcile(ctx, tenant, deploymentID)
	}
	_, err := g.Get(ctx, tenant, deploymentID)
	return err
}

type runtimeDeployBody struct {
	Artifact      buildv1.ArtifactRef     `json:"artifact"`
	Configuration runtimev1.ReleaseConfig `json:"configuration"`
}

type runtimeRollbackBody struct {
	TargetReleaseID  string `json:"target_release_id"`
	CriticalOverride bool   `json:"critical_override"`
}

func (g *HTTPRuntime) configured() bool {
	return g != nil && strings.TrimSpace(g.BaseURL) != ""
}

func (g *HTTPRuntime) do(ctx context.Context, method, path, tenant, idempotencyKey, correlationID string, expectedRevision int64, body, out any) error {
	endpoint, err := g.endpoint(path)
	if err != nil {
		return unavailable("runtime_gateway")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return unavailable("runtime_gateway")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return unavailable("runtime_gateway")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Tenant-ID", tenant)
	request.Header.Set("X-Correlation-ID", correlationID)
	if method != http.MethodGet {
		request.Header.Set("Idempotency-Key", idempotencyKey)
		request.Header.Set("X-Expected-Environment-Revision", strconv.FormatInt(expectedRevision, 10))
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return unavailable("runtime_gateway")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return runtimeGatewayStatusError(response.StatusCode, response.Body)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(out); err != nil {
		return unavailable("runtime_gateway")
	}
	return nil
}

func (g *HTTPRuntime) endpoint(path string) (string, error) {
	raw := strings.TrimSpace(g.BaseURL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid runtime base url")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (g *HTTPRuntime) principal() string {
	if g != nil && strings.TrimSpace(g.PrincipalID) != "" {
		return strings.TrimSpace(g.PrincipalID)
	}
	return "agent-api"
}

func runtimeGatewayStatusError(status int, body io.Reader) error {
	message := "runtime gateway returned non-success"
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&payload)
	if strings.TrimSpace(payload.Error.Message) != "" && status < 500 {
		message = strings.TrimSpace(payload.Error.Message)
	}
	switch status {
	case http.StatusBadRequest:
		return agentdomain.NewError(agentdomain.CodeInvalidArgument, message)
	case http.StatusUnauthorized, http.StatusForbidden:
		return agentdomain.NewError(agentdomain.CodePermissionDenied, message)
	case http.StatusNotFound:
		return agentdomain.NewError(agentdomain.CodeNotFound, message)
	case http.StatusConflict:
		return agentdomain.NewError(agentdomain.CodeConflict, message)
	default:
		if status < 500 && status != http.StatusTooManyRequests {
			return agentdomain.NewError(agentdomain.CodeUnavailable, "runtime gateway contract rejected the request")
		}
		return &agentapp.ProviderError{
			Message:   "runtime gateway returned non-success",
			Retryable: true,
		}
	}
}

func validRuntimeDeploymentRef(ref runtimev1.DeploymentRef) bool {
	return strings.TrimSpace(ref.DeploymentID) != "" && strings.TrimSpace(ref.ReleaseID) != "" && validRuntimePhase(ref.Phase)
}

func validRuntimeStatus(status runtimev1.RuntimeStatus) bool {
	return strings.TrimSpace(status.DeploymentID) != "" && validRuntimePhase(status.Phase)
}

func validRuntimePhase(phase runtimev1.DeploymentPhase) bool {
	switch phase {
	case runtimev1.DeploymentPending, runtimev1.DeploymentGitCommitted, runtimev1.DeploymentArgoSyncing,
		runtimev1.DeploymentRollingOut, runtimev1.DeploymentReady, runtimev1.DeploymentDegraded, runtimev1.DeploymentFailed:
		return true
	default:
		return false
	}
}
