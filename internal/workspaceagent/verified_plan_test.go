package workspaceagent

import (
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
