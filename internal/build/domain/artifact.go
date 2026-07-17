package domain

import (
	"strings"
	"time"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type ArtifactState string

const (
	ArtifactDiscovered  ArtifactState = "DISCOVERED"
	ArtifactQuarantined ArtifactState = "QUARANTINED"
	ArtifactScanned     ArtifactState = "SCANNED"
	ArtifactSigned      ArtifactState = "SIGNED"
	ArtifactReleasable  ArtifactState = "RELEASABLE"
	ArtifactRejected    ArtifactState = "REJECTED"
)

type ScanResult struct {
	Scanner         string
	PolicyVersion   string
	Passed          bool
	HighestSeverity string
	FindingsDigest  string
	Reasons         []string
	ScannedAt       time.Time
}

type SignatureRecord struct {
	Issuer           string
	Algorithm        string
	Digest           string
	Signature        string
	AttachmentDigest string
	SignedAt         time.Time
}

type Artifact struct {
	ID                  string
	TenantID            string
	BuildID             string
	Repository          string
	Digest              string
	MediaType           string
	State               ArtifactState
	SBOMDigest          string
	SBOMMediaType       string
	ProvenanceDigest    string
	ProvenanceMediaType string
	Scan                *ScanResult
	Signature           *SignatureRecord
	RejectionCode       string
	RejectionNotes      []string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func NewArtifact(id, tenantID, buildID, repository, digest, mediaType string, now time.Time) (Artifact, error) {
	ref := buildv1.ArtifactRef{ArtifactID: id, Repository: repository, Digest: digest, MediaType: mediaType}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(buildID) == "" || ref.Validate() != nil {
		return Artifact{}, NewError(CodeInvalidArgument, "invalid artifact")
	}
	return Artifact{ID: id, TenantID: tenantID, BuildID: buildID, Repository: repository, Digest: digest, MediaType: mediaType, State: ArtifactDiscovered, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}
func (a Artifact) Ref() buildv1.ArtifactRef {
	return buildv1.ArtifactRef{ArtifactID: a.ID, Repository: a.Repository, Digest: a.Digest, MediaType: a.MediaType}
}
func (a *Artifact) Quarantine(now time.Time) error {
	if a.State == ArtifactQuarantined {
		return nil
	}
	if a.State != ArtifactDiscovered {
		return NewError(CodeConflict, "artifact cannot enter quarantine")
	}
	a.State = ArtifactQuarantined
	a.bump(now)
	return nil
}
func (a *Artifact) AttachSBOM(digest, mediaType string, now time.Time) error {
	if a.State != ArtifactQuarantined {
		return NewError(CodeConflict, "SBOM can only attach to quarantined artifact")
	}
	mediaType = strings.TrimSpace(mediaType)
	if !validDigest(digest) || mediaType == "" {
		return NewError(CodeInvalidArgument, "invalid SBOM reference")
	}
	if a.SBOMDigest != "" {
		if a.SBOMDigest == digest && a.SBOMMediaType == mediaType {
			return nil
		}
		return NewError(CodeConflict, "artifact SBOM reference is immutable")
	}
	a.SBOMDigest = digest
	a.SBOMMediaType = mediaType
	a.bump(now)
	return nil
}
func (a *Artifact) ApplyScan(result ScanResult, now time.Time) error {
	if a.State != ArtifactQuarantined {
		return NewError(CodeConflict, "artifact is not quarantined")
	}
	if !validDigest(a.SBOMDigest) || strings.TrimSpace(a.SBOMMediaType) == "" {
		return NewError(CodeConflict, "artifact must have an immutable SBOM before scanning")
	}
	if strings.TrimSpace(result.Scanner) == "" || strings.TrimSpace(result.PolicyVersion) == "" || !validDigest(result.FindingsDigest) || result.ScannedAt.IsZero() {
		return NewError(CodeInvalidArgument, "invalid scan result")
	}
	copyResult := result
	copyResult.Reasons = append([]string(nil), result.Reasons...)
	copyResult.ScannedAt = result.ScannedAt.UTC().Truncate(time.Microsecond)
	a.Scan = &copyResult
	if result.Passed {
		a.State = ArtifactScanned
	} else {
		a.State = ArtifactRejected
		a.RejectionCode = string(CodePolicyRejected)
		a.RejectionNotes = append([]string(nil), result.Reasons...)
	}
	a.bump(now)
	return nil
}
func (a *Artifact) AttachSignature(signature SignatureRecord, now time.Time) error {
	if a.State != ArtifactScanned {
		return NewError(CodeConflict, "artifact must be scanned before signing")
	}
	if signature.Digest != a.Digest || strings.TrimSpace(signature.Issuer) == "" || strings.TrimSpace(signature.Algorithm) == "" || strings.TrimSpace(signature.Signature) == "" || signature.SignedAt.IsZero() {
		return NewError(CodeInvalidArgument, "signature does not bind artifact digest")
	}
	copySignature := signature
	copySignature.SignedAt = signature.SignedAt.UTC().Truncate(time.Microsecond)
	a.Signature = &copySignature
	a.State = ArtifactSigned
	a.bump(now)
	return nil
}
func (a *Artifact) AttachProvenance(digest, mediaType string, now time.Time) error {
	if a.State != ArtifactSigned {
		return NewError(CodeConflict, "provenance can only attach after signature")
	}
	mediaType = strings.TrimSpace(mediaType)
	if !validDigest(digest) || mediaType == "" {
		return NewError(CodeInvalidArgument, "invalid provenance reference")
	}
	if a.ProvenanceDigest != "" {
		if a.ProvenanceDigest == digest && a.ProvenanceMediaType == mediaType {
			return nil
		}
		return NewError(CodeConflict, "artifact provenance reference is immutable")
	}
	a.ProvenanceDigest = digest
	a.ProvenanceMediaType = mediaType
	a.bump(now)
	return nil
}
func (a *Artifact) MarkReleasable(now time.Time) error {
	if a.State == ArtifactReleasable {
		return nil
	}
	if a.State != ArtifactSigned || a.Signature == nil || a.Signature.Digest != a.Digest || strings.TrimSpace(a.Signature.Issuer) == "" || strings.TrimSpace(a.Signature.Algorithm) == "" || strings.TrimSpace(a.Signature.Signature) == "" || !validDigest(a.Signature.AttachmentDigest) || !validDigest(a.SBOMDigest) || strings.TrimSpace(a.SBOMMediaType) == "" || a.Scan == nil || !a.Scan.Passed {
		return NewError(CodeConflict, "artifact trust chain is incomplete")
	}
	a.State = ArtifactReleasable
	a.bump(now)
	return nil
}
func (a *Artifact) bump(now time.Time) {
	a.Version++
	a.UpdatedAt = now.UTC()
}
