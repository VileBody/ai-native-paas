package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

const reconcileLease = 30 * time.Second

type Service struct {
	Store              Store
	Provider           Provider
	Sessions           AgentSessions
	Leases             LeaseRevoker
	Clock              Clock
	IDs                IDGenerator
	Policy             CommandPolicy
	ReconcilerID       string
	WorkspaceVPCID     string
	AllowedEgressHosts []string
	EgressGatewayCIDRs []string
	DNSResolverCIDRs   []string
	DeniedCIDRs        []string
}

type CreateRequest struct {
	Scope          Scope
	Spec           workspacev1.WorkspaceSpec
	IdempotencyKey string
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (workspacev1.WorkspaceRef, error) {
	if err := s.require(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	if err := request.Scope.validate(); err != nil || request.Spec.Validate() != nil || request.Spec.ProjectID != request.Scope.ProjectID || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 {
		return workspacev1.WorkspaceRef{}, errors.New("workspace creation request is invalid")
	}
	hash, err := agentv2.StableFingerprint(request.Spec)
	if err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	now := s.Clock.Now().UTC()
	workspace := Workspace{
		ID: s.IDs.New("workspace"), TenantID: request.Scope.TenantID, ProjectID: request.Scope.ProjectID,
		TaskID: request.Spec.TaskID, Spec: request.Spec, State: workspacev1.WorkspaceProvisioning,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CorrelationID: s.IDs.New("workspace-correlation"),
		ExpiresAt: now.Add(time.Duration(request.Spec.TTLSeconds) * time.Second), CreatedBy: request.Scope.ActorID, UpdatedBy: request.Scope.ActorID,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	stored, _, err := s.Store.CreateWorkspace(ctx, workspace)
	if err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	return stored.Ref(), nil
}

func (s *Service) Get(ctx context.Context, scope Scope, workspaceID string) (workspacev1.WorkspaceRef, error) {
	if err := s.require(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	if err := scope.validate(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	workspace, err := s.Store.GetWorkspace(ctx, scope.TenantID, scope.ProjectID, workspaceID)
	if err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	return workspace.Ref(), nil
}

// Reconcile performs a single idempotent provider step. It is intended for a
// durable worker consuming the workspace outbox, not for the request handler.
func (s *Service) Reconcile(ctx context.Context, workspaceID string) (workspacev1.WorkspaceRef, error) {
	if err := s.require(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	now := s.Clock.Now().UTC()
	workspace, err := s.Store.ClaimWorkspace(ctx, workspaceID, s.ReconcilerID, now, now.Add(reconcileLease))
	if err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	switch workspace.State {
	case workspacev1.WorkspaceProvisioning:
		return s.reconcileProvision(ctx, workspace)
	case workspacev1.WorkspaceDestroying:
		return s.reconcileDestroy(ctx, workspace)
	default:
		return workspace.Ref(), nil
	}
}

func (s *Service) reconcileProvision(ctx context.Context, workspace Workspace) (workspacev1.WorkspaceRef, error) {
	vm, err := s.Provider.FindByCorrelation(ctx, workspace.CorrelationID)
	if errors.Is(err, ErrNotFound) {
		vm, err = s.Provider.Create(ctx, s.providerRequest(workspace))
	}
	if err != nil {
		return workspace.Ref(), err
	}
	if err := validateProviderVM(workspace, vm); err != nil {
		return workspace.Ref(), err
	}
	connected, err := s.Sessions.Connected(ctx, workspace.ID, vm.VMID)
	if err != nil {
		return workspace.Ref(), err
	}
	if !connected || !vm.OutboundAgentReady {
		return workspace.Ref(), nil
	}
	expected := workspace.Version
	workspace.ProviderVMID = vm.VMID
	workspace.ProviderDiskIDs = append([]string(nil), vm.DiskIDs...)
	workspace.ProviderFirewallGroupIDs = append([]string(nil), vm.FirewallGroupIDs...)
	workspace.ProviderFingerprint = providerFingerprint(vm)
	workspace.State = workspacev1.WorkspaceReady
	workspace.LastError = ""
	workspace.ReconcileOwner = ""
	workspace.ReconcileLeaseUntil = time.Time{}
	workspace.UpdatedBy = s.ReconcilerID
	workspace.UpdatedAt = s.Clock.Now().UTC()
	workspace.Version++
	if err := s.Store.UpdateWorkspace(ctx, workspace, expected); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	return workspace.Ref(), nil
}

func (s *Service) Destroy(ctx context.Context, scope Scope, workspaceID string) (workspacev1.WorkspaceRef, error) {
	if err := s.require(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	if err := scope.validate(); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	workspace, err := s.Store.GetWorkspace(ctx, scope.TenantID, scope.ProjectID, workspaceID)
	if err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	if workspace.State == workspacev1.WorkspaceDestroyed || workspace.State == workspacev1.WorkspaceDestroying {
		return workspace.Ref(), nil
	}
	expected := workspace.Version
	workspace.State = workspacev1.WorkspaceDestroying
	workspace.UpdatedBy = scope.ActorID
	workspace.UpdatedAt = s.Clock.Now().UTC()
	workspace.Version++
	if err := s.Store.UpdateWorkspace(ctx, workspace, expected); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	return workspace.Ref(), nil
}

func (s *Service) reconcileDestroy(ctx context.Context, workspace Workspace) (workspacev1.WorkspaceRef, error) {
	if err := s.Sessions.Close(ctx, workspace.ID); err != nil {
		return workspace.Ref(), err
	}
	for _, leaseID := range workspace.Spec.CredentialLeases {
		if err := s.Leases.Revoke(ctx, leaseID, "workspace_destroy"); err != nil {
			return workspace.Ref(), err
		}
	}
	target := ProviderVM{VMID: workspace.ProviderVMID, DiskIDs: workspace.ProviderDiskIDs, FirewallGroupIDs: workspace.ProviderFirewallGroupIDs, CorrelationID: workspace.CorrelationID, ImageDigest: workspace.Spec.ImageDigest, NetworkProfile: workspace.Spec.NetworkProfile}
	if target.VMID == "" || len(target.DiskIDs) == 0 {
		discovered, findErr := s.Provider.FindByCorrelation(ctx, workspace.CorrelationID)
		if findErr == nil {
			target = discovered
		} else if !errors.Is(findErr, ErrNotFound) {
			return workspace.Ref(), findErr
		}
	}
	evidence, err := s.Provider.Destroy(ctx, target)
	if err != nil {
		return workspace.Ref(), err
	}
	if !evidence.VMAbsent || !sameSet(evidence.AbsentDiskIDs, target.DiskIDs) || !sameSet(evidence.AbsentFirewallGroupIDs, target.FirewallGroupIDs) {
		return workspace.Ref(), errors.New("provider did not prove workspace VM, disks, and firewall absent")
	}
	expected := workspace.Version
	workspace.State = workspacev1.WorkspaceDestroyed
	workspace.ReconcileOwner = ""
	workspace.ReconcileLeaseUntil = time.Time{}
	workspace.UpdatedBy = s.ReconcilerID
	workspace.UpdatedAt = s.Clock.Now().UTC()
	workspace.Version++
	if err := s.Store.UpdateWorkspace(ctx, workspace, expected); err != nil {
		return workspacev1.WorkspaceRef{}, err
	}
	return workspace.Ref(), nil
}

type ExecRequest struct {
	Scope            Scope
	WorkspaceID      string
	Spec             workspacev1.CommandSpec
	Kind             string
	SerializationKey string
	CredentialLeases []string
	IdempotencyKey   string
}

func (s *Service) Exec(ctx context.Context, request ExecRequest) (workspacev1.CommandView, error) {
	if err := s.require(); err != nil {
		return workspacev1.CommandView{}, err
	}
	if err := request.Scope.validate(); err != nil || strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.Kind) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return workspacev1.CommandView{}, errors.New("workspace command request is invalid")
	}
	if err := s.Policy.Validate(request.Spec); err != nil {
		return workspacev1.CommandView{}, err
	}
	workspace, err := s.Store.GetWorkspace(ctx, request.Scope.TenantID, request.Scope.ProjectID, request.WorkspaceID)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	now := s.Clock.Now().UTC()
	if workspace.State != workspacev1.WorkspaceReady && workspace.State != workspacev1.WorkspaceBusy || !workspace.ExpiresAt.After(now) {
		return workspacev1.CommandView{}, ErrConflict
	}
	fingerprintValue := struct {
		WorkspaceID      string
		Spec             workspacev1.CommandSpec
		Kind             string
		SerializationKey string
	}{request.WorkspaceID, request.Spec, request.Kind, request.SerializationKey}
	hash, err := agentv2.StableFingerprint(fingerprintValue)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	command := Command{
		ID: s.IDs.New("command"), TenantID: request.Scope.TenantID, ProjectID: request.Scope.ProjectID,
		TaskID: workspace.TaskID, WorkspaceID: workspace.ID, Spec: request.Spec, Kind: request.Kind,
		SerializationKey: request.SerializationKey, CredentialLeases: append([]string(nil), request.CredentialLeases...),
		ActorID: request.Scope.ActorID, IdempotencyKey: request.IdempotencyKey, RequestHash: hash, State: workspacev1.CommandQueued,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	command, _, err = s.Store.CreateCommand(ctx, command)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	return command.View(), nil
}

// Dispatch performs the asynchronous outbox step for a queued command. A
// duplicate dispatch uses the same durable command ID, which the workspace
// agent must also treat idempotently.
func (s *Service) Dispatch(ctx context.Context, scope Scope, commandID string) (workspacev1.CommandView, error) {
	if err := s.require(); err != nil {
		return workspacev1.CommandView{}, err
	}
	if err := scope.validate(); err != nil || strings.TrimSpace(commandID) == "" {
		return workspacev1.CommandView{}, errors.New("workspace command dispatch is invalid")
	}
	command, err := s.Store.GetCommand(ctx, scope.TenantID, scope.ProjectID, commandID)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	if command.terminal() || command.State == workspacev1.CommandRunning {
		return command.View(), nil
	}
	workspace, err := s.Store.GetWorkspace(ctx, scope.TenantID, scope.ProjectID, command.WorkspaceID)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	now := s.Clock.Now().UTC()
	if workspace.State != workspacev1.WorkspaceReady && workspace.State != workspacev1.WorkspaceBusy || !workspace.ExpiresAt.After(now) {
		return workspacev1.CommandView{}, ErrConflict
	}
	if command.SerializationKey != "" {
		acquired, acquireErr := s.Store.AcquireSerialization(ctx, command.ProjectID, command.SerializationKey, command.ID)
		if acquireErr != nil {
			return workspacev1.CommandView{}, acquireErr
		}
		if !acquired {
			return command.View(), ErrStatefulCommandBusy
		}
	}
	receipt, err := s.Sessions.Dispatch(ctx, CommandEnvelope{CommandID: command.ID, WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, TaskID: command.TaskID, Spec: command.Spec, CredentialLeases: command.CredentialLeases})
	if err != nil {
		return command.View(), err
	}
	if !receipt.Accepted || receipt.CommandID != command.ID || receipt.WorkspaceID != workspace.ID || receipt.VMID != workspace.ProviderVMID || strings.TrimSpace(receipt.AgentSessionID) == "" {
		return command.View(), errors.New("workspace agent returned invalid dispatch evidence")
	}
	expected := command.Version
	command.State = workspacev1.CommandRunning
	command.AgentSessionID = receipt.AgentSessionID
	command.ExecutionVMID = receipt.VMID
	command.StartedAt = timePointer(now)
	command.UsageStartedAt = timePointer(now)
	command.UpdatedAt = now
	command.Version++
	if err := s.Store.UpdateCommand(ctx, command, expected); err != nil {
		return workspacev1.CommandView{}, err
	}
	return command.View(), nil
}

type CommandOutcome struct {
	TenantID              string
	ProjectID             string
	WorkspaceID           string
	CommandID             string
	AgentSessionID        string
	VMID                  string
	State                 workspacev1.CommandState
	ExitCode              *int
	FinishedAt            time.Time
	ProcessTreeTerminated bool
}

func (s *Service) RecordOutcome(ctx context.Context, outcome CommandOutcome) (workspacev1.CommandView, error) {
	if err := s.require(); err != nil {
		return workspacev1.CommandView{}, err
	}
	command, err := s.Store.GetCommand(ctx, outcome.TenantID, outcome.ProjectID, outcome.CommandID)
	if err != nil {
		return workspacev1.CommandView{}, err
	}
	if command.State != workspacev1.CommandRunning || command.StartedAt == nil || command.AgentSessionID == "" || command.ExecutionVMID == "" || command.WorkspaceID != outcome.WorkspaceID || command.AgentSessionID != outcome.AgentSessionID || command.ExecutionVMID != outcome.VMID || !terminalCommandState(outcome.State) {
		if command.terminal() && command.WorkspaceID == outcome.WorkspaceID && command.AgentSessionID == outcome.AgentSessionID && command.ExecutionVMID == outcome.VMID && command.State == outcome.State && equalExitCode(command.ExitCode, outcome.ExitCode) {
			return command.View(), nil
		}
		return workspacev1.CommandView{}, ErrConflict
	}
	if outcome.State == workspacev1.CommandTimedOut && !outcome.ProcessTreeTerminated {
		return workspacev1.CommandView{}, errors.New("workspace agent did not prove process-tree termination")
	}
	if outcome.FinishedAt.Before(*command.StartedAt) || outcome.FinishedAt.After(s.Clock.Now().UTC().Add(time.Minute)) {
		return workspacev1.CommandView{}, errors.New("command outcome time is invalid")
	}
	expected := command.Version
	command.State = outcome.State
	command.ExitCode = copyInt(outcome.ExitCode)
	command.FinishedAt = timePointer(outcome.FinishedAt.UTC())
	command.UsageFinishedAt = timePointer(outcome.FinishedAt.UTC())
	command.UpdatedAt = s.Clock.Now().UTC()
	command.Version++
	if err := s.Store.UpdateCommand(ctx, command, expected); err != nil {
		return workspacev1.CommandView{}, err
	}
	if command.SerializationKey != "" {
		if err := s.Store.ReleaseSerialization(ctx, command.ProjectID, command.SerializationKey, command.ID); err != nil {
			return workspacev1.CommandView{}, err
		}
	}
	for _, leaseID := range command.CredentialLeases {
		if err := s.Leases.Revoke(ctx, leaseID, "command_terminal"); err != nil {
			return workspacev1.CommandView{}, err
		}
	}
	return command.View(), nil
}

func (s *Service) Expire(ctx context.Context, limit int) (int, error) {
	if err := s.require(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("expiry batch limit is invalid")
	}
	items, err := s.Store.ListExpired(ctx, s.Clock.Now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		_, err := s.Destroy(ctx, Scope{TenantID: item.TenantID, ProjectID: item.ProjectID, ActorID: "workspace-ttl-controller"}, item.ID)
		if err != nil && !errors.Is(err, ErrConflict) {
			return 0, err
		}
	}
	return len(items), nil
}

// EnforceTimeouts persists cancellation intent through the outbound agent
// session. TIMED_OUT and usage are recorded later from mTLS-bound termination
// evidence, never from a control-plane assumption.
func (s *Service) EnforceTimeouts(ctx context.Context, limit int) (int, error) {
	if err := s.require(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("timeout batch limit is invalid")
	}
	commands, err := s.Store.ListTimedOut(ctx, s.Clock.Now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	for _, command := range commands {
		if cancelErr := s.Sessions.RequestCancel(ctx, command.WorkspaceID, command.ID); cancelErr != nil {
			return 0, cancelErr
		}
		expected := command.Version
		now := s.Clock.Now().UTC()
		command.CancelRequestedAt = timePointer(now)
		command.UpdatedAt = now
		command.Version++
		if err := s.Store.UpdateCommand(ctx, command, expected); err != nil {
			return 0, err
		}
	}
	return len(commands), nil
}

func (s *Service) providerRequest(workspace Workspace) ProviderCreateRequest {
	denied := append([]string{"169.254.169.254/32"}, s.DeniedCIDRs...)
	return ProviderCreateRequest{
		WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, TaskID: workspace.TaskID,
		CorrelationID: workspace.CorrelationID, ImageDigest: workspace.Spec.ImageDigest,
		CPUMillis: workspace.Spec.CPUMillis, MemoryMiB: workspace.Spec.MemoryMiB,
		ExpiresAt: workspace.ExpiresAt, NetworkProfile: workspace.Spec.NetworkProfile,
		NetworkIsolation: NetworkIsolation{
			VPCID: s.WorkspaceVPCID, PrivateAddressOnly: true, DenyAllInbound: true, OutboundGatewayMTLS: true,
			AllowedEgressHosts: append([]string(nil), s.AllowedEgressHosts...), EgressGatewayCIDRs: append([]string(nil), s.EgressGatewayCIDRs...),
			DNSResolverCIDRs: append([]string(nil), s.DNSResolverCIDRs...), DeniedCIDRs: denied,
			DeniedDestinationPorts: []int{25, 465, 587},
		},
	}
}

func (s *Service) require() error {
	if s == nil || s.Store == nil || s.Provider == nil || s.Sessions == nil || s.Leases == nil || s.Clock == nil || s.IDs == nil || strings.TrimSpace(s.ReconcilerID) == "" || strings.TrimSpace(s.WorkspaceVPCID) == "" || len(s.Policy.AllowedExecutables) == 0 || len(s.EgressGatewayCIDRs) == 0 || len(s.DNSResolverCIDRs) == 0 {
		return errors.New("workspace service dependencies are unavailable")
	}
	return nil
}

func validateProviderVM(workspace Workspace, vm ProviderVM) error {
	if vm.VMID == "" || len(vm.DiskIDs) == 0 || len(vm.FirewallGroupIDs) == 0 || vm.CorrelationID != workspace.CorrelationID || vm.ImageDigest != workspace.Spec.ImageDigest || vm.NetworkProfile != workspace.Spec.NetworkProfile || !vm.PrivateAddressOnly || !vm.DenyAllInbound {
		return errors.New("workspace provider returned an invalid or unisolated VM")
	}
	return nil
}

func providerFingerprint(vm ProviderVM) string {
	hash, _ := agentv2.StableFingerprint(vm)
	return hash
}

func terminalCommandState(state workspacev1.CommandState) bool {
	switch state {
	case workspacev1.CommandSucceeded, workspacev1.CommandFailed, workspacev1.CommandCanceled, workspacev1.CommandTimedOut:
		return true
	default:
		return false
	}
}

func sameSet(left, right []string) bool {
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func timePointer(value time.Time) *time.Time { return &value }
func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func equalExitCode(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
