// Package mcp implements the project-scoped MCP v2 HTTP façade.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type ProjectReader interface {
	GetProject(context.Context, string, string) (domain.Project, error)
	GetRepositoryForProject(context.Context, string, string) (domain.Repository, error)
}

type WorkspaceCommands interface {
	Create(context.Context, workspace.CreateRequest) (workspacev1.WorkspaceRef, error)
	Get(context.Context, workspace.Scope, string) (workspacev1.WorkspaceRef, error)
	GetSourceRevision(context.Context, workspace.Scope, string) (sourcev2.SourceRevision, error)
	GetCommitReceipt(context.Context, workspace.Scope, string) (sourcev2.AgentCommitReceipt, error)
	Exec(context.Context, workspace.ExecRequest) (workspacev1.CommandView, error)
	Destroy(context.Context, workspace.Scope, string) (workspacev1.WorkspaceRef, error)
}

type InfrastructureCommands interface {
	PlanFromReceipt(context.Context, infraapp.ReceiptPlanCommand) (infraapp.PlanResult, error)
	GetPlan(context.Context, string, string, string) (infraapp.PlanRecord, error)
	GetApprovalStatus(context.Context, string, string, string, string) (infraapp.ApprovalStatus, error)
	AuthorizeApply(context.Context, infraapp.ApplyCommand) (infraapp.PlanRecord, error)
}

type SourceChangeCommands interface {
	PlanSourceChange(context.Context, workspace.SourceChangePlanRequest) (workspace.SourceChangePlan, error)
	GetSourceChangePlan(context.Context, string, string, string) (workspace.SourceChangePlan, error)
	CheckSourceApproval(context.Context, workspace.Scope, string, string) (workspace.SourceChangePlan, error)
	AuthorizeSourceCommit(context.Context, workspace.AuthorizeSourceCommitRequest) (workspace.SourceChangePlan, error)
}

type RepositoryMergeRequests interface {
	CreateMergeRequest(context.Context, sourceapp.CreateMergeRequestCommand) (sourceapp.CreateMergeRequestResult, error)
}

var errInvalidWorkspaceArguments = errors.New("invalid workspace tool arguments")
var errInvalidInfrastructureArguments = errors.New("invalid infrastructure tool arguments")
var errInvalidRepositoryArguments = errors.New("invalid repository tool arguments")
var errSourceApprovalRequired = errors.New("source approval required")

type Handler struct {
	Enrollment     *enrollment.Service
	Projects       ProjectReader
	Workspaces     WorkspaceCommands
	Infrastructure InfrastructureCommands
	SourceChanges  SourceChangeCommands
	MergeRequests  RepositoryMergeRequests
	MaxBodyBytes   int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	projectID, action, ok := route(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	claims, ok := h.authenticate(r, projectID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "valid project access credential is required")
		return
	}
	switch {
	case action == "tools" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"api_version": agentv2.APIVersion, "semantics_version": agentv2.SemanticsVersion, "tools": agentv2.ToolCatalog()})
	case action == "invoke" && r.Method == http.MethodPost:
		h.invoke(w, r, claims)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h Handler) authenticate(r *http.Request, projectID string) (enrollment.AccessClaims, bool) {
	if h.Enrollment == nil {
		return enrollment.AccessClaims{}, false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return enrollment.AccessClaims{}, false
	}
	claims, err := h.Enrollment.AuthenticateAccess(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), projectID)
	return claims, err == nil
}

