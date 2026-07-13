// Package v2 defines the stable Project MCP catalog and verified-scope invocation envelope.
package v2

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
)

const (
	APIVersion       = "agent.platform.example.com/v2"
	SemanticsVersion = "v2"
)

type Tool string
type ToolGroup string

const (
	GroupProjectRepository  ToolGroup = "project_repository"
	GroupWorkspace          ToolGroup = "workspace"
	GroupInfrastructure     ToolGroup = "infrastructure"
	GroupBuild              ToolGroup = "build"
	GroupGitOpsRuntime      ToolGroup = "gitops_runtime"
	GroupProviderCapability ToolGroup = "provider_capability"
	GroupGovernance         ToolGroup = "governance"
)

const (
	ToolProjectCreate                Tool = "project_create"
	ToolProjectGet                   Tool = "project_get"
	ToolProjectGetPolicy             Tool = "project_get_policy"
	ToolRepositoryStatus             Tool = "repository_status"
	ToolRepositoryDiff               Tool = "repository_diff"
	ToolRepositoryApplyPatch         Tool = "repository_apply_patch"
	ToolRepositoryCreateBranch       Tool = "repository_create_branch"
	ToolRepositoryCommit             Tool = "repository_commit"
	ToolRepositoryPush               Tool = "repository_push"
	ToolRepositoryCreateMergeRequest Tool = "repository_create_merge_request"
	ToolWorkspaceCreate              Tool = "workspace_create"
	ToolWorkspaceGet                 Tool = "workspace_get"
	ToolWorkspaceExec                Tool = "workspace_exec"
	ToolWorkspaceUploadArtifact      Tool = "workspace_upload_artifact"
	ToolWorkspaceCancelCommand       Tool = "workspace_cancel_command"
	ToolWorkspaceDestroy             Tool = "workspace_destroy"
	ToolInfraInit                    Tool = "infra_init"
	ToolInfraValidate                Tool = "infra_validate"
	ToolInfraPlan                    Tool = "infra_plan"
	ToolInfraGetPlan                 Tool = "infra_get_plan"
	ToolInfraApply                   Tool = "infra_apply"
	ToolInfraDestroy                 Tool = "infra_destroy"
	ToolInfraStateList               Tool = "infra_state_list"
	ToolInfraImport                  Tool = "infra_import"
	ToolBuildExecute                 Tool = "build_execute"
	ToolBuildGet                     Tool = "build_get"
	ToolArtifactGet                  Tool = "artifact_get"
	ToolArtifactVerify               Tool = "artifact_verify"
	ToolGitOpsValidate               Tool = "gitops_validate"
	ToolGitOpsCommit                 Tool = "gitops_commit"
	ToolArgoCDSync                   Tool = "argocd_sync"
	ToolArgoCDGetStatus              Tool = "argocd_get_status"
	ToolArgoCDRollback               Tool = "argocd_rollback"
	ToolDeploymentGetLogs            Tool = "deployment_get_logs"
	ToolDeploymentGetEvents          Tool = "deployment_get_events"
	ToolDeploymentHTTPProbe          Tool = "deployment_http_probe"
	ToolSecretSet                    Tool = "secret_set"
	ToolSecretListMetadata           Tool = "secret_list_metadata"
	ToolCredentialRequest            Tool = "credential_request"
	ToolCredentialRevoke             Tool = "credential_revoke"
	ToolRecipeSearch                 Tool = "recipe_search"
	ToolRecipeGet                    Tool = "recipe_get"
	ToolProviderConnect              Tool = "provider_connect"
	ToolProviderPlanResource         Tool = "provider_plan_resource"
	ToolCapabilityBind               Tool = "capability_bind"
	ToolCapabilityGetUsage           Tool = "capability_get_usage"
	ToolCostEstimate                 Tool = "cost_estimate"
	ToolApprovalRequest              Tool = "approval_request"
	ToolApprovalGet                  Tool = "approval_get"
	ToolOperationGet                 Tool = "operation_get"
	ToolOperationWait                Tool = "operation_wait"
	ToolOperationCancel              Tool = "operation_cancel"
	ToolUsageGet                     Tool = "usage_get"
)

