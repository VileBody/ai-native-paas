package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type Severity int

const (
	SeverityUnknown Severity = iota
	SeverityLow
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Package  string   `json:"package,omitempty"`
}
type FindingsProvider interface {
	Find(context.Context, buildv1.ArtifactRef, application.SBOMResult) ([]Finding, error)
}
type PolicyScanner struct {
	Provider       FindingsProvider
	MaximumAllowed Severity
	PolicyVersion  string
	Now            func() time.Time
}

func (s PolicyScanner) Scan(ctx context.Context, artifact buildv1.ArtifactRef, sbom application.SBOMResult) (domain.ScanResult, error) {
	if s.Provider == nil {
		return domain.ScanResult{}, domain.NewError(domain.CodeUnavailable, "scanner provider unavailable")
	}
	findings, err := s.Provider.Find(ctx, artifact, sbom)
	if err != nil {
		return domain.ScanResult{}, domain.Retryable(domain.CodePlatformFailure, "scanner unavailable", err)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].ID != findings[j].ID {
			return findings[i].ID < findings[j].ID
		}
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity < findings[j].Severity
		}
		return findings[i].Package < findings[j].Package
	})
	raw, _ := json.Marshal(findings)
	sum := sha256.Sum256(raw)
	highest := SeverityUnknown
	var reasons []string
	for _, f := range findings {
		if f.Severity > highest {
			highest = f.Severity
		}
		if s.MaximumAllowed > 0 && f.Severity > s.MaximumAllowed {
			reasons = append(reasons, f.ID+":"+severityName(f.Severity))
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if s.Now != nil {
		now = s.Now().UTC().Truncate(time.Microsecond)
	}
	version := s.PolicyVersion
	if version == "" {
		version = "v1"
	}
	return domain.ScanResult{Scanner: "policy-scanner", PolicyVersion: version, Passed: len(reasons) == 0, HighestSeverity: severityName(highest), FindingsDigest: "sha256:" + hex.EncodeToString(sum[:]), Reasons: reasons, ScannedAt: now}, nil
}
func severityName(v Severity) string {
	switch v {
	case SeverityLow:
		return "LOW"
	case SeverityMedium:
		return "MEDIUM"
	case SeverityHigh:
		return "HIGH"
	case SeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

type StaticProvider struct {
	Findings []Finding
	Err      error
}

func (p StaticProvider) Find(context.Context, buildv1.ArtifactRef, application.SBOMResult) ([]Finding, error) {
	return append([]Finding(nil), p.Findings...), p.Err
}

var _ application.Scanner = PolicyScanner{}
