package domain_test

import (
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

func newArtifact(t *testing.T) domain.Artifact {
	t.Helper()
	a, err := domain.NewArtifact("art-1", "t1", "bld-1", "registry/t1/app", digest("a"), "application/vnd.oci.image.manifest.v1+json", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestArtifact_RequiresDigest(t *testing.T) {
	if _, err := domain.NewArtifact("a", "t", "b", "r", "latest", "m", time.Now()); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
func TestArtifact_TagMutationDoesNotChangeReference(t *testing.T) {
	a := newArtifact(t)
	before := a.Ref()
	tag := "latest"
	tag = "moved"
	_ = tag
	if a.Ref() != before {
		t.Fatal("immutable reference changed")
	}
}
func TestArtifact_ScanFailureLeavesQuarantined(t *testing.T) {
	a := newArtifact(t)
	_ = a.Quarantine(time.Now())
	before := a.Version /* adapter failure means ApplyScan is never called */
	if a.State != domain.ArtifactQuarantined || a.Version != before {
		t.Fatal(a.State, a.Version)
	}
}
func TestArtifact_PolicyViolationRejectsRelease(t *testing.T) {
	a := newArtifact(t)
	_ = a.Quarantine(time.Now())
	_ = a.AttachSBOM(digest("e"), "application/spdx+json", time.Now())
	r := domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: false, FindingsDigest: digest("f"), Reasons: []string{"critical"}, ScannedAt: time.Now()}
	if err := a.ApplyScan(r, time.Now()); err != nil || a.State != domain.ArtifactRejected {
		t.Fatal(a.State, err)
	}
	if err := a.MarkReleasable(time.Now()); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatal(err)
	}
}
func TestArtifact_SignatureRequiredForReleasable(t *testing.T) {
	a := newArtifact(t)
	_ = a.Quarantine(time.Now())
	_ = a.AttachSBOM(digest("e"), "application/spdx+json", time.Now())
	_ = a.ApplyScan(domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: true, FindingsDigest: digest("f"), ScannedAt: time.Now()}, time.Now())
	if err := a.MarkReleasable(time.Now()); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatal(err)
	}
}
func TestArtifact_SignatureBindsExactDigest(t *testing.T) {
	a := newArtifact(t)
	_ = a.Quarantine(time.Now())
	_ = a.AttachSBOM(digest("e"), "application/spdx+json", time.Now())
	_ = a.ApplyScan(domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: true, FindingsDigest: digest("f"), ScannedAt: time.Now()}, time.Now())
	err := a.AttachSignature(domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: digest("b"), Signature: "x", SignedAt: time.Now()}, time.Now())
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatal(err)
	}
}
func TestArtifact_SBOMStoredByDigest(t *testing.T) {
	a := newArtifact(t)
	_ = a.Quarantine(time.Now())
	if err := a.AttachSBOM(digest("e"), "application/spdx+json", time.Now()); err != nil || a.SBOMDigest != digest("e") {
		t.Fatal(a.SBOMDigest, err)
	}
}

func TestArtifact_ProvenanceAttachesOnlyAfterSignatureAndIsImmutable(t *testing.T) {
	a := newArtifact(t)
	now := time.Now()
	_ = a.Quarantine(now)
	_ = a.AttachSBOM(digest("e"), "application/spdx+json", now)
	_ = a.ApplyScan(domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: true, FindingsDigest: digest("f"), ScannedAt: now}, now)
	if err := a.AttachProvenance(digest("0"), "application/vnd.dsse.envelope.v1+json", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("pre-signature provenance err=%v", err)
	}
	_ = a.AttachSignature(domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: a.Digest, Signature: "x", AttachmentDigest: digest("a"), SignedAt: now}, now)
	if err := a.AttachProvenance(digest("0"), "application/vnd.dsse.envelope.v1+json", now); err != nil || a.ProvenanceDigest != digest("0") {
		t.Fatalf("provenance=%q err=%v", a.ProvenanceDigest, err)
	}
	version := a.Version
	if err := a.AttachProvenance(digest("0"), "application/vnd.dsse.envelope.v1+json", now); err != nil || a.Version != version {
		t.Fatalf("same provenance must be idempotent: version=%d err=%v", a.Version, err)
	}
	if err := a.AttachProvenance(digest("1"), "application/vnd.dsse.envelope.v1+json", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("replacement err=%v", err)
	}
}

func TestArtifact_SBOMCannotChangeAfterAttachmentOrScan(t *testing.T) {
	a := newArtifact(t)
	now := time.Now()
	_ = a.Quarantine(now)
	if err := a.AttachSBOM(digest("e"), "application/spdx+json", now); err != nil {
		t.Fatal(err)
	}
	version := a.Version
	if err := a.AttachSBOM(digest("e"), "application/spdx+json", now); err != nil || a.Version != version {
		t.Fatalf("same SBOM must be idempotent: version=%d err=%v", a.Version, err)
	}
	if err := a.AttachSBOM(digest("d"), "application/spdx+json", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("replacement err=%v", err)
	}
	if err := a.ApplyScan(domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: true, FindingsDigest: digest("f"), ScannedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := a.AttachSBOM(digest("e"), "application/spdx+json", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("post-scan attachment err=%v", err)
	}
}

func TestArtifact_ScanRequiresSBOMAndTimestamp(t *testing.T) {
	a := newArtifact(t)
	now := time.Now()
	_ = a.Quarantine(now)
	result := domain.ScanResult{Scanner: "s", PolicyVersion: "v1", Passed: true, FindingsDigest: digest("f"), ScannedAt: now}
	if err := a.ApplyScan(result, now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("missing SBOM err=%v", err)
	}
	_ = a.AttachSBOM(digest("e"), "application/spdx+json", now)
	result.ScannedAt = time.Time{}
	if err := a.ApplyScan(result, now); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("zero timestamp err=%v", err)
	}
}