type ToolDefinition struct {
	Name          Tool      `json:"name"`
	Group         ToolGroup `json:"group"`
	ReadOnly      bool      `json:"read_only"`
	RequiredScope string    `json:"required_scope"`
}

var definitions = []ToolDefinition{
	{Name: ToolProjectCreate, Group: GroupProjectRepository}, {Name: ToolProjectGet, Group: GroupProjectRepository, ReadOnly: true}, {Name: ToolProjectGetPolicy, Group: GroupProjectRepository, ReadOnly: true},
	{Name: ToolRepositoryStatus, Group: GroupProjectRepository, ReadOnly: true}, {Name: ToolRepositoryDiff, Group: GroupProjectRepository, ReadOnly: true}, {Name: ToolRepositoryApplyPatch, Group: GroupProjectRepository},
	{Name: ToolRepositoryCreateBranch, Group: GroupProjectRepository}, {Name: ToolRepositoryCommit, Group: GroupProjectRepository}, {Name: ToolRepositoryPush, Group: GroupProjectRepository}, {Name: ToolRepositoryCreateMergeRequest, Group: GroupProjectRepository},
	{Name: ToolWorkspaceCreate, Group: GroupWorkspace}, {Name: ToolWorkspaceGet, Group: GroupWorkspace, ReadOnly: true}, {Name: ToolWorkspaceExec, Group: GroupWorkspace}, {Name: ToolWorkspaceUploadArtifact, Group: GroupWorkspace}, {Name: ToolWorkspaceCancelCommand, Group: GroupWorkspace}, {Name: ToolWorkspaceDestroy, Group: GroupWorkspace},
	{Name: ToolInfraInit, Group: GroupInfrastructure}, {Name: ToolInfraValidate, Group: GroupInfrastructure}, {Name: ToolInfraPlan, Group: GroupInfrastructure}, {Name: ToolInfraGetPlan, Group: GroupInfrastructure, ReadOnly: true}, {Name: ToolInfraApply, Group: GroupInfrastructure}, {Name: ToolInfraDestroy, Group: GroupInfrastructure}, {Name: ToolInfraStateList, Group: GroupInfrastructure, ReadOnly: true}, {Name: ToolInfraImport, Group: GroupInfrastructure},
	{Name: ToolBuildExecute, Group: GroupBuild}, {Name: ToolBuildGet, Group: GroupBuild, ReadOnly: true}, {Name: ToolArtifactGet, Group: GroupBuild, ReadOnly: true}, {Name: ToolArtifactVerify, Group: GroupBuild, ReadOnly: true},
	{Name: ToolGitOpsValidate, Group: GroupGitOpsRuntime}, {Name: ToolGitOpsCommit, Group: GroupGitOpsRuntime}, {Name: ToolArgoCDSync, Group: GroupGitOpsRuntime}, {Name: ToolArgoCDGetStatus, Group: GroupGitOpsRuntime, ReadOnly: true}, {Name: ToolArgoCDRollback, Group: GroupGitOpsRuntime}, {Name: ToolDeploymentGetLogs, Group: GroupGitOpsRuntime, ReadOnly: true}, {Name: ToolDeploymentGetEvents, Group: GroupGitOpsRuntime, ReadOnly: true}, {Name: ToolDeploymentHTTPProbe, Group: GroupGitOpsRuntime, ReadOnly: true},
	{Name: ToolSecretSet, Group: GroupProviderCapability}, {Name: ToolSecretListMetadata, Group: GroupProviderCapability, ReadOnly: true}, {Name: ToolCredentialRequest, Group: GroupProviderCapability}, {Name: ToolCredentialRevoke, Group: GroupProviderCapability}, {Name: ToolRecipeSearch, Group: GroupProviderCapability, ReadOnly: true}, {Name: ToolRecipeGet, Group: GroupProviderCapability, ReadOnly: true}, {Name: ToolProviderConnect, Group: GroupProviderCapability}, {Name: ToolProviderPlanResource, Group: GroupProviderCapability}, {Name: ToolCapabilityBind, Group: GroupProviderCapability}, {Name: ToolCapabilityGetUsage, Group: GroupProviderCapability, ReadOnly: true},
	{Name: ToolCostEstimate, Group: GroupGovernance, ReadOnly: true}, {Name: ToolApprovalRequest, Group: GroupGovernance}, {Name: ToolApprovalGet, Group: GroupGovernance, ReadOnly: true}, {Name: ToolOperationGet, Group: GroupGovernance, ReadOnly: true}, {Name: ToolOperationWait, Group: GroupGovernance, ReadOnly: true}, {Name: ToolOperationCancel, Group: GroupGovernance}, {Name: ToolUsageGet, Group: GroupGovernance, ReadOnly: true},
}

