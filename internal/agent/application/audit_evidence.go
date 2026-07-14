package application

import (
	"encoding/json"

	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func invocationAuditEvidence(request agentv1.InvocationRequest, response agentv1.InvocationResponse) agentv1.AuditEvidence {
	var evidence agentv1.AuditEvidence
	if response.Result == nil {
		return validatedInvocationEvidence(evidence)
	}
	switch request.Tool {
	case agentv1.ToolCreateProject, agentv1.ToolGetProject:
		var value ProjectRef
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.ProjectID, evidence.RepositoryID = value.ProjectID, value.RepositoryID
		}
	case agentv1.ToolApplyRepositoryPatch, agentv1.ToolCreateBranch:
		var value CommitRef
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.ProjectID, evidence.RepositoryID, evidence.CommitSHA = value.ProjectID, value.RepositoryID, value.CommitSHA
		}
	case agentv1.ToolRequestBuild, agentv1.ToolGetBuild:
		var value buildv1.BuildView
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.BuildID, evidence.CommitSHA = value.BuildID, value.Identity
			if value.Artifact != nil {
				evidence.ArtifactDigest = value.Artifact.Digest
			}
		}
	case agentv1.ToolRequestApproval:
		var value agentv1.ApprovalRequestView
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.ApprovalRequestID, evidence.ApprovalPayloadHash = value.ApprovalRequestID, value.PayloadHash
		}
	case agentv1.ToolDeploy, agentv1.ToolRollback:
		var value runtimev1.DeploymentRef
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.DeploymentID, evidence.ReleaseID, evidence.GitOpsRevision = value.DeploymentID, value.ReleaseID, value.GitOpsRevision
		}
		if request.Tool == agentv1.ToolDeploy {
			var arguments DeployArguments
			if agentv1.DecodeStrict(request.Arguments, &arguments) == nil {
				evidence.BuildID = arguments.BuildID
			}
		}
	case agentv1.ToolGetDeployment:
		var value runtimev1.RuntimeStatus
		if json.Unmarshal(response.Result.Data, &value) == nil {
			evidence.DeploymentID, evidence.ReleaseID, evidence.GitOpsRevision = value.DeploymentID, value.ActiveRelease, value.GitOpsRevision
			if value.Phase == runtimev1.DeploymentReady {
				evidence.ReadyEndpoint = value.URL
			}
		}
	}
	return validatedInvocationEvidence(evidence)
}

func validatedInvocationEvidence(evidence agentv1.AuditEvidence) agentv1.AuditEvidence {
	if evidence.Validate() != nil {
		// Provider output is already represented by the public invocation
		// response, but malformed identifiers must never enter the durable join
		// index. Finalization still succeeds so a completed side effect is not
		// misreported as failed merely because optional evidence was unusable.
		return agentv1.AuditEvidence{}
	}
	return evidence
}
