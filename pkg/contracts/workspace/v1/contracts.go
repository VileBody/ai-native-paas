// Package v1 defines disposable workspace and command contracts.
package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

const APIVersion = "workspace.platform.example.com/v1"

var (
	digest            = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	credentialLeaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	environmentName   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	environmentRef    = regexp.MustCompile(`^(credential|secret|input|state|registry)://[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

type WorkspaceState string

const (
	WorkspaceProvisioning WorkspaceState = "PROVISIONING"
	WorkspaceReady        WorkspaceState = "READY"
	WorkspaceBusy         WorkspaceState = "BUSY"
	WorkspaceDestroying   WorkspaceState = "DESTROYING"
	WorkspaceDestroyed    WorkspaceState = "DESTROYED"
	WorkspaceFailed       WorkspaceState = "FAILED"
)

type WorkspaceSpec struct {
	ProjectID        string                   `json:"project_id"`
	TaskID           string                   `json:"task_id"`
	ImageDigest      string                   `json:"image_digest"`
	CPUMillis        int64                    `json:"cpu_millis"`
	MemoryMiB        int64                    `json:"memory_mib"`
	TTLSeconds       int64                    `json:"ttl_seconds"`
	NetworkProfile   string                   `json:"network_profile"`
	CredentialLeases []string                 `json:"credential_leases,omitempty"`
	SourceRevision   *sourcev2.SourceRevision `json:"source_revision,omitempty"`
}

func (s WorkspaceSpec) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.TaskID) == "" || !digest.MatchString(s.ImageDigest) || s.CPUMillis < 250 || s.CPUMillis > 64000 || s.MemoryMiB < 256 || s.MemoryMiB > 262144 || s.TTLSeconds < 60 || s.TTLSeconds > 86400 || s.NetworkProfile != "isolated-governed" {
		return errors.New("invalid workspace specification")
	}
	if len(s.CredentialLeases) > 64 {
		return errors.New("invalid workspace credential lease references")
	}
	for _, leaseID := range s.CredentialLeases {
		if !credentialLeaseID.MatchString(leaseID) {
			return errors.New("invalid workspace credential lease references")
		}
	}
	if s.SourceRevision != nil && s.SourceRevision.Validate() != nil {
		return errors.New("invalid workspace source revision")
	}
	return nil
}

type WorkspaceRef struct {
	WorkspaceID string         `json:"workspace_id"`
	ProjectID   string         `json:"project_id"`
	TaskID      string         `json:"task_id"`
	State       WorkspaceState `json:"state"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type CommandState string

const (
	CommandQueued    CommandState = "QUEUED"
	CommandRunning   CommandState = "RUNNING"
	CommandSucceeded CommandState = "SUCCEEDED"
	CommandFailed    CommandState = "FAILED"
	CommandCanceled  CommandState = "CANCELED"
	CommandTimedOut  CommandState = "TIMED_OUT"
)

type CommandSpec struct {
	Argv             []string          `json:"argv"`
	WorkingDir       string            `json:"working_dir"`
	EnvironmentRefs  map[string]string `json:"environment_refs,omitempty"`
	TimeoutSeconds   int64             `json:"timeout_seconds"`
	OutputLimitBytes int64             `json:"output_limit_bytes"`
}

func (s CommandSpec) Validate() error {
	if len(s.Argv) == 0 || len(s.Argv) > 128 || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 86400 || s.OutputLimitBytes < 1024 || s.OutputLimitBytes > 1<<30 || strings.HasPrefix(s.WorkingDir, "/") || strings.Contains(s.WorkingDir, "..") {
		return errors.New("invalid workspace command")
	}
	for _, arg := range s.Argv {
		if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
			return errors.New("invalid command argument")
		}
	}
	if len(s.EnvironmentRefs) > 128 {
		return errors.New("invalid workspace environment references")
	}
	for name, reference := range s.EnvironmentRefs {
		if !environmentName.MatchString(name) || !environmentRef.MatchString(reference) {
			return errors.New("invalid workspace environment references")
		}
	}
	return nil
}

type CommandView struct {
	CommandID   string       `json:"command_id"`
	WorkspaceID string       `json:"workspace_id"`
	State       CommandState `json:"state"`
	ExitCode    *int         `json:"exit_code,omitempty"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
}

type AgentMessageKind string

const (
	AgentMessageExec   AgentMessageKind = "EXEC"
	AgentMessageCancel AgentMessageKind = "CANCEL"
)

// AgentMessage is delivered over the workspace agent's outbound mTLS
// channel. It contains only credential references; secret values are resolved
// inside the workspace through the governed gateway.
type AgentMessage struct {
	MessageID           string           `json:"message_id"`
	Kind                AgentMessageKind `json:"kind"`
	CommandID           string           `json:"command_id"`
	WorkspaceID         string           `json:"workspace_id"`
	Spec                *CommandSpec     `json:"spec,omitempty"`
	CredentialLeases    []string         `json:"credential_leases,omitempty"`
	BudgetReservationID string           `json:"budget_reservation_id,omitempty"`
	BudgetDeadline      time.Time        `json:"budget_deadline,omitempty"`
	DeliveryAttempt     int64            `json:"delivery_attempt"`
}

type AgentSessionConnect struct {
	// VMID remains additive compatibility metadata for early v1 agents. The
	// control plane never trusts it for binding; CorrelationID is resolved
	// against the durable workspace/provider record.
	VMID          string `json:"vm_id,omitempty"`
	CorrelationID string `json:"correlation_id"`
}

type AgentSessionView struct {
	SessionID   string    `json:"session_id"`
	WorkspaceID string    `json:"workspace_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type AgentMessageAck struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
}

type AgentHeartbeat struct {
	SessionID string `json:"session_id"`
}

type AgentCertificateRotate struct {
	SessionID string `json:"session_id"`
	CSRPEM    string `json:"csr_pem"`
}

type AgentCertificateView struct {
	CertificatePEM string    `json:"certificate_pem"`
	CAChainPEM     string    `json:"ca_chain_pem"`
	NotAfter       time.Time `json:"not_after"`
}

type AgentCommandOutcome struct {
	SessionID             string       `json:"session_id"`
	ExecutionSessionID    string       `json:"execution_session_id,omitempty"`
	CommandID             string       `json:"command_id"`
	State                 CommandState `json:"state"`
	ExitCode              *int         `json:"exit_code,omitempty"`
	FinishedAt            time.Time    `json:"finished_at"`
	ProcessTreeTerminated bool         `json:"process_tree_terminated,omitempty"`
}

// AgentCredentialResolve identifies an already accepted command. Environment
// references are intentionally not accepted from the agent: the control plane
// resolves the exact references persisted with the RUNNING command.
type AgentCredentialResolve struct {
	SessionID          string `json:"session_id"`
	ExecutionSessionID string `json:"execution_session_id,omitempty"`
	CommandID          string `json:"command_id"`
}

// AgentCredentialView is a short-lived, command-scoped materialization. It is
// delivered only over the workspace's verified mTLS channel and must never be
// persisted by the agent.
type AgentCredentialView struct {
	Values    map[string]string `json:"values"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type AgentOutputStream string

const (
	AgentOutputStdout AgentOutputStream = "STDOUT"
	AgentOutputStderr AgentOutputStream = "STDERR"
)

// AgentOutputChunk carries already-redacted output. A stream always ends in
// one final chunk (which may contain zero bytes) and binds both the chunk and
// complete stream digests before encrypted object storage accepts it.
type AgentOutputChunk struct {
	SessionID          string            `json:"session_id"`
	ExecutionSessionID string            `json:"execution_session_id,omitempty"`
	CommandID          string            `json:"command_id"`
	Stream             AgentOutputStream `json:"stream"`
	Sequence           int64             `json:"sequence"`
	Data               []byte            `json:"data"`
	ChunkSHA256        string            `json:"chunk_sha256"`
	Final              bool              `json:"final"`
	Truncated          bool              `json:"truncated,omitempty"`
	TotalSHA256        string            `json:"total_sha256,omitempty"`
}

func (c AgentOutputChunk) Validate() error {
	if strings.TrimSpace(c.SessionID) == "" || strings.TrimSpace(c.CommandID) == "" || c.Stream != AgentOutputStdout && c.Stream != AgentOutputStderr || c.Sequence < 0 || c.Sequence > 1<<20 || len(c.Data) > 32<<10 || !digest.MatchString(c.ChunkSHA256) {
		return errors.New("invalid workspace output chunk")
	}
	chunkDigest := sha256.Sum256(c.Data)
	if c.ChunkSHA256 != "sha256:"+hex.EncodeToString(chunkDigest[:]) {
		return errors.New("invalid workspace output chunk digest")
	}
	if c.Final {
		if !digest.MatchString(c.TotalSHA256) {
			return errors.New("invalid workspace output stream digest")
		}
	} else if c.Truncated || c.TotalSHA256 != "" {
		return errors.New("non-final workspace output carries terminal metadata")
	}
	return nil
}
