package v2

import "testing"

const validContract = `apiVersion: platform.example.com/v2
kind: Project
metadata:
  name: hello-go
environments:
  - name: preview
    production: false
    approvalPolicy: automatic
  - name: production
    production: true
    approvalPolicy: required
workspace:
  imageDigest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  cpuMillis: 2000
  memoryMiB: 4096
  timeoutSeconds: 3600
  networkProfile: isolated
  egressPolicy: governed
  maxArtifactMiB: 512
infrastructure:
  engine: opentofu
  root: infrastructure/tofu
  stateBackend: platform
build:
  defaultDriver: dockerfile
  supplyChainPolicy: beta-default-v1
gitops:
  engine: argocd
  root: deploy/environments
policy:
  forbidClusterScopedResources: true
  productionRequiresApproval: true
  destructiveApplyRequiresApproval: true
  monthlyCostApprovalThresholdMinorUnit: 5000
`

func TestPlatformYAMLV2_StrictAndSafe(t *testing.T) {
	contract, err := Parse([]byte(validContract))
	if err != nil {
		t.Fatalf("parse valid contract: %v", err)
	}
	if contract.Metadata.Name != "hello-go" || len(contract.Environments) != 2 {
		t.Fatalf("unexpected contract: %#v", contract)
	}
	for _, mutation := range []string{
		validContract + "unknown: true\n",
		stringsReplace(validContract, "root: infrastructure/tofu", "root: ../outside"),
		stringsReplace(validContract, "approvalPolicy: required", "approvalPolicy: automatic"),
		stringsReplace(validContract, "forbidClusterScopedResources: true", "forbidClusterScopedResources: false"),
	} {
		if _, err := Parse([]byte(mutation)); err == nil {
			t.Fatalf("unsafe contract accepted:\n%s", mutation)
		}
	}
}

func stringsReplace(value, old, replacement string) string {
	for i := 0; i+len(old) <= len(value); i++ {
		if value[i:i+len(old)] == old {
			return value[:i] + replacement + value[i+len(old):]
		}
	}
	return value
}