var definitionByTool map[Tool]ToolDefinition

func init() {
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	definitionByTool = make(map[Tool]ToolDefinition, len(definitions))
	for index := range definitions {
		definitions[index].RequiredScope = "agent.tool:" + string(definitions[index].Name)
		if _, exists := definitionByTool[definitions[index].Name]; exists {
			panic("duplicate MCP v2 tool: " + definitions[index].Name)
		}
		definitionByTool[definitions[index].Name] = definitions[index]
	}
}

func ToolCatalog() []ToolDefinition { return append([]ToolDefinition(nil), definitions...) }
func ValidTool(tool Tool) bool      { _, ok := definitionByTool[tool]; return ok }
func Definition(tool Tool) (ToolDefinition, bool) {
	definition, ok := definitionByTool[tool]
	return definition, ok
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// VerifiedInvocationContext is built only by the authentication middleware.
// It is deliberately not part of the client-controlled JSON invocation body.
type VerifiedInvocationContext struct {
	TenantID      string
	ProjectID     string
	UserID        string
	AgentID       string
	WorkspaceID   string
	GrantedScopes map[string]struct{}
	CredentialID  string
}

func (c VerifiedInvocationContext) Authorize(tool Tool) error {
	definition, ok := Definition(tool)
	if !ok || !safeID.MatchString(c.TenantID) || !safeID.MatchString(c.ProjectID) || !safeID.MatchString(c.UserID) || !safeID.MatchString(c.AgentID) || !safeID.MatchString(c.CredentialID) {
		return errors.New("invalid verified invocation context")
	}
	if _, ok := c.GrantedScopes[definition.RequiredScope]; !ok {
		return errors.New("tool scope is not granted")
	}
	return nil
}

type InvocationRequest struct {
	APIVersion       string          `json:"api_version"`
	SemanticsVersion string          `json:"semantics_version"`
	TaskID           string          `json:"task_id"`
	Tool             Tool            `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	IdempotencyKey   string          `json:"idempotency_key"`
	CorrelationID    string          `json:"correlation_id"`
	ApprovalGrantID  string          `json:"approval_grant_id,omitempty"`
}

func (r InvocationRequest) Validate() error {
	if r.APIVersion != APIVersion || r.SemanticsVersion != SemanticsVersion || !safeID.MatchString(r.TaskID) || !ValidTool(r.Tool) || strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 128 || !safeID.MatchString(r.CorrelationID) {
		return errors.New("invalid invocation identity")
	}
	if len(r.Arguments) == 0 || len(r.Arguments) > 1<<20 || !json.Valid(r.Arguments) {
		return errors.New("invalid tool arguments")
	}
	if r.ApprovalGrantID != "" && !safeID.MatchString(r.ApprovalGrantID) {
		return errors.New("invalid approval grant")
	}
	return nil
}

type InvocationResponse struct {
	APIVersion   string                 `json:"api_version"`
	InvocationID string                 `json:"invocation_id"`
	Operation    *kernelv2.OperationRef `json:"operation,omitempty"`
	Result       json.RawMessage        `json:"result,omitempty"`
	Error        *kernelv2.PublicError  `json:"error,omitempty"`
	Replayed     bool                   `json:"replayed"`
}

type EnrollmentExchange struct {
	EnrollmentToken string `json:"enrollment_token"`
	AgentID         string `json:"agent_id"`
	PublicKey       string `json:"public_key"`
}

type EnrollmentCredential struct {
	CredentialID string    `json:"credential_id"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type AccessCredentialClaims struct {
	TenantID  string    `json:"tenant_id"`
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	AgentID   string    `json:"agent_id"`
	Scopes    []string  `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c AccessCredentialClaims) Validate(now time.Time) error {
	if !safeID.MatchString(c.TenantID) || !safeID.MatchString(c.ProjectID) || !safeID.MatchString(c.UserID) || !safeID.MatchString(c.AgentID) || len(c.Scopes) == 0 || !c.ExpiresAt.After(now) || c.ExpiresAt.After(now.Add(15*time.Minute+time.Second)) {
		return errors.New("invalid access credential claims")
	}
	return nil
}

type CompatibilityRoute struct {
	V1Tool             agentv1.Tool `json:"v1_tool"`
	V2Targets          []Tool       `json:"v2_targets"`
	GovernanceRequired bool         `json:"governance_required"`
	Deprecation        string       `json:"deprecation"`
}

var compatibilityRoutes = map[agentv1.Tool]CompatibilityRoute{
	agentv1.ToolCreateProject:        route(agentv1.ToolCreateProject, ToolProjectCreate),
	agentv1.ToolGetProject:           route(agentv1.ToolGetProject, ToolProjectGet),
	agentv1.ToolApplyRepositoryPatch: route(agentv1.ToolApplyRepositoryPatch, ToolRepositoryApplyPatch),
	agentv1.ToolCreateBranch:         route(agentv1.ToolCreateBranch, ToolRepositoryCreateBranch),
	agentv1.ToolCreateMergeRequest:   route(agentv1.ToolCreateMergeRequest, ToolRepositoryCreateMergeRequest),
	agentv1.ToolRequestBuild:         route(agentv1.ToolRequestBuild, ToolBuildExecute),
	agentv1.ToolGetBuild:             route(agentv1.ToolGetBuild, ToolBuildGet),
	agentv1.ToolDeploy:               route(agentv1.ToolDeploy, ToolGitOpsValidate, ToolGitOpsCommit, ToolArgoCDSync),
	agentv1.ToolGetDeployment:        route(agentv1.ToolGetDeployment, ToolArgoCDGetStatus),
	agentv1.ToolRollback:             route(agentv1.ToolRollback, ToolArgoCDRollback),
	agentv1.ToolSetSecret:            route(agentv1.ToolSetSecret, ToolSecretSet),
	agentv1.ToolListSecretMetadata:   route(agentv1.ToolListSecretMetadata, ToolSecretListMetadata),
	agentv1.ToolProvisionService:     route(agentv1.ToolProvisionService, ToolProviderPlanResource, ToolInfraPlan, ToolInfraApply),
	agentv1.ToolBindService:          route(agentv1.ToolBindService, ToolProviderConnect),
	agentv1.ToolAddDomain:            route(agentv1.ToolAddDomain, ToolProviderPlanResource, ToolInfraPlan, ToolInfraApply),
	agentv1.ToolGetLogs:              route(agentv1.ToolGetLogs, ToolDeploymentGetLogs),
	agentv1.ToolGetUsage:             route(agentv1.ToolGetUsage, ToolUsageGet),
	agentv1.ToolRequestApproval:      route(agentv1.ToolRequestApproval, ToolApprovalRequest),
	agentv1.ToolGetOperation:         route(agentv1.ToolGetOperation, ToolOperationGet),
	agentv1.ToolCancelOperation:      route(agentv1.ToolCancelOperation, ToolOperationCancel),
}

func route(v1Tool agentv1.Tool, targets ...Tool) CompatibilityRoute {
	return CompatibilityRoute{V1Tool: v1Tool, V2Targets: targets, GovernanceRequired: true, Deprecation: "MCP v1 compatibility façade; migrate to Project MCP v2"}
}

func CompatibilityFor(v1Tool agentv1.Tool) (CompatibilityRoute, bool) {
	route, ok := compatibilityRoutes[v1Tool]
	return route, ok
}

func CompatibilityCatalog() []CompatibilityRoute {
	routes := make([]CompatibilityRoute, 0, len(compatibilityRoutes))
	for _, route := range compatibilityRoutes {
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].V1Tool < routes[j].V1Tool })
	return routes
}

func DecodeStrict(raw json.RawMessage, out any) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("arguments size invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func StableFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", err
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}
