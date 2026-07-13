package application

import (
	"context"
	"errors"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

// TrustPolicy is the frozen Iteration-3 boundary consumed by Runtime Delivery.
// It re-resolves the artifact by immutable ID, confirms the registry still
// resolves the exact digest, and cryptographically verifies the persisted
// signature before allowing release.
type TrustPolicy struct {
	Store         Store
	Registry      Registry
	Verifier      SignatureVerifier
	PolicyVersion string
}

func (p TrustPolicy) IsReleasable(ctx context.Context, ref buildv1.ArtifactRef) (buildv1.ReleasabilityDecision, error) {
	if err := ref.Validate(); err != nil {
		return buildv1.ReleasabilityDecision{}, domain.NewError(domain.CodeInvalidArgument, "invalid artifact reference")
	}
	if p.Store == nil || p.Registry == nil || p.Verifier == nil {
		return buildv1.ReleasabilityDecision{}, domain.NewError(domain.CodeUnavailable, "artifact trust policy is not configured")
	}
	var artifact domain.Artifact
	err := p.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetArtifact(ref.ArtifactID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "artifact not found")
		}
		artifact = value
		return nil
	})
	if err != nil {
		return buildv1.ReleasabilityDecision{}, err
	}
	version := p.PolicyVersion
	if version == "" {
		version = "artifact-trust-v1"
	}
	decision := buildv1.ReleasabilityDecision{PolicyVersion: version}
	if artifact.Repository != ref.Repository || artifact.Digest != ref.Digest || artifact.MediaType != ref.MediaType {
		decision.Reasons = []string{"artifact identity mismatch"}
		return decision, nil
	}
	if artifact.State != domain.ArtifactReleasable || artifact.Scan == nil || !artifact.Scan.Passed || artifact.Signature == nil || artifact.Signature.Digest != artifact.Digest || !buildv1.ValidDigest(artifact.Signature.AttachmentDigest) || !buildv1.ValidDigest(artifact.SBOMDigest) {
		decision.Reasons = []string{"artifact trust chain is incomplete"}
		return decision, nil
	}
	resolved, err := p.Registry.Resolve(ctx, artifact.TenantID, artifact.Repository+"@"+artifact.Digest)
	if err != nil {
		return buildv1.ReleasabilityDecision{}, err
	}
	if resolved.Repository != artifact.Repository || resolved.Digest != artifact.Digest || resolved.MediaType != artifact.MediaType {
		decision.Reasons = []string{"registry identity mismatch"}
		return decision, nil
	}
	if err := p.Verifier.Verify(ctx, artifact.Repository, *artifact.Signature); err != nil {
		var typed *domain.Error
		if errors.As(err, &typed) && typed.Code == domain.CodePolicyRejected {
			decision.Reasons = []string{typed.Message}
			return decision, nil
		}
		return buildv1.ReleasabilityDecision{}, err
	}
	decision.Allowed = true
	return decision, nil
}

var _ buildv1.ArtifactPolicy = TrustPolicy{}
