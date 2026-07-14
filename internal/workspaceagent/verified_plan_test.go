package workspaceagent

import (
	"bytes"
	"strings"
	"testing"
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
