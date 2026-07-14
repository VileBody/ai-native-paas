// Package v1 defines disposable workspace and command contracts.
package v1

import (
	"errors"
	"regexp"
	"strings"
	"time"
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
	ProjectID        string   `json:"project_id"`
	TaskID           string   `json:"task_id"`
	ImageDigest      string   `json:"image_digest"`
	CPUMillis        int64    `json:"cpu_millis"`
	MemoryMiB        int64    `json:"memory_mib"`
	TTLSeconds       int64    `json:"ttl_seconds"`
	NetworkProfile   string   `json:"network_profile"`
	CredentialLeases []string `json:"credential_leases,omitempty"`
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
