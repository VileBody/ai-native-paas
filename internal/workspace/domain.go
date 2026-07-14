// Package workspace implements the control-plane lifecycle for disposable task VMs.
package workspace

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

var (
	ErrNotFound            = errors.New("workspace resource not found")
	ErrConflict            = errors.New("workspace resource conflicts with existing state")
	ErrPolicyDenied        = errors.New("workspace command denied by policy")
	ErrReconcileClaimed    = errors.New("workspace reconciliation is already claimed")
	ErrStatefulCommandBusy = errors.New("stateful workspace command is already running")
)

type Scope struct {
	TenantID  string
	ProjectID string
	ActorID   string
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.TenantID) == "" || strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.ActorID) == "" {
		return errors.New("verified workspace scope is incomplete")
	}
	return nil
}

type Workspace struct {
	ID                  string
	TenantID            string
	ProjectID           string
	TaskID              string
	Spec                workspacev1.WorkspaceSpec
	State               workspacev1.WorkspaceState
	IdempotencyKey      string
	RequestHash         string
	CorrelationID       string
	ProviderVMID        string
	ProviderDiskIDs     []string
	ProviderFingerprint string
	LastError           string
	ExpiresAt           time.Time
	CreatedBy           string
	UpdatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Version             int64
	ReconcileOwner      string
	ReconcileLeaseUntil time.Time
}

func (w Workspace) Ref() workspacev1.WorkspaceRef {
	return workspacev1.WorkspaceRef{WorkspaceID: w.ID, ProjectID: w.ProjectID, TaskID: w.TaskID, State: w.State, ExpiresAt: w.ExpiresAt}
}

func (w Workspace) terminal() bool {
	return w.State == workspacev1.WorkspaceDestroyed || w.State == workspacev1.WorkspaceFailed
}

type Command struct {
	ID               string
	TenantID         string
	ProjectID        string
	TaskID           string
	WorkspaceID      string
	Spec             workspacev1.CommandSpec
	Kind             string
	SerializationKey string
	CredentialLeases []string
	ActorID          string
	IdempotencyKey   string
	RequestHash      string
	State            workspacev1.CommandState
	AgentSessionID   string
	ExecutionVMID    string
	ExitCode         *int
	StartedAt        *time.Time
	FinishedAt       *time.Time
	UsageStartedAt   *time.Time
	UsageFinishedAt  *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Version          int64
}

func (c Command) View() workspacev1.CommandView {
	return workspacev1.CommandView{CommandID: c.ID, WorkspaceID: c.WorkspaceID, State: c.State, ExitCode: c.ExitCode, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt}
}

func (c Command) terminal() bool {
	switch c.State {
	case workspacev1.CommandSucceeded, workspacev1.CommandFailed, workspacev1.CommandCanceled, workspacev1.CommandTimedOut:
		return true
	default:
		return false
	}
}

type CommandPolicy struct {
	AllowedExecutables map[string]struct{}
}

func DefaultCommandPolicy() CommandPolicy {
	allowed := []string{"git", "tofu", "helm", "kustomize", "buildctl", "cosign", "go", "npm", "pnpm", "node", "python", "python3", "make", "sed", "grep", "rg", "find", "mkdir", "cp", "mv", "rm", "tar"}
	result := CommandPolicy{AllowedExecutables: make(map[string]struct{}, len(allowed))}
	for _, executable := range allowed {
		result.AllowedExecutables[executable] = struct{}{}
	}
	return result
}

func (p CommandPolicy) Validate(spec workspacev1.CommandSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	executable := spec.Argv[0]
	if filepath.Base(executable) != executable || strings.ContainsAny(executable, `/\\`) {
		return ErrPolicyDenied
	}
	if _, ok := p.AllowedExecutables[executable]; !ok {
		return ErrPolicyDenied
	}
	for _, argument := range spec.Argv[1:] {
		normalized := strings.ToLower(strings.TrimSpace(argument))
		if strings.HasPrefix(normalized, "ssh://root@") || strings.HasPrefix(normalized, "root@") {
			return ErrPolicyDenied
		}
		for _, forbidden := range []string{
			"--privileged", "--network=host", "--pid=host", "--ipc=host", "--userns=host",
			"/var/run/docker.sock", "/run/containerd/containerd.sock", "--device=", "--cap-add=",
		} {
			if normalized == forbidden || strings.HasPrefix(normalized, forbidden) || strings.Contains(normalized, forbidden) {
				return ErrPolicyDenied
			}
		}
	}
	return nil
}