func (h Handler) invoke(w http.ResponseWriter, r *http.Request, claims enrollment.AccessClaims) {
	var request agentv2.InvocationRequest
	if err := decode(r, h.limit(), &request); err != nil || request.Validate() != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid MCP v2 invocation")
		return
	}
	granted := make(map[string]struct{}, len(claims.Scopes))
	for _, scope := range claims.Scopes {
		granted[scope] = struct{}{}
	}
	verified := agentv2.VerifiedInvocationContext{
		TenantID: claims.TenantID, ProjectID: claims.ProjectID, UserID: claims.UserID,
		AgentID: claims.AgentID, GrantedScopes: granted, CredentialID: claims.CredentialID,
	}
	if err := verified.Authorize(request.Tool); err != nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "tool scope is not granted")
		return
	}
	var (
		result any
		err    error
	)
	switch request.Tool {
	case agentv2.ToolProjectGet:
		if h.Projects == nil {
			err = errors.New("project reader is unavailable")
			break
		}
		result, err = h.Projects.GetProject(r.Context(), verified.TenantID, verified.ProjectID)
	case agentv2.ToolRepositoryStatus:
		if h.Projects == nil {
			err = errors.New("project reader is unavailable")
			break
		}
		result, err = h.Projects.GetRepositoryForProject(r.Context(), verified.TenantID, verified.ProjectID)
	case agentv2.ToolRepositoryDiff, agentv2.ToolRepositoryApplyPatch, agentv2.ToolRepositoryCreateBranch, agentv2.ToolRepositoryCommit, agentv2.ToolRepositoryPush, agentv2.ToolRepositoryCreateMergeRequest:
		result, err = h.invokeRepository(r.Context(), verified, request)
	case agentv2.ToolWorkspaceCreate, agentv2.ToolWorkspaceGet, agentv2.ToolWorkspaceExec, agentv2.ToolWorkspaceDestroy:
		result, err = h.invokeWorkspace(r.Context(), verified, request)
	case agentv2.ToolInfraPlan, agentv2.ToolInfraGetPlan, agentv2.ToolInfraApply, agentv2.ToolApprovalRequest, agentv2.ToolApprovalGet:
		result, err = h.invokeInfrastructure(r.Context(), verified, request)
	default:
		writeResponse(w, http.StatusNotImplemented, agentv2.InvocationResponse{
			APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID,
			Error: &kernelv2.PublicError{Code: "NOT_IMPLEMENTED", Message: "tool execution adapter is not available in this beta slice", Retryable: false},
		})
		return
	}
	if err != nil {
		status, code, message, retryable := http.StatusServiceUnavailable, "UNAVAILABLE", "project operation failed", true
		switch {
		case errors.Is(err, errInvalidRepositoryArguments):
			status, code, message, retryable = http.StatusBadRequest, "INVALID_ARGUMENT", "invalid repository tool arguments", false
		case errors.Is(err, errSourceApprovalRequired), errors.Is(err, workspace.ErrSourceApprovalRequired):
			status, code, message, retryable = http.StatusPreconditionRequired, "APPROVAL_REQUIRED", "production GitOps path requires an exact source approval", false
		case errors.Is(err, errInvalidInfrastructureArguments):
			status, code, message, retryable = http.StatusBadRequest, "INVALID_ARGUMENT", "invalid infrastructure tool arguments", false
		case errors.Is(err, infraapp.ErrNotFound):
			status, code, message, retryable = http.StatusNotFound, "NOT_FOUND", "infrastructure record not found", false
		case errors.Is(err, infraapp.ErrApprovalRequired):
			status, code, message, retryable = http.StatusPreconditionRequired, "APPROVAL_REQUIRED", "exact-plan approval is required", false
		case errors.Is(err, infraapp.ErrPermissionDenied):
			status, code, message, retryable = http.StatusForbidden, "POLICY_DENIED", "infrastructure authorization does not match exact plan", false
		case errors.Is(err, infraapp.ErrConflict):
			status, code, message, retryable = http.StatusConflict, "CONFLICT", "infrastructure operation conflicts with current state", false
		case errors.Is(err, errInvalidWorkspaceArguments):
			status, code, message, retryable = http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace tool arguments", false
		case errors.Is(err, workspace.ErrNotFound):
			status, code, message, retryable = http.StatusNotFound, "NOT_FOUND", "workspace resource not found", false
		case errors.Is(err, workspace.ErrConflict), errors.Is(err, workspace.ErrStatefulCommandBusy):
			status, code, message, retryable = http.StatusConflict, "CONFLICT", "workspace operation conflicts with current state", false
		case errors.Is(err, workspace.ErrPolicyDenied):
			status, code, message, retryable = http.StatusForbidden, "POLICY_DENIED", "workspace command is denied by policy", false
		case domain.HasCode(err, domain.CodeInvalidArgument):
			status, code, message, retryable = http.StatusBadRequest, "INVALID_ARGUMENT", "source operation is invalid", false
		case domain.HasCode(err, domain.CodeNotFound):
			status, code, message, retryable = http.StatusNotFound, "NOT_FOUND", "source resource not found", false
		case domain.HasCode(err, domain.CodeConflict), domain.HasCode(err, domain.CodeStaleVersion):
			status, code, message, retryable = http.StatusConflict, "CONFLICT", "source operation conflicts with current repository state", false
		}
		writeResponse(w, status, agentv2.InvocationResponse{
			APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID,
			Error: &kernelv2.PublicError{Code: code, Message: message, Retryable: retryable},
		})
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "response encoding failed")
		return
	}
	writeResponse(w, http.StatusOK, agentv2.InvocationResponse{APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID, Result: raw})
}

type repositoryWorkspaceArguments struct {
	WorkspaceID      string            `json:"workspace_id"`
	WorkingDir       string            `json:"working_dir,omitempty"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	CredentialLeases []string          `json:"credential_leases,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64             `json:"output_limit_bytes,omitempty"`
}

type repositoryBranchArguments struct {
	WorkspaceID      string            `json:"workspace_id"`
	Branch           string            `json:"branch"`
	WorkingDir       string            `json:"working_dir,omitempty"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	CredentialLeases []string          `json:"credential_leases,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64             `json:"output_limit_bytes,omitempty"`
}

