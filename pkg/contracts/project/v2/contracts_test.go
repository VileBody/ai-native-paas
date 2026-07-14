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

func TestPlatformYAMLV2_RetentionRulesAreStrictAndExplicit(t *testing.T) {
	withRetention := stringsReplace(validContract, "  stateBackend: platform\n", `  stateBackend: platform
  retention:
    - address: cozystack_postgres.primary
      provider: cozystack
      resourceType: cozystack_postgres
      externalId: postgres-primary
      policy: retain
      reason: production data retention policy
`)
	contract, err := Parse([]byte(withRetention))
	if err != nil || len(contract.Infrastructure.Retention) != 1 || contract.Infrastructure.Retention[0].Policy != "retain" {
		t.Fatalf("retention=%+v error=%v", contract.Infrastructure.Retention, err)
	}
	for _, invalid := range []string{
		stringsReplace(withRetention, "policy: retain", "policy: destroy"),
		stringsReplace(withRetention, "cozystack_postgres.primary", ""),
		stringsReplace(withRetention, "provider: cozystack", "provider: ''"),
		stringsReplace(withRetention, "production data retention policy", ""),
		stringsReplace(withRetention, "      reason: production data retention policy\n", "      reason: production data retention policy\n    - address: cozystack_postgres.primary\n      provider: cozystack\n      resourceType: cozystack_postgres\n      policy: retain\n      reason: duplicate\n"),
	} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatal("invalid retention rule accepted")
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
