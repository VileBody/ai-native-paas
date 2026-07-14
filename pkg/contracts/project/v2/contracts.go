// Package v2 defines the strict platform.yaml/v2 project contract.
package v2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "platform.example.com/v2"
	Kind       = "Project"
)

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)
	digest   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Contract struct {
	APIVersion     string             `json:"apiVersion" yaml:"apiVersion"`
	Kind           string             `json:"kind" yaml:"kind"`
	Metadata       Metadata           `json:"metadata" yaml:"metadata"`
	Environments   []Environment      `json:"environments" yaml:"environments"`
	Workspace      WorkspacePolicy    `json:"workspace" yaml:"workspace"`
	Infrastructure InfrastructureSpec `json:"infrastructure" yaml:"infrastructure"`
	Build          BuildPolicy        `json:"build" yaml:"build"`
	GitOps         GitOpsPolicy       `json:"gitops" yaml:"gitops"`
	Policy         GovernancePolicy   `json:"policy" yaml:"policy"`
}

type Metadata struct {
	Name string `json:"name" yaml:"name"`
}

type Environment struct {
	Name           string `json:"name" yaml:"name"`
	Production     bool   `json:"production" yaml:"production"`
	ApprovalPolicy string `json:"approvalPolicy" yaml:"approvalPolicy"`
}

type WorkspacePolicy struct {
	ImageDigest    string `json:"imageDigest" yaml:"imageDigest"`
	CPUMillis      int64  `json:"cpuMillis" yaml:"cpuMillis"`
	MemoryMiB      int64  `json:"memoryMiB" yaml:"memoryMiB"`
	TimeoutSeconds int64  `json:"timeoutSeconds" yaml:"timeoutSeconds"`
	NetworkProfile string `json:"networkProfile" yaml:"networkProfile"`
	EgressPolicy   string `json:"egressPolicy" yaml:"egressPolicy"`
	MaxArtifactMiB int64  `json:"maxArtifactMiB" yaml:"maxArtifactMiB"`
}

type InfrastructureSpec struct {
	Engine       string `json:"engine" yaml:"engine"`
	Root         string `json:"root" yaml:"root"`
	StateBackend string `json:"stateBackend" yaml:"stateBackend"`
}

type BuildPolicy struct {
	DefaultDriver     string `json:"defaultDriver" yaml:"defaultDriver"`
	SupplyChainPolicy string `json:"supplyChainPolicy" yaml:"supplyChainPolicy"`
}

type GitOpsPolicy struct {
	Engine string `json:"engine" yaml:"engine"`
	Root   string `json:"root" yaml:"root"`
}

type GovernancePolicy struct {
	ForbidClusterScopedResources          bool  `json:"forbidClusterScopedResources" yaml:"forbidClusterScopedResources"`
	ProductionRequiresApproval            bool  `json:"productionRequiresApproval" yaml:"productionRequiresApproval"`
	DestructiveApplyRequiresApproval      bool  `json:"destructiveApplyRequiresApproval" yaml:"destructiveApplyRequiresApproval"`
	MonthlyCostApprovalThresholdMinorUnit int64 `json:"monthlyCostApprovalThresholdMinorUnit" yaml:"monthlyCostApprovalThresholdMinorUnit"`
}

func Parse(raw []byte) (Contract, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return Contract{}, errors.New("platform contract size is invalid")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var contract Contract
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode platform contract: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Contract{}, errors.New("platform contract must contain one YAML document")
		}
		return Contract{}, fmt.Errorf("decode trailing platform contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (c Contract) Validate() error {
	if c.APIVersion != APIVersion || c.Kind != Kind || !dnsLabel.MatchString(c.Metadata.Name) {
		return errors.New("invalid platform contract identity")
	}
	if len(c.Environments) == 0 || len(c.Environments) > 32 {
		return errors.New("platform contract needs between one and 32 environments")
	}
	seen := make(map[string]struct{}, len(c.Environments))
	for _, environment := range c.Environments {
		if !dnsLabel.MatchString(environment.Name) || (environment.ApprovalPolicy != "automatic" && environment.ApprovalPolicy != "required") {
			return errors.New("invalid environment")
		}
		if _, ok := seen[environment.Name]; ok {
			return errors.New("duplicate environment")
		}
		seen[environment.Name] = struct{}{}
		if environment.Production && environment.ApprovalPolicy != "required" {
			return errors.New("production environment requires exact-plan approval")
		}
	}
	if !digest.MatchString(c.Workspace.ImageDigest) || c.Workspace.CPUMillis < 250 || c.Workspace.CPUMillis > 64000 || c.Workspace.MemoryMiB < 256 || c.Workspace.MemoryMiB > 262144 || c.Workspace.TimeoutSeconds < 60 || c.Workspace.TimeoutSeconds > 86400 || c.Workspace.MaxArtifactMiB < 1 || c.Workspace.MaxArtifactMiB > 10240 {
		return errors.New("invalid workspace policy")
	}
	if c.Workspace.NetworkProfile != "isolated" || (c.Workspace.EgressPolicy != "governed" && c.Workspace.EgressPolicy != "deny-all") {
		return errors.New("workspace network must be isolated and governed")
	}
	if c.Infrastructure.Engine != "opentofu" || c.Infrastructure.StateBackend != "platform" || !safeRelativePath(c.Infrastructure.Root) {
		return errors.New("invalid infrastructure policy")
	}
	if c.GitOps.Engine != "argocd" || !safeRelativePath(c.GitOps.Root) {
		return errors.New("invalid GitOps policy")
	}
	switch c.Build.DefaultDriver {
	case "dockerfile", "buildpacks", "nix", "custom-approved":
	default:
		return errors.New("invalid build driver")
	}
	if c.Build.SupplyChainPolicy == "" {
		return errors.New("supply-chain policy is required")
	}
	if !c.Policy.ForbidClusterScopedResources || !c.Policy.ProductionRequiresApproval || !c.Policy.DestructiveApplyRequiresApproval || c.Policy.MonthlyCostApprovalThresholdMinorUnit < 0 {
		return errors.New("beta governance minimums are not satisfied")
	}
	return nil
}

func safeRelativePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || strings.Contains(value, "\x00") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

type CreateProjectResponse struct {
	ProjectID            string `json:"project_id"`
	GitURL               string `json:"git_url"`
	MCPURL               string `json:"mcp_url"`
	AgentID              string `json:"agent_id"`
	AgentEnrollmentToken string `json:"agent_enrollment_token"`
	EnrollmentExpiresIn  int64  `json:"enrollment_expires_in_seconds"`
}

type CreateProjectRequest struct {
	Name string `json:"name"`
}

func (r CreateProjectRequest) Validate() error {
	if !dnsLabel.MatchString(r.Name) {
		return errors.New("project name must be a DNS label")
	}
	return nil
}