type repositoryPatchArguments struct {
	WorkspaceID      string                   `json:"workspace_id"`
	TargetBranch     string                   `json:"target_branch"`
	Files            []sourcev2.PatchMutation `json:"files"`
	WorkingDir       string                   `json:"working_dir,omitempty"`
	TimeoutSeconds   int64                    `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64                    `json:"output_limit_bytes,omitempty"`
}

type repositoryCommitArguments struct {
	WorkspaceID      string `json:"workspace_id"`
	ChangePlanID     string `json:"change_plan_id"`
	Branch           string `json:"branch"`
	Message          string `json:"message"`
	WorkingDir       string `json:"working_dir,omitempty"`
	TimeoutSeconds   int64  `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64  `json:"output_limit_bytes,omitempty"`
}

type repositoryPushArguments struct {
	WorkspaceID       string            `json:"workspace_id"`
	ChangePlanID      string            `json:"change_plan_id"`
	Branch            string            `json:"branch"`
	CommitSHA         string            `json:"commit_sha"`
	ExpectedRemoteSHA string            `json:"expected_remote_sha"`
	WorkingDir        string            `json:"working_dir,omitempty"`
	EnvironmentRefs   map[string]string `json:"environment_refs"`
	CredentialLeases  []string          `json:"credential_leases"`
	TimeoutSeconds    int64             `json:"timeout_seconds,omitempty"`
	OutputLimitBytes  int64             `json:"output_limit_bytes,omitempty"`
}

type repositoryMergeRequestArguments struct {
	WorkspaceID     string `json:"workspace_id"`
	ChangePlanID    string `json:"change_plan_id"`
	CommitCommandID string `json:"commit_command_id"`
	SourceBranch    string `json:"source_branch"`
	Title           string `json:"title"`
}

