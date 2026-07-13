package signer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type Signer struct {
	Issuer     string
	PrivateKey ed25519.PrivateKey
	Now        func() time.Time
}

func (s Signer) Sign(ctx context.Context, repository, digest string) (domain.SignatureRecord, error) {
	if err := ctx.Err(); err != nil {
		return domain.SignatureRecord{}, err
	}
	if s.Issuer == "" || len(s.PrivateKey) != ed25519.PrivateKeySize || !buildv1.ValidDigest(digest) {
		return domain.SignatureRecord{}, domain.NewError(domain.CodeUnavailable, "signer is not configured")
	}
	payload := []byte(repository + "\n" + digest)
	sig := ed25519.Sign(s.PrivateKey, payload)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if s.Now != nil {
		now = s.Now().UTC().Truncate(time.Microsecond)
	}
	return domain.SignatureRecord{Issuer: s.Issuer, Algorithm: "ed25519", Digest: digest, Signature: base64.RawStdEncoding.EncodeToString(sig), SignedAt: now}, nil
}

type Verifier struct{ TrustedIssuers map[string]ed25519.PublicKey }

func (v Verifier) Verify(ctx context.Context, repository string, record domain.SignatureRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, ok := v.TrustedIssuers[record.Issuer]
	if !ok {
		return domain.NewError(domain.CodePolicyRejected, "unknown signature issuer")
	}
	if record.Algorithm != "ed25519" || !buildv1.ValidDigest(record.Digest) {
		return domain.NewError(domain.CodePolicyRejected, "invalid signature metadata")
	}
	raw, err := base64.RawStdEncoding.DecodeString(record.Signature)
	if err != nil {
		return domain.NewError(domain.CodePolicyRejected, "invalid signature encoding")
	}
	if !ed25519.Verify(key, []byte(repository+"\n"+record.Digest), raw) {
		return domain.NewError(domain.CodePolicyRejected, "signature verification failed")
	}
	return nil
}
func New(issuer string) (Signer, Verifier, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Signer{}, Verifier{}, err
	}
	if strings.TrimSpace(issuer) == "" {
		return Signer{}, Verifier{}, errors.New("issuer required")
	}
	return Signer{Issuer: issuer, PrivateKey: priv}, Verifier{TrustedIssuers: map[string]ed25519.PublicKey{issuer: pub}}, nil
}

var _ application.Signer = Signer{}
var _ application.SignatureVerifier = Verifier{}
