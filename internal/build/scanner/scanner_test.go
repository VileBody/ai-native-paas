package scanner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/scanner"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

func ref() buildv1.ArtifactRef {
	return buildv1.ArtifactRef{ArtifactID: "a", Repository: "r", Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "m"}
}
func TestScannerAdapter_MapsPolicyThresholds(t *testing.T) {
	s := scanner.PolicyScanner{Provider: scanner.StaticProvider{Findings: []scanner.Finding{{ID: "CVE-HIGH", Severity: scanner.SeverityHigh}, {ID: "CVE-LOW", Severity: scanner.SeverityLow}}}, MaximumAllowed: scanner.SeverityMedium, PolicyVersion: "policy-1"}
	result, err := s.Scan(context.Background(), ref(), application.SBOMResult{})
	if err != nil || result.Passed || result.HighestSeverity != "HIGH" || len(result.Reasons) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestScannerAdapter_OperationalFailureIsRetryablePlatformFailure(t *testing.T) {
	s := scanner.PolicyScanner{Provider: scanner.StaticProvider{Err: errors.New("registry timeout")}}
	_, err := s.Scan(context.Background(), ref(), application.SBOMResult{})
	if !domain.HasCode(err, domain.CodePlatformFailure) || !domain.IsRetryable(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestScannerAdapter_DeterministicForEqualIDsAndShuffledInput(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	firstFindings := []scanner.Finding{
		{ID: "CVE-SAME", Severity: scanner.SeverityHigh, Package: "zlib"},
		{ID: "CVE-SAME", Severity: scanner.SeverityLow, Package: "alpha"},
		{ID: "CVE-OTHER", Severity: scanner.SeverityMedium, Package: "openssl"},
	}
	secondFindings := []scanner.Finding{firstFindings[2], firstFindings[0], firstFindings[1]}
	first, err := (scanner.PolicyScanner{Provider: scanner.StaticProvider{Findings: firstFindings}, MaximumAllowed: scanner.SeverityMedium, PolicyVersion: "policy-1", Now: func() time.Time { return now }}).Scan(context.Background(), ref(), application.SBOMResult{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := (scanner.PolicyScanner{Provider: scanner.StaticProvider{Findings: secondFindings}, MaximumAllowed: scanner.SeverityMedium, PolicyVersion: "policy-1", Now: func() time.Time { return now }}).Scan(context.Background(), ref(), application.SBOMResult{})
	if err != nil {
		t.Fatal(err)
	}
	if first.FindingsDigest != second.FindingsDigest || strings.Join(first.Reasons, "|") != strings.Join(second.Reasons, "|") {
		t.Fatalf("nondeterministic results: first=%+v second=%+v", first, second)
	}
}