func (h Handler) invokeRepository(ctx context.Context, verified agentv2.VerifiedInvocationContext, request agentv2.InvocationRequest) (any, error) {
	if h.Workspaces == nil || h.Projects == nil || h.SourceChanges == nil {
		return nil, errors.New("repository workspace service is unavailable")
	}
	scope := workspace.Scope{TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID}
	repository, err := h.Projects.GetRepositoryForProject(ctx, verified.TenantID, verified.ProjectID)
	if err != nil {
		return nil, err
	}
	queue := func(workspaceID, idempotencyKey, kind, serializationKey string, argv []string, workingDir string, environmentRefs map[string]string, leases []string, timeout, outputLimit int64) (workspacev1.CommandView, error) {
		if timeout == 0 {
			timeout = 300
		}
		if outputLimit == 0 {
			outputLimit = 1 << 20
		}
		spec := workspacev1.CommandSpec{Argv: argv, WorkingDir: workingDir, EnvironmentRefs: cloneStringMap(environmentRefs), TimeoutSeconds: timeout, OutputLimitBytes: outputLimit}
		if strings.TrimSpace(workspaceID) == "" || timeout > 600 || spec.Validate() != nil {
			return workspacev1.CommandView{}, errInvalidRepositoryArguments
		}
		return h.Workspaces.Exec(ctx, workspace.ExecRequest{Scope: scope, WorkspaceID: workspaceID, IdempotencyKey: idempotencyKey, Kind: kind, SerializationKey: serializationKey, CredentialLeases: append([]string(nil), leases...), Spec: spec})
	}
	revisionFor := func(workspaceID string) (sourcev2.SourceRevision, error) {
		revision, revisionErr := h.Workspaces.GetSourceRevision(ctx, scope, workspaceID)
		if revisionErr != nil {
			return sourcev2.SourceRevision{}, revisionErr
		}
		if revision.RepositoryID != repository.ID {
			return sourcev2.SourceRevision{}, workspace.ErrPolicyDenied
		}
		return revision, nil
	}
	switch request.Tool {
	case agentv2.ToolRepositoryCreateBranch:
		var arguments repositoryBranchArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil || !sourcev2.ValidBranch(arguments.Branch) || !exactGitCredentialRefs(arguments.EnvironmentRefs, arguments.CredentialLeases) {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !matchesSourceRoot(arguments.WorkingDir, revision.SourceRoot) {
			return nil, errInvalidRepositoryArguments
		}
		workingDir := revision.SourceRoot
		return queue(arguments.WorkspaceID, request.IdempotencyKey, "repository_checkout", "repository:"+repository.ID,
			[]string{"workspace-agent", "verified-git-checkout", repository.ID, strings.TrimRight(repository.WebURL, "/") + ".git", revision.CommitSHA, arguments.Branch},
			workingDir, arguments.EnvironmentRefs, arguments.CredentialLeases, arguments.TimeoutSeconds, arguments.OutputLimitBytes)
	case agentv2.ToolRepositoryDiff:
		var arguments repositoryWorkspaceArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !matchesSourceRoot(arguments.WorkingDir, revision.SourceRoot) {
			return nil, errInvalidRepositoryArguments
		}
		return queue(arguments.WorkspaceID, request.IdempotencyKey, "repository_diff", "", []string{"git", "diff", "--no-ext-diff", "--stat", "--"}, revision.SourceRoot, nil, nil, arguments.TimeoutSeconds, arguments.OutputLimitBytes)
	case agentv2.ToolRepositoryApplyPatch:
		var arguments repositoryPatchArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil || !sourcev2.ValidBranch(arguments.TargetBranch) || len(arguments.Files) == 0 || len(arguments.Files) > 126 {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !matchesSourceRoot(arguments.WorkingDir, revision.SourceRoot) {
			return nil, errInvalidRepositoryArguments
		}
		argv := []string{"workspace-agent", "verified-git-apply-patch", revision.CommitSHA}
		for _, file := range arguments.Files {
			if file.Validate() != nil {
				return nil, errInvalidRepositoryArguments
			}
			raw, _ := json.Marshal(file)
			encoded := base64.RawURLEncoding.EncodeToString(raw)
			if len(encoded) > 4096 {
				return nil, errInvalidRepositoryArguments
			}
			argv = append(argv, encoded)
		}
		plan, err := h.SourceChanges.PlanSourceChange(ctx, workspace.SourceChangePlanRequest{
			Scope: scope, WorkspaceID: arguments.WorkspaceID, RepositoryID: repository.ID, BaseSHA: revision.CommitSHA,
			TargetBranch: arguments.TargetBranch, TaskID: request.TaskID, Files: arguments.Files, IdempotencyKey: request.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		if plan.RequiresApproval {
			if request.ApprovalGrantID == "" {
				return map[string]any{"status": "WAITING_APPROVAL", "plan": plan}, nil
			}
			if _, err := h.SourceChanges.CheckSourceApproval(ctx, scope, plan.PlanID, request.ApprovalGrantID); err != nil {
				return nil, err
			}
		}
		command, err := queue(arguments.WorkspaceID, request.IdempotencyKey, "repository_patch", "repository:"+repository.ID, argv, revision.SourceRoot, nil, nil, arguments.TimeoutSeconds, arguments.OutputLimitBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plan": plan, "command": command}, nil
	case agentv2.ToolRepositoryCommit:
		var arguments repositoryCommitArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil || strings.TrimSpace(arguments.ChangePlanID) == "" || !sourcev2.ValidBranch(arguments.Branch) || !validCommitMessage(arguments.Message) {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !matchesSourceRoot(arguments.WorkingDir, revision.SourceRoot) {
			return nil, errInvalidRepositoryArguments
		}
		plan, err := h.SourceChanges.GetSourceChangePlan(ctx, verified.TenantID, verified.ProjectID, arguments.ChangePlanID)
		if err != nil {
			return nil, err
		}
		authorized, err := h.SourceChanges.AuthorizeSourceCommit(ctx, workspace.AuthorizeSourceCommitRequest{
			Scope: scope, WorkspaceID: arguments.WorkspaceID, RepositoryID: repository.ID, BaseSHA: revision.CommitSHA,
			TargetBranch: arguments.Branch, TaskID: request.TaskID, PlanID: plan.PlanID, PlanHash: plan.PlanHash,
			ApprovalGrantID: request.ApprovalGrantID, IdempotencyKey: request.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		command, err := queue(arguments.WorkspaceID, request.IdempotencyKey, "repository_commit", "repository:"+repository.ID,
			[]string{"workspace-agent", "verified-git-commit", repository.ID, revision.CommitSHA, arguments.Branch, verified.AgentID, request.TaskID, request.CorrelationID, authorized.PlanHash, arguments.Message},
			revision.SourceRoot, nil, nil, arguments.TimeoutSeconds, arguments.OutputLimitBytes)
		if err != nil {
			return nil, err
		}
		receipt, receiptErr := h.Workspaces.GetCommitReceipt(ctx, scope, command.CommandID)
		if receiptErr == nil {
			return map[string]any{"command": command, "receipt": receipt}, nil
		}
		if !errors.Is(receiptErr, workspace.ErrNotFound) {
			return nil, receiptErr
		}
		return map[string]any{"status": "WAITING_DEPENDENCY", "command": command}, nil
	case agentv2.ToolRepositoryPush:
		var arguments repositoryPushArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil || strings.TrimSpace(arguments.ChangePlanID) == "" || !sourcev2.ValidBranch(arguments.Branch) || !validCommitSHA(arguments.CommitSHA) || arguments.ExpectedRemoteSHA != "absent" && !validCommitSHA(arguments.ExpectedRemoteSHA) || !exactGitCredentialRefs(arguments.EnvironmentRefs, arguments.CredentialLeases) {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !matchesSourceRoot(arguments.WorkingDir, revision.SourceRoot) {
			return nil, errInvalidRepositoryArguments
		}
		plan, err := h.SourceChanges.GetSourceChangePlan(ctx, verified.TenantID, verified.ProjectID, arguments.ChangePlanID)
		if err != nil {
			return nil, err
		}
		if plan.WorkspaceID != arguments.WorkspaceID || plan.RepositoryID != repository.ID || plan.BaseSHA != revision.CommitSHA || plan.TargetBranch != arguments.Branch || plan.ActorID != verified.AgentID || plan.TaskID != request.TaskID || plan.AuthorizedAt.IsZero() {
			return nil, workspace.ErrPolicyDenied
		}
		return queue(arguments.WorkspaceID, request.IdempotencyKey, "repository_push", "repository:"+repository.ID,
			[]string{"workspace-agent", "verified-git-push", repository.ID, strings.TrimRight(repository.WebURL, "/") + ".git", arguments.Branch, revision.CommitSHA, arguments.CommitSHA, plan.PlanHash, arguments.ExpectedRemoteSHA},
			revision.SourceRoot, arguments.EnvironmentRefs, arguments.CredentialLeases, arguments.TimeoutSeconds, arguments.OutputLimitBytes)
	case agentv2.ToolRepositoryCreateMergeRequest:
		if h.MergeRequests == nil {
			return nil, errors.New("merge request service is unavailable")
		}
		var arguments repositoryMergeRequestArguments
		if agentv2.DecodeStrict(request.Arguments, &arguments) != nil || strings.TrimSpace(arguments.WorkspaceID) == "" || strings.TrimSpace(arguments.ChangePlanID) == "" || strings.TrimSpace(arguments.CommitCommandID) == "" || !sourcev2.ValidBranch(arguments.SourceBranch) || !validMergeRequestTitle(arguments.Title) {
			return nil, errInvalidRepositoryArguments
		}
		revision, err := revisionFor(arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		plan, err := h.SourceChanges.GetSourceChangePlan(ctx, verified.TenantID, verified.ProjectID, arguments.ChangePlanID)
		if err != nil {
			return nil, err
		}
		receipt, err := h.Workspaces.GetCommitReceipt(ctx, scope, arguments.CommitCommandID)
		if err != nil {
			return nil, err
		}
		statement := receipt.Statement
		if plan.WorkspaceID != arguments.WorkspaceID || plan.RepositoryID != repository.ID || plan.BaseSHA != revision.CommitSHA || plan.TargetBranch != arguments.SourceBranch || plan.ActorID != verified.AgentID || plan.TaskID != request.TaskID || plan.AuthorizedAt.IsZero() ||
			statement.RepositoryID != repository.ID || statement.BaseSHA != revision.CommitSHA || statement.Branch != arguments.SourceBranch || statement.AgentID != verified.AgentID || statement.TaskID != request.TaskID || statement.CorrelationID != request.CorrelationID || statement.SourcePlanHash != plan.PlanHash || receipt.CommandID != arguments.CommitCommandID {
			return nil, workspace.ErrPolicyDenied
		}
		return h.MergeRequests.CreateMergeRequest(ctx, sourceapp.CreateMergeRequestCommand{
			TenantID: verified.TenantID, ActorID: verified.AgentID, RepositoryID: repository.ID,
			SourceBranch: arguments.SourceBranch, TargetBranch: repository.DefaultBranch,
			ExpectedHeadSHA: statement.CommitSHA, SourcePlanHash: plan.PlanHash,
			TaskID: request.TaskID, CorrelationID: request.CorrelationID, Title: arguments.Title,
			IdempotencyKey: request.IdempotencyKey,
		})
	default:
		return nil, errors.New("repository tool is unavailable")
	}
}

func exactGitCredentialRefs(refs map[string]string, leases []string) bool {
	if len(refs) != 2 || len(leases) == 0 {
		return false
	}
	for _, name := range []string{"GIT_USERNAME", "GIT_TOKEN"} {
		if !strings.HasPrefix(refs[name], "credential://") {
			return false
		}
	}
	return true
}

func matchesSourceRoot(requested, bound string) bool { return requested == "" || requested == bound }

func protectedSourcePath(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean == "deploy/environments/production" || strings.HasPrefix(clean, "deploy/environments/production/")
}

func validCommitMessage(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func validMergeRequestTitle(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\x00")
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

type workspaceCreateArguments struct {
	ImageDigest      string   `json:"image_digest"`
	RepositoryID     string   `json:"repository_id"`
	CommitSHA        string   `json:"commit_sha"`
	SourceRoot       string   `json:"source_root,omitempty"`
	CPUMillis        int64    `json:"cpu_millis"`
	MemoryMiB        int64    `json:"memory_mib"`
	TTLSeconds       int64    `json:"ttl_seconds"`
	NetworkProfile   string   `json:"network_profile"`
	CredentialLeases []string `json:"credential_leases,omitempty"`
}

type workspaceGetArguments struct {
	WorkspaceID string `json:"workspace_id"`
}

type workspaceExecArguments struct {
	WorkspaceID      string            `json:"workspace_id"`
	Argv             []string          `json:"argv"`
	WorkingDir       string            `json:"working_dir"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds"`
	OutputLimitBytes int64             `json:"output_limit_bytes"`
	Kind             string            `json:"kind"`
	SerializationKey string            `json:"serialization_key,omitempty"`
	CredentialLeases []string          `json:"credential_leases,omitempty"`
}

func (h Handler) invokeWorkspace(ctx context.Context, verified agentv2.VerifiedInvocationContext, request agentv2.InvocationRequest) (any, error) {
	if h.Workspaces == nil {
		return nil, errors.New("workspace service is unavailable")
	}
	scope := workspace.Scope{TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID}
	switch request.Tool {
	case agentv2.ToolWorkspaceCreate:
		var arguments workspaceCreateArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || h.Projects == nil {
			return nil, errInvalidWorkspaceArguments
		}
		repository, err := h.Projects.GetRepositoryForProject(ctx, verified.TenantID, verified.ProjectID)
		if err != nil || arguments.RepositoryID != repository.ID {
			return nil, errInvalidWorkspaceArguments
		}
		revision := sourcev2.SourceRevision{RepositoryID: repository.ID, CommitSHA: arguments.CommitSHA, SourceRoot: arguments.SourceRoot}
		spec := workspacev1.WorkspaceSpec{
			ProjectID: verified.ProjectID, TaskID: request.TaskID, ImageDigest: arguments.ImageDigest,
			CPUMillis: arguments.CPUMillis, MemoryMiB: arguments.MemoryMiB, TTLSeconds: arguments.TTLSeconds,
			NetworkProfile: arguments.NetworkProfile, CredentialLeases: append([]string(nil), arguments.CredentialLeases...), SourceRevision: &revision,
		}
		if spec.Validate() != nil {
			return nil, errInvalidWorkspaceArguments
		}
		return h.Workspaces.Create(ctx, workspace.CreateRequest{Scope: scope, IdempotencyKey: request.IdempotencyKey, Spec: spec})
	case agentv2.ToolWorkspaceGet:
		var arguments workspaceGetArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.WorkspaceID) == "" {
			return nil, errInvalidWorkspaceArguments
		}
		return h.Workspaces.Get(ctx, scope, arguments.WorkspaceID)
	case agentv2.ToolWorkspaceExec:
		var arguments workspaceExecArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil {
			return nil, errInvalidWorkspaceArguments
		}
		spec := workspacev1.CommandSpec{Argv: append([]string(nil), arguments.Argv...), WorkingDir: arguments.WorkingDir,
			EnvironmentRefs: cloneStringMap(arguments.EnvironmentRefs), TimeoutSeconds: arguments.TimeoutSeconds, OutputLimitBytes: arguments.OutputLimitBytes}
		if strings.TrimSpace(arguments.WorkspaceID) == "" || strings.TrimSpace(arguments.Kind) == "" || spec.Validate() != nil || requiresGovernedInfrastructureTool(arguments.Kind, spec.Argv) || requiresGovernedSourceTool(spec.Argv) {
			return nil, errInvalidWorkspaceArguments
		}
		return h.Workspaces.Exec(ctx, workspace.ExecRequest{
			Scope: scope, WorkspaceID: arguments.WorkspaceID, IdempotencyKey: request.IdempotencyKey,
			Kind: arguments.Kind, SerializationKey: arguments.SerializationKey, CredentialLeases: append([]string(nil), arguments.CredentialLeases...),
			Spec: spec,
		})
	case agentv2.ToolWorkspaceDestroy:
		var arguments workspaceGetArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.WorkspaceID) == "" {
			return nil, errInvalidWorkspaceArguments
		}
		return h.Workspaces.Destroy(ctx, scope, arguments.WorkspaceID)
	default:
		return nil, errors.New("workspace tool is unavailable")
	}
}

type infraPlanArguments struct {
	WorkspaceID      string            `json:"workspace_id"`
	Target           string            `json:"target"`
	SourceSHA        string            `json:"source_sha"`
	StateGeneration  int64             `json:"state_generation"`
	PlanPath         string            `json:"plan_path"`
	WorkingDir       string            `json:"working_dir"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	CredentialLeases []string          `json:"credential_leases,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64             `json:"output_limit_bytes,omitempty"`
}

type infraPlanLookupArguments struct {
	PlanID string `json:"plan_id"`
}

type infraApplyArguments struct {
	PlanID           string            `json:"plan_id"`
	PlanHash         string            `json:"plan_hash"`
	EstimateVersion  string            `json:"estimate_version"`
	ReservationID    string            `json:"reservation_id"`
	Target           string            `json:"target"`
	PlanPath         string            `json:"plan_path"`
	WorkingDir       string            `json:"working_dir"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	CredentialLeases []string          `json:"credential_leases,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64             `json:"output_limit_bytes,omitempty"`
}

type infrastructurePlanView struct {
	Summary        infrastructurev1.PlanSummary `json:"summary"`
	Estimate       any                          `json:"estimate"`
	Reservation    any                          `json:"reservation"`
	Target         string                       `json:"target"`
	ArtifactDigest string                       `json:"artifact_digest"`
	ApplyStartedAt time.Time                    `json:"apply_started_at,omitempty"`
}

func (h Handler) invokeInfrastructure(ctx context.Context, verified agentv2.VerifiedInvocationContext, request agentv2.InvocationRequest) (any, error) {
	if h.Infrastructure == nil {
		return nil, errors.New("infrastructure service is unavailable")
	}
	switch request.Tool {
	case agentv2.ToolInfraPlan:
		var arguments infraPlanArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || !validRelativePlanPath(arguments.PlanPath) || h.Workspaces == nil {
			return nil, errInvalidInfrastructureArguments
		}
		revision, err := h.Workspaces.GetSourceRevision(ctx, workspace.Scope{
			TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID,
		}, arguments.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if arguments.SourceSHA != revision.CommitSHA {
			return nil, infraapp.ErrPermissionDenied
		}
		timeout := arguments.TimeoutSeconds
		if timeout == 0 {
			timeout = 600
		}
		outputLimit := arguments.OutputLimitBytes
		if outputLimit == 0 {
			outputLimit = 1 << 20
		}
		spec := workspacev1.CommandSpec{
			Argv:       []string{"workspace-agent", "verified-tofu-plan", arguments.PlanPath, revision.CommitSHA},
			WorkingDir: arguments.WorkingDir, EnvironmentRefs: cloneStringMap(arguments.EnvironmentRefs),
			TimeoutSeconds: timeout, OutputLimitBytes: outputLimit,
		}
		if timeout > 600 || spec.Validate() != nil {
			return nil, errInvalidInfrastructureArguments
		}
		command, err := h.Workspaces.Exec(ctx, workspace.ExecRequest{
			Scope:       workspace.Scope{TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID},
			WorkspaceID: arguments.WorkspaceID, IdempotencyKey: request.IdempotencyKey,
			Kind: "infra_plan", SerializationKey: arguments.Target,
			CredentialLeases: append([]string(nil), arguments.CredentialLeases...), Spec: spec,
		})
		if err != nil {
			return nil, err
		}
		result, err := h.Infrastructure.PlanFromReceipt(ctx, infraapp.ReceiptPlanCommand{
			TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID,
			WorkspaceID: arguments.WorkspaceID, CommandID: command.CommandID, Target: arguments.Target,
			SourceSHA: arguments.SourceSHA, IdempotencyKey: request.IdempotencyKey, StateGeneration: arguments.StateGeneration,
		})
		if errors.Is(err, infraapp.ErrDependencyPending) {
			return map[string]any{"status": "WAITING_DEPENDENCY", "command": command}, nil
		}
		return result, err
	case agentv2.ToolInfraGetPlan:
		plan, err := h.infrastructurePlan(ctx, verified, request.Arguments)
		if err != nil {
			return nil, err
		}
		return planView(plan), nil
	case agentv2.ToolApprovalRequest, agentv2.ToolApprovalGet:
		var arguments infraPlanLookupArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.PlanID) == "" {
			return nil, errInvalidInfrastructureArguments
		}
		return h.Infrastructure.GetApprovalStatus(ctx, verified.TenantID, verified.ProjectID, arguments.PlanID, verified.AgentID)
	case agentv2.ToolInfraApply:
		var arguments infraApplyArguments
		if err := agentv2.DecodeStrict(request.Arguments, &arguments); err != nil || !validRelativePlanPath(arguments.PlanPath) {
			return nil, errInvalidInfrastructureArguments
		}
		plan, err := h.Infrastructure.GetPlan(ctx, verified.TenantID, verified.ProjectID, arguments.PlanID)
		if err != nil {
			return nil, err
		}
		if plan.Summary.WorkspaceID == "" || plan.RequestedByActorID != verified.AgentID {
			return nil, infraapp.ErrPermissionDenied
		}
		if h.Workspaces == nil {
			return nil, errors.New("workspace service is unavailable")
		}
		timeout := arguments.TimeoutSeconds
		if timeout == 0 {
			timeout = 3600
		}
		outputLimit := arguments.OutputLimitBytes
		if outputLimit == 0 {
			outputLimit = 8 << 20
		}
		spec := workspacev1.CommandSpec{
			Argv:       []string{"workspace-agent", "verified-tofu-apply", plan.ArtifactDigest, arguments.PlanPath},
			WorkingDir: arguments.WorkingDir, EnvironmentRefs: cloneStringMap(arguments.EnvironmentRefs),
			TimeoutSeconds: timeout, OutputLimitBytes: outputLimit,
		}
		if spec.Validate() != nil {
			return nil, errInvalidInfrastructureArguments
		}
		authorization := infrastructurev1.ApplyAuthorization{
			PlanID: arguments.PlanID, PlanHash: arguments.PlanHash, EstimateVersion: arguments.EstimateVersion,
			ReservationID: arguments.ReservationID, ApprovalGrantID: request.ApprovalGrantID,
			Target: arguments.Target, ActorID: verified.AgentID, ExpiresAt: plan.Reservation.ExpiresAt,
		}
		started, err := h.Infrastructure.AuthorizeApply(ctx, infraapp.ApplyCommand{
			TenantID: verified.TenantID, ProjectID: verified.ProjectID,
			IdempotencyKey: request.IdempotencyKey, Authorization: authorization,
		})
		if err != nil {
			return nil, err
		}
		return h.Workspaces.Exec(ctx, workspace.ExecRequest{
			Scope:       workspace.Scope{TenantID: verified.TenantID, ProjectID: verified.ProjectID, ActorID: verified.AgentID},
			WorkspaceID: started.Summary.WorkspaceID, IdempotencyKey: request.IdempotencyKey,
			Kind: "infra_apply", SerializationKey: started.Target,
			CredentialLeases: append([]string(nil), arguments.CredentialLeases...), Spec: spec,
		})
	default:
		return nil, errors.New("infrastructure tool is unavailable")
	}
}

func (h Handler) infrastructurePlan(ctx context.Context, verified agentv2.VerifiedInvocationContext, raw json.RawMessage) (infraapp.PlanRecord, error) {
	var arguments infraPlanLookupArguments
	if err := agentv2.DecodeStrict(raw, &arguments); err != nil || strings.TrimSpace(arguments.PlanID) == "" {
		return infraapp.PlanRecord{}, errInvalidInfrastructureArguments
	}
	return h.Infrastructure.GetPlan(ctx, verified.TenantID, verified.ProjectID, arguments.PlanID)
}

func planView(plan infraapp.PlanRecord) infrastructurePlanView {
	return infrastructurePlanView{Summary: plan.Summary, Estimate: plan.Estimate, Reservation: plan.Reservation, Target: plan.Target, ArtifactDigest: plan.ArtifactDigest, ApplyStartedAt: plan.ApplyStartedAt}
}

func validRelativePlanPath(value string) bool {
	clean := filepath.Clean(strings.TrimSpace(value))
	return clean != "" && clean != "." && clean == value && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func requiresGovernedInfrastructureTool(kind string, argv []string) bool {
	if kind == "infra_apply" || kind == "infra_destroy" || kind == "infra_import" || len(argv) == 0 || argv[0] == "workspace-agent" {
		return true
	}
	if argv[0] != "tofu" {
		return false
	}
	if len(argv) < 2 {
		return true
	}
	allowed := map[string]struct{}{"fmt": {}, "validate": {}, "plan": {}, "show": {}, "providers": {}, "version": {}, "graph": {}, "output": {}}
	_, ok := allowed[strings.ToLower(strings.TrimSpace(argv[1]))]
	return !ok
}

func requiresGovernedSourceTool(argv []string) bool {
	if len(argv) == 0 || argv[0] != "git" {
		return false
	}
	if len(argv) < 2 {
		return true
	}
	// All repository mutation and remote access goes through Project MCP
	// receipts, exact-base policy and attestation. Generic exec is limited to
	// local, read-only inspection and cannot smuggle global git options.
	allowed := map[string]struct{}{
		"diff": {}, "grep": {}, "log": {}, "ls-files": {}, "rev-parse": {}, "show": {}, "status": {},
	}
	_, ok := allowed[strings.ToLower(strings.TrimSpace(argv[1]))]
	return !ok
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func route(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 5 || parts[0] != "projects" || parts[2] != "mcp" || parts[3] != "v2" || (parts[4] != "tools" && parts[4] != "invoke") || parts[1] == "" {
		return "", "", false
	}
	return parts[1], parts[4], true
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 || h.MaxBodyBytes > agentv2.MaximumArgumentsBytes+(64<<10) {
		return agentv2.MaximumArgumentsBytes + (64 << 10)
	}
	return h.MaxBodyBytes
}

func decode(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeResponse(w http.ResponseWriter, status int, response agentv2.InvocationResponse) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
