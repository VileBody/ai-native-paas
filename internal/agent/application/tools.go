package application

import (
	"context"
	"encoding/json"
	"fmt"
	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
	"strings"
	"time"
)

func (s *Service) execute(ctx context.Context, req agentv1.InvocationRequest, invID string) (agentv1.InvocationResponse, error) {
	result := func(t, id, state, url string, v any) agentv1.InvocationResponse {
		data, _ := json.Marshal(v)
		return agentv1.InvocationResponse{APIVersion: agentv1.APIVersion, InvocationID: invID, Result: &agentv1.ResultReference{Type: t, ID: id, State: state, URL: url, Data: data}}
	}
	operation := func(op *kernelv1.OperationRef) agentv1.InvocationResponse {
		return agentv1.InvocationResponse{APIVersion: agentv1.APIVersion, InvocationID: invID, Operation: op}
	}
	switch req.Tool {
	case agentv1.ToolCreateProject:
		if s.Source == nil {
			return s.failureResponse(invID, domain.NewError(domain.CodeUnavailable, "source service unavailable")), domain.NewError(domain.CodeUnavailable, "source service unavailable")
		}
		var a CreateProjectArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Source.CreateProject(ctx, req.TenantID, req.AgentID, a.Name, req.IdempotencyKey)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("project", v.ProjectID, v.State, v.WebURL, v), nil
	case agentv1.ToolGetProject:
		var a GetProjectArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Source.GetProject(ctx, req.TenantID, a.ProjectID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("project", v.ProjectID, v.State, v.WebURL, v), nil
	case agentv1.ToolApplyRepositoryPatch:
		var a ApplyPatchArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Source.ApplyPatch(ctx, req.TenantID, req.AgentID, a, req.IdempotencyKey)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if err = s.Source.Reconcile(ctx, req.TenantID, a.ProjectID); err != nil {
			return s.providerFailure(invID, err)
		}
		return result("commit", v.CommitSHA, "COMMITTED", "", v), nil
	case agentv1.ToolCreateBranch:
		var a CreateBranchArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Source.CreateBranch(ctx, req.TenantID, req.AgentID, a, req.IdempotencyKey)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("commit", v.CommitSHA, "CREATED", "", v), nil
	case agentv1.ToolCreateMergeRequest:
		var a CreateMergeRequestArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Source.CreateMergeRequest(ctx, req.TenantID, req.AgentID, a, req.IdempotencyKey)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("merge_request", v.MergeRequestID, v.State, v.URL, v), nil
	case agentv1.ToolRequestBuild:
		var a RequestBuildArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		revision := sourcev1.SourceRevision{ProjectID: a.Revision.ProjectID, RepositoryID: a.Revision.RepositoryID, Branch: a.Revision.Branch, CommitSHA: a.Revision.CommitSHA, SourceRoot: a.Revision.SourceRoot}
		v, err := s.Builds.Request(ctx, req.TenantID, revision, a.EstimatedMinutes, req.IdempotencyKey, req.CorrelationID)
		if err != nil {
			if p, ok := err.(*ProviderError); ok && p.Retryable && p.OperationID != "" {
				v, err = s.Builds.Resume(ctx, req.TenantID, p.OperationID)
			}
		}
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if v.Operation != nil {
			return operation(v.Operation), nil
		}
		return result("build", v.Build.BuildID, string(v.Build.State), "", v.Build), nil
	case agentv1.ToolGetBuild:
		var a GetBuildArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Builds.Get(ctx, req.TenantID, a.BuildID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if v.Operation != nil {
			return operation(v.Operation), nil
		}
		return result("build", v.Build.BuildID, string(v.Build.State), "", v.Build), nil
	case agentv1.ToolDeploy:
		var a DeployArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		b, err := s.Builds.Get(ctx, req.TenantID, a.BuildID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if b.Build.Artifact == nil {
			return s.providerFailure(invID, domain.NewError(domain.CodeConflict, "build has no releasable artifact"))
		}
		dr := runtimev1.DeployRequest{TenantID: req.TenantID, ApplicationID: a.ApplicationID, EnvironmentID: a.EnvironmentID, Artifact: *b.Build.Artifact, Configuration: a.Configuration, IdempotencyKey: req.IdempotencyKey, ActorID: req.AgentID}
		v, err := s.Runtime.Deploy(ctx, dr, a.ExpectedEnvironmentRevision)
		if err != nil {
			if p, ok := err.(*ProviderError); ok && p.Retryable {
				_ = s.Runtime.Reconcile(ctx, req.TenantID, a.EnvironmentID)
				v, err = s.Runtime.Deploy(ctx, dr, a.ExpectedEnvironmentRevision)
			}
		}
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if v.Operation != nil {
			return operation(v.Operation), nil
		}
		return result("deployment", v.Deployment.DeploymentID, string(v.Deployment.Phase), "", v.Deployment), nil
	case agentv1.ToolGetDeployment:
		var a GetDeploymentArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Runtime.Get(ctx, req.TenantID, a.DeploymentID)
		if err != nil {
			_ = s.Runtime.Reconcile(ctx, req.TenantID, a.DeploymentID)
			v, err = s.Runtime.Get(ctx, req.TenantID, a.DeploymentID)
		}
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if v.Operation != nil {
			return operation(v.Operation), nil
		}
		if v.Status != nil {
			return result("deployment", v.Status.DeploymentID, string(v.Status.Phase), v.Status.URL, *v.Status), nil
		}
		return result("deployment", v.Deployment.DeploymentID, string(v.Deployment.Phase), "", v.Deployment), nil
	case agentv1.ToolRollback:
		var a RollbackArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Runtime.Rollback(ctx, req.TenantID, a.DeploymentID, a.TargetReleaseID, a.ExpectedEnvironmentRevision, req.IdempotencyKey)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		if v.Operation != nil {
			return operation(v.Operation), nil
		}
		return result("deployment", v.Deployment.DeploymentID, string(v.Deployment.Phase), "", v.Deployment), nil
	case agentv1.ToolSetSecret:
		var a SetSecretArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Attachments.SetSecret(ctx, attachmentsv1.SetSecretRequest{TenantID: req.TenantID, ApplicationID: a.ApplicationID, EnvironmentID: a.EnvironmentID, Name: a.Name, Value: a.Value, IdempotencyKey: req.IdempotencyKey, ActorID: req.AgentID})
		if err != nil {
			return s.providerFailure(invID, err)
		}
		v.Name = a.Name
		return result("secret_metadata", a.Name, "WRITTEN", "", v), nil
	case agentv1.ToolListSecretMetadata:
		var a ListSecretMetadataArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Attachments.ListSecretMetadata(ctx, req.TenantID, a.ApplicationID, a.EnvironmentID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("secret_metadata_list", a.ApplicationID, "READY", "", v), nil
	case agentv1.ToolProvisionService:
		var a ProvisionServiceArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Attachments.Provision(ctx, attachmentsv1.ServiceRequest{TenantID: req.TenantID, ServiceType: a.ServiceType, Plan: a.Plan, Name: a.Name, IdempotencyKey: req.IdempotencyKey, ActorID: req.AgentID})
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("service_instance", v.ServiceInstanceID, v.State, "", v), nil
	case agentv1.ToolBindService:
		var a BindServiceArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Attachments.Bind(ctx, attachmentsv1.BindRequest{TenantID: req.TenantID, ServiceInstanceID: a.ServiceInstanceID, ApplicationID: a.ApplicationID, EnvironmentID: a.EnvironmentID, IdempotencyKey: req.IdempotencyKey, ActorID: req.AgentID})
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("service_binding", v.BindingID, v.State, "", v), nil
	case agentv1.ToolAddDomain:
		var a AddDomainArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Attachments.AddDomain(ctx, attachmentsv1.DomainRequest{TenantID: req.TenantID, ApplicationID: a.ApplicationID, EnvironmentID: a.EnvironmentID, Hostname: a.Hostname, IdempotencyKey: req.IdempotencyKey, ActorID: req.AgentID})
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("domain_claim", v.DomainClaimID, v.State, "", v), nil
	case agentv1.ToolGetLogs:
		var a GetLogsArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Logs.GetLogs(ctx, req.TenantID, a.ApplicationID, a.Limit)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("logs", a.ApplicationID, "READY", "", redactLines(v)), nil
	case agentv1.ToolGetUsage:
		var a GetUsageArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Usage.GetUsage(ctx, req.TenantID, a.PeriodID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("usage", a.PeriodID, "READY", "", v), nil
	case agentv1.ToolRequestApproval:
		var a RequestApprovalArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		hash, _ := agentv1.StableFingerprint(a.Payload)
		now := s.now()
		ar := domain.ApprovalRequest{ID: s.newID("approval"), TenantID: req.TenantID, AgentID: req.AgentID, TaskID: req.TaskID, Action: a.Action, Resource: a.Resource, PayloadHash: hash, State: domain.ApprovalPending, ExpiresAt: now.Add(timeDuration(a.TTLSeconds)), Version: 1, CreatedAt: now, UpdatedAt: now}
		err := s.Store.Transact(ctx, func(tx Tx) error { return tx.InsertApprovalRequest(ar) })
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("approval_request", ar.ID, string(ar.State), "", ar.View()), nil
	case agentv1.ToolGetOperation:
		var a GetOperationArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		v, err := s.Operations.Get(ctx, req.TenantID, a.OperationID)
		if err != nil {
			return s.providerFailure(invID, err)
		}
		return result("operation", string(v.OperationID), string(v.State), "", v), nil
	case agentv1.ToolCancelOperation:
		var a CancelOperationArguments
		_ = agentv1.DecodeStrict(req.Arguments, &a)
		if err := s.Operations.Cancel(ctx, req.TenantID, a.OperationID); err != nil {
			return s.providerFailure(invID, err)
		}
		return result("operation", a.OperationID, "CANCELED", "", map[string]string{"operation_id": a.OperationID}), nil
	default:
		return s.providerFailure(invID, domain.NewError(domain.CodeInvalidArgument, "unsupported tool"))
	}
}
func (s *Service) providerFailure(id string, err error) (agentv1.InvocationResponse, error) {
	if p, ok := err.(*ProviderError); ok {
		e := domain.Retryable(domain.CodeUnavailable, "provider operation is temporarily unavailable", p.OperationID, p)
		return s.failureResponse(id, e), e
	}
	if _, ok := err.(*domain.Error); ok {
		return s.failureResponse(id, err), err
	}
	e := domain.Wrap(domain.CodeUnavailable, "provider operation failed", err)
	return s.failureResponse(id, e), e
}
func redactLines(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		for _, word := range []string{"token=", "password=", "secret=", "authorization:"} {
			if j := indexFold(v, word); j >= 0 {
				v = v[:j] + word + "[REDACTED]"
			}
		}
		out[i] = v
	}
	return out
}
func indexFold(s, sub string) int        { return strings.Index(strings.ToLower(s), strings.ToLower(sub)) }
func timeDuration(v int64) time.Duration { return time.Duration(v) * time.Second }

var _ = fmt.Sprintf
