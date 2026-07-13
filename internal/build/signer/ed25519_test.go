package signer_test

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/signer"
)

func TestSignerAdapter_SignsDigestNotTag(t *testing.T) {
	s, v, err := signer.New("platform-eu1")
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	record, err := s.Sign(context.Background(), "registry/t1/app", digest)
	if err != nil {
		t.Fatal(err)
	}
	if record.Digest != digest {
		t.Fatal(record.Digest)
	}
	if err := v.Verify(context.Background(), "registry/t1/app", record); err != nil {
		t.Fatal(err)
	}
	record.Digest = "sha256:" + strings.Repeat("b", 64)
	if err := v.Verify(context.Background(), "registry/t1/app", record); !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
}
func TestVerifier_RejectsUnknownIssuer(t *testing.T) {
	s, _, _ := signer.New("unknown")
	record, _ := s.Sign(context.Background(), "r", "sha256:"+strings.Repeat("a", 64))
	v := signer.Verifier{TrustedIssuers: map[string]ed25519.PublicKey{}}
	if err := v.Verify(context.Background(), "r", record); !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
}
