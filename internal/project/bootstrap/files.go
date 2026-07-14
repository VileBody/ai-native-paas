// Package bootstrap renders the canonical files committed into a new project repository.
package bootstrap

import (
	"fmt"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Name                 string
	WorkspaceImageDigest string
}

func Files(options Options) ([]application.BootstrapFile, error) {
	contract := projectv2.Contract{
		APIVersion: projectv2.APIVersion,
		Kind:       projectv2.Kind,
		Metadata:   projectv2.Metadata{Name: options.Name},
		Environments: []projectv2.Environment{
			{Name: "development", ApprovalPolicy: "automatic"},
			{Name: "production", Production: true, ApprovalPolicy: "required"},
		},
		Workspace: projectv2.WorkspacePolicy{
			ImageDigest: options.WorkspaceImageDigest, CPUMillis: 2000, MemoryMiB: 4096,
			TimeoutSeconds: 3600, NetworkProfile: "isolated", EgressPolicy: "governed", MaxArtifactMiB: 512,
		},
		Infrastructure: projectv2.InfrastructureSpec{Engine: "opentofu", Root: "infrastructure/tofu", StateBackend: "platform"},
		Build:          projectv2.BuildPolicy{DefaultDriver: "dockerfile", SupplyChainPolicy: "beta-default-v1"},
		GitOps:         projectv2.GitOpsPolicy{Engine: "argocd", Root: "deploy/environments"},
		Policy: projectv2.GovernancePolicy{
			ForbidClusterScopedResources: true, ProductionRequiresApproval: true,
			DestructiveApplyRequiresApproval: true, MonthlyCostApprovalThresholdMinorUnit: 1000,
		},
	}
	if err := contract.Validate(); err != nil {
		return nil, fmt.Errorf("render platform contract: %w", err)
	}
	platformYAML, err := yaml.Marshal(contract)
	if err != nil {
		return nil, err
	}
	return []application.BootstrapFile{
		{Path: "README.md", Update: true, Content: []byte(readme(options.Name))},
		{Path: "platform.yaml", Content: platformYAML},
		{Path: "infrastructure/tofu/versions.tf", Content: []byte(tofuVersions)},
		{Path: "infrastructure/tofu/main.tf", Content: []byte(tofuMain)},
		{Path: "deploy/base/kustomization.yaml", Content: []byte(baseKustomization)},
		{Path: "deploy/environments/development/kustomization.yaml", Content: []byte(environmentKustomization)},
		{Path: "deploy/environments/production/kustomization.yaml", Content: []byte(environmentKustomization)},
		{Path: "recipes.lock.yaml", Content: []byte(recipesLock)},
	}, nil
}

func readme(name string) string {
	return "# " + name + "\n\nThis private repository is managed by the AI-native DevOps Platform.\n\n" +
		"- `platform.yaml` is the strict project contract.\n" +
		"- `infrastructure/tofu` contains OpenTofu configuration.\n" +
		"- `deploy/environments` is the GitOps source of truth.\n" +
		"- Secrets must be written through Project MCP and never committed.\n"
}

const tofuVersions = `terraform {
  required_version = ">= 1.10.0"
}
`

const tofuMain = `# Declare project infrastructure here. State is provided by the platform HTTP backend.
`

const baseKustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`

const environmentKustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
`

const recipesLock = `apiVersion: recipes.platform.example.com/v1
kind: RecipeLock
recipes: []
`
