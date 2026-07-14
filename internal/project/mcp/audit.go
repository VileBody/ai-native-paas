package mcp

import (
	"encoding/json"

	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

// projectToolAuditEvidence extracts only stable, non-secret references. It
// deliberately does not persist argv, environment_refs, credential leases,
// source patches, secret values, plan JSON or provider response bodies.
func projectToolAuditEvidence(request agentv2.InvocationRequest, result any) agentv1.AuditEvidence {
	var evidence agentv1.AuditEvidence
	var arguments struct {
		WorkspaceID  string `json:"workspace_id"`
		PlanID       string `json:"plan_id"`
		ChangePlanID string `json:"change_plan_id"`
		PlanHash     string `json:"plan_hash"`
	}
	_ = json.Unmarshal(request.Arguments, &arguments)
	evidence.WorkspaceID = arguments.WorkspaceID
	evidence.PlanID = firstNonEmpty(arguments.PlanID, arguments.ChangePlanID)
	evidence.PlanHash = arguments.PlanHash
	if request.ApprovalGrantID != "" {
		evidence.ApprovalGrantID = request.ApprovalGrantID
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return evidence
	}
	var references struct {
		WorkspaceID      string `json:"workspace_id"`
		CommandID        string `json:"command_id"`
		RepositoryID     string `json:"repository_id"`
		CommitSHA        string `json:"commit_sha"`
		HeadSHA          string `json:"head_sha"`
		BuildID          string `json:"build_id"`
		ArtifactDigest   string `json:"artifact_digest"`
		PlanID           string `json:"plan_id"`
		PlanHash         string `json:"plan_hash"`
		ApprovalGrantID  string `json:"approval_grant_id"`
		ApplyOperationID string `json:"apply_operation_id"`
		DeploymentID     string `json:"deployment_id"`
		ReleaseID        string `json:"release_id"`
		GitOpsRevision   string `json:"gitops_revision"`
		ReadyEndpoint    string `json:"ready_endpoint"`
		URL              string `json:"url"`
		Summary          struct {
			PlanID   string `json:"plan_id"`
			PlanHash string `json:"plan_hash"`
		} `json:"summary"`
		Plan struct {
			PlanID   string `json:"plan_id"`
			PlanHash string `json:"plan_hash"`
		} `json:"plan"`
		Command struct {
			WorkspaceID string `json:"workspace_id"`
			CommandID   string `json:"command_id"`
		} `json:"command"`
		Receipt struct {
			CommandID string `json:"command_id"`
			Statement struct {
				RepositoryID string `json:"repository_id"`
				CommitSHA    string `json:"commit_sha"`
			} `json:"statement"`
		} `json:"receipt"`
	}
	if json.Unmarshal(raw, &references) != nil {
		return evidence
	}
	evidence.WorkspaceID = firstNonEmpty(references.WorkspaceID, references.Command.WorkspaceID, evidence.WorkspaceID)
	evidence.WorkspaceCommandID = firstNonEmpty(references.CommandID, references.Command.CommandID, references.Receipt.CommandID)
	evidence.RepositoryID = firstNonEmpty(references.RepositoryID, references.Receipt.Statement.RepositoryID)
	evidence.CommitSHA = firstNonEmpty(references.CommitSHA, references.HeadSHA, references.Receipt.Statement.CommitSHA)
	evidence.BuildID = references.BuildID
	evidence.ArtifactDigest = references.ArtifactDigest
	evidence.PlanID = firstNonEmpty(references.PlanID, references.Summary.PlanID, references.Plan.PlanID, evidence.PlanID)
	evidence.PlanHash = firstNonEmpty(references.PlanHash, references.Summary.PlanHash, references.Plan.PlanHash, evidence.PlanHash)
	evidence.ApprovalGrantID = firstNonEmpty(references.ApprovalGrantID, evidence.ApprovalGrantID)
	evidence.ApplyOperationID = references.ApplyOperationID
	evidence.DeploymentID = references.DeploymentID
	evidence.ReleaseID = references.ReleaseID
	evidence.GitOpsRevision = references.GitOpsRevision
	if request.Tool == agentv2.ToolArgoCDGetStatus || request.Tool == agentv2.ToolDeploymentHTTPProbe {
		evidence.ReadyEndpoint = firstNonEmpty(references.ReadyEndpoint, references.URL)
	}
	return evidence
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
