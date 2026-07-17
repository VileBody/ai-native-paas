// Package productiongate provides fail-closed dependency adapters used while
// the internal mTLS service mesh and network-deferred providers are absent.
// Mutations are denied by Commerce before the agent service reserves budget.
package productiongate

import (
	"context"

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

func (Commerce) Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	return commercev1.EntitlementDecision{Allowed: false, Reason: "control-plane service gateways are not installed", PolicyVersion: "network-deferred-v1"}, nil
}

type Operations struct{}

func (Operations) Get(context.Context, string, string) (kernelv1.OperationSnapshot, error) {
	return kernelv1.OperationSnapshot{}, unavailable("kernel_gateway")
}
func (Operations) Cancel(context.Context, string, string) error { return unavailable("kernel_gateway") }

type Logs struct{}

func (Logs) GetLogs(context.Context, string, string, int) ([]string, error) {
	return nil, unavailable("workspace_nat")
}

type Usage struct{}

func (Usage) GetUsage(context.Context, string, string) (commercev1.InvoicePreview, error) {
	return commercev1.InvoicePreview{}, unavailable("commerce_gateway")
}
