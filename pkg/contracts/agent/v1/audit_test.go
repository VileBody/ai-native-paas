package v1

import (
	"strings"
	"testing"
)

func TestAuditEvidence_ValidatesLowercaseGitAndDigestReferences(t *testing.T) {
	evidence := AuditEvidence{
		ProjectID: "project-1", CommitSHA: "3097ba13fbfcb41bc2b81becc6011a4a38547ba0",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), GitOpsRevision: strings.Repeat("c", 40),
		ReadyEndpoint: "https://booking.apps.example.test/health",
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	evidence.CommitSHA = "not-a-sha"
	if err := evidence.Validate(); err == nil {
		t.Fatal("invalid Git revision accepted")
	}
}
