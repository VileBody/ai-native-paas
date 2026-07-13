package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
)

func trustedArtifact(t *testing.T, store application.Store) domain.Artifact {
	t.Helper()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	artifact, err := domain.NewArtifact("artifact-1", "tenant-1", "build-1", "registry.test/tenants/tenant-1/apps/project-1", "sha256:"+strings.Repeat("a", 64), "application/vnd.oci.image.manifest.v1+json", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Quarantine(now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.AttachSBOM("sha256:"+strings.Repeat("b", 64), "application/spdx+json", now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.ApplyScan(domain.ScanResult{Scanner: "scanner", PolicyVersion: "v1", Passed: true, FindingsDigest: "sha256:" + strings.Repeat("c", 64), ScannedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.AttachSignature(domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: artifact.Digest, Signature: "sig", AttachmentDigest: "sha256:" + strings.Repeat("d", 64), SignedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.MarkReleasable(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx application.Tx) error { return tx.InsertArtifact(artifact) }); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestTrustPolicy_RequiresExactPersistedIdentityAndCompleteTrustChain(t *testing.T) {
	store := memory.New()
	artifact := trustedArtifact(t, store)
	registry := &testkit.Registry{Published: application.PublishedArtifact{Repository: artifact.Repository, Digest: artifact.Digest, MediaType: artifact.MediaType}}
	verifier := &testkit.Verifier{}
	policy := application.TrustPolicy{Store: store, Registry: registry, Verifier: verifier, PolicyVersion: "policy-v1"}
	decision, err := policy.IsReleasable(context.Background(), artifact.Ref())
	if err != nil || !decision.Allowed || decision.PolicyVersion != "policy-v1" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	tampered := artifact.Ref()
	tampered.Digest = "sha256:" + strings.Repeat("e", 64)
	decision, err = policy.IsReleasable(context.Background(), tampered)
	if err != nil || decision.Allowed {
		t.Fatalf("tampered decision=%+v err=%v", decision, err)
	}
}

func TestTrustPolicy_RejectsUnverifiablePersistedSignature(t *testing.T) {
	store := memory.New()
	artifact := trustedArtifact(t, store)
	registry := &testkit.Registry{Published: application.PublishedArtifact{Repository: artifact.Repository, Digest: artifact.Digest, MediaType: artifact.MediaType}}
	verifier := &testkit.Verifier{Err: domain.NewError(domain.CodePolicyRejected, "signature verification failed")}
	policy := application.TrustPolicy{Store: store, Registry: registry, Verifier: verifier}
	decision, err := policy.IsReleasable(context.Background(), artifact.Ref())
	if err != nil || decision.Allowed || len(decision.Reasons) == 0 {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	verifier.Err = errors.New("kms unavailable")
	if _, err := policy.IsReleasable(context.Background(), artifact.Ref()); err == nil {
		t.Fatal("operational verifier failure must propagate")
	}
}
