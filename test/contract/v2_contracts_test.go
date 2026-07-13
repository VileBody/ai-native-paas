package contract_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

func TestPlatformYAMLV2TemplateIsStrictAndValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "templates", "project-v2", "platform.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectv2.Parse(raw); err != nil {
		t.Fatalf("project template is invalid: %v", err)
	}
}

func TestBuildV2RequiresCompleteTrustChain(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	artifact := buildv2.ArtifactRef{ArtifactID: "artifact-1", Repository: "registry.example/project/app", Digest: digest, SBOMDigest: digest, ScanDigest: digest, SignatureDigest: digest, ProvenanceDigest: digest}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("complete trust chain rejected: %v", err)
	}
	artifact.SignatureDigest = ""
	if err := artifact.Validate(); err == nil {
		t.Fatal("unsigned artifact accepted")
	}
}

func TestCommerceV2UnknownPriceRequiresApproval(t *testing.T) {
	now := time.Now().UTC()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	estimate := commercev2.CostEstimate{
		EstimateID: "estimate-1", Version: "price-1", PlanHash: digest, RateCardID: "beta-1",
		Lines:   []commercev2.EstimateLine{{Meter: "vm.second", Quantity: 60, Unit: "second", ProviderCost: commercev2.Money{Currency: "RUB", MinorUnit: 100}, CustomerCost: commercev2.Money{Currency: "RUB", MinorUnit: 150}, PriceKnown: false}},
		Minimum: commercev2.Money{Currency: "RUB", MinorUnit: 100}, Maximum: commercev2.Money{Currency: "RUB", MinorUnit: 200}, ExpiresAt: now.Add(time.Hour),
	}
	if err := estimate.Validate(now); err == nil {
		t.Fatal("unknown price estimate without approval was accepted")
	}
	estimate.ApprovalRequired = true
	if err := estimate.Validate(now); err != nil {
		t.Fatalf("conservative approved estimate rejected: %v", err)
	}
}
