package workspaceagent

import (
	"bytes"
	"strings"
	"testing"

	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

func TestVerifiedTofuApply_RequiresExactDigestAndCanonicalRelativePath(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	if expected, path, err := validateVerifiedApplyArguments([]string{digest, "infrastructure/saved.plan"}); err != nil || expected != strings.Repeat("a", 64) || path != "infrastructure/saved.plan" {
		t.Fatalf("expected=%q path=%q err=%v", expected, path, err)
	}
	for _, arguments := range [][]string{{digest, "../saved.plan"}, {digest, "/tmp/saved.plan"}, {"sha256:bad", "saved.plan"}, {digest, "saved.plan", "extra"}} {
		if _, _, err := validateVerifiedApplyArguments(arguments); err == nil {
			t.Fatalf("unsafe arguments accepted: %#v", arguments)
		}
	}
}

func TestVerifiedTofuPlanReceipt_RetentionComesFromStrictPlatformContract(t *testing.T) {
	contract := projectv2.Contract{Infrastructure: projectv2.InfrastructureSpec{Retention: []projectv2.RetentionRule{{
		Address: "cozystack_postgres.primary", Provider: "cozystack", ResourceType: "cozystack_postgres",
		ExternalID: "postgres-primary", Policy: "retain", Reason: "production data retention policy",
	}}}}
	retained := retainedResourcesFromContract(contract)
	if len(retained) != 1 || retained[0].Address != "cozystack_postgres.primary" || retained[0].Policy != "platform.yaml/v2:retain" || retained[0].Validate() != nil {
		t.Fatalf("retained=%+v", retained)
	}
}

func TestVerifiedTofuPlanReceipt_StripsAllBeforeAfterValues(t *testing.T) {
	raw := []byte(`{"format_version":"1.2","resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"],"before":{"token":"SECRET_SENTINEL"},"after":{"password":"SECRET_SENTINEL"}}}]}`)
	normalized, err := normalizePlanReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(normalized, []byte("SECRET_SENTINEL")) || bytes.Contains(normalized, []byte("before")) || bytes.Contains(normalized, []byte("after")) || !bytes.Contains(normalized, []byte("twc_server.app")) {
		t.Fatalf("unsafe normalized receipt: %s", normalized)
	}
}

func TestVerifiedTofuPlan_RequiresCanonicalPathAndExactSourceRevision(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if path, source, err := validateVerifiedPlanArguments([]string{"infrastructure/saved.plan", sha}); err != nil || path != "infrastructure/saved.plan" || source != sha {
		t.Fatalf("path=%q source=%q err=%v", path, source, err)
	}
	for _, arguments := range [][]string{{"../saved.plan", sha}, {"saved.plan", "main"}, {"saved.plan"}, {"saved.plan", sha, "extra"}} {
		if _, _, err := validateVerifiedPlanArguments(arguments); err == nil {
			t.Fatalf("unsafe verified plan arguments accepted: %#v", arguments)
		}
	}
}
