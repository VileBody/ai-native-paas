package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
)

type IngestVerifiedBuildReceiptCommand struct {
	TenantID, ActorID, IdempotencyKey string
	BuildID                           string
	Receipt                           buildv2.VerifiedBuildReceipt
}

type IngestVerifiedBuildReceiptResult struct {
	Build      domain.Build         `json:"build"`
	Artifact   *domain.Artifact     `json:"artifact,omitempty"`
	TrustChain *buildv2.ArtifactRef `json:"trust_chain,omitempty"`
}

func (s *Service) IngestVerifiedBuildReceipt(ctx context.Context, cmd IngestVerifiedBuildReceiptCommand) (IngestVerifiedBuildReceiptResult, error) {
	if s.Store == nil || s.Fetcher == nil || s.Registry == nil || s.SBOM == nil || s.Scanner == nil || s.Signer == nil || s.Verifier == nil || s.Provenance == nil || s.ProvenanceVerifier == nil || s.Clock == nil || s.IDs == nil {
		return IngestVerifiedBuildReceiptResult{}, domain.NewError(domain.CodeUnavailable, "verified build receipt pipeline is not configured")
	}
	if strings.TrimSpace(cmd.TenantID) == "" || strings.TrimSpace(cmd.ActorID) == "" || strings.TrimSpace(cmd.IdempotencyKey) == "" || strings.TrimSpace(cmd.BuildID) == "" {
		return IngestVerifiedBuildReceiptResult{}, domain.NewError(domain.CodeInvalidArgument, "missing verified build receipt fields")
	}
	receipt, err := canonicalVerifiedBuildReceipt(cmd.Receipt)
	if err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	requestHash := hashJSON(struct {
		BuildID string                       `json:"build_id"`
		Receipt buildv2.VerifiedBuildReceipt `json:"receipt"`
	}{BuildID: cmd.BuildID, Receipt: receipt})

	var build domain.Build
	var existingArtifact *domain.Artifact
	var result IngestVerifiedBuildReceiptResult
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != "build.verified_receipt.v2" || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if !record.Completed {
				return domain.NewError(domain.CodeConflict, "verified build receipt is in progress")
			}
			return json.Unmarshal(record.Result, &result)
		}
		var ok bool
		build, ok = tx.GetBuild(cmd.BuildID)
		if !ok || build.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "build not found")
		}
		if value, found := tx.FindArtifactByBuild(build.ID); found {
			copyValue := value
			existingArtifact = &copyValue
		}
		if err := validateReceiptForBuild(build, receipt, s.repository(build)); err != nil {
			return err
		}
		if build.Terminal() {
			if existingArtifact == nil || existingArtifact.State != domain.ArtifactReleasable || existingArtifact.Digest != receipt.Digest || existingArtifact.Repository != receipt.Repository || !buildv1.ValidDigest(existingArtifact.ProvenanceDigest) {
				return domain.NewError(domain.CodeConflict, "terminal build does not match verified receipt")
			}
			result = receiptResult(build, existingArtifact)
			return s.completeVerifiedReceiptIdempotency(tx, cmd, requestHash, result)
		}
		if build.State != buildv1.BuildQueued {
			return domain.NewError(domain.CodeConflict, "build is already being processed")
		}
		return nil
	})
	if err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	if result.Build.ID != "" {
		return result, nil
	}

	if err := s.startBuild(ctx, cmd.ActorID, &build); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	snapshot, err := s.Fetcher.Fetch(ctx, build.TenantID, build.Source, s.SourceLimits)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, err)
	}
	if snapshot.Cleanup != nil {
		defer snapshot.Cleanup()
	}
	if err := s.advance(ctx, &build, buildv1.BuildDetecting); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	detection, selectionSource, err := s.resolveExecution(ctx, snapshot.Path, build)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, err)
	}
	if selectionSource != "explicit-build-spec" || detection.Backend != BackendDockerfile {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, domain.NewError(domain.CodePolicyRejected, "verified receipt requires an explicit Dockerfile build spec"))
	}
	if err := s.selectExecution(ctx, cmd.ActorID, &build, detection, selectionSource); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	if err := s.advance(ctx, &build, buildv1.BuildExporting); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	published, err := s.Registry.Resolve(ctx, build.TenantID, receipt.Repository+"@"+receipt.Digest)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, err)
	}
	if published.Repository != receipt.Repository || published.Digest != receipt.Digest || published.MediaType != receipt.MediaType {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, domain.NewError(domain.CodePlatformFailure, "registry receipt resolved to a different artifact"))
	}

	created, err := domain.NewArtifact(s.IDs.NewID("art"), build.TenantID, build.ID, published.Repository, published.Digest, published.MediaType, s.Clock.Now())
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, err)
	}
	if err := created.Quarantine(s.Clock.Now()); err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, existingArtifact, err)
	}
	if err := s.insertArtifact(ctx, created); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	artifact := &created

	sbom, err := s.SBOM.Generate(ctx, snapshot, published)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	storedSBOM, err := s.Registry.StoreAttachment(ctx, build.TenantID, published, sbom.MediaType, sbom.Document)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if storedSBOM != sbom.Digest {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "registry changed SBOM digest"))
	}
	if err := s.updateArtifact(ctx, artifact, func(a *domain.Artifact) error { return a.AttachSBOM(sbom.Digest, sbom.MediaType, s.Clock.Now()) }); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	if err := s.advance(ctx, &build, buildv1.BuildScanning); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	scan, err := s.Scanner.Scan(ctx, artifact.Ref(), sbom)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if err := s.updateArtifact(ctx, artifact, func(a *domain.Artifact) error { return a.ApplyScan(scan, s.Clock.Now()) }); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	if artifact.State == domain.ArtifactRejected {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.NewError(domain.CodePolicyRejected, strings.Join(artifact.RejectionNotes, "; ")))
	}
	if err := s.advance(ctx, &build, buildv1.BuildSigning); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	signature, err := s.Signer.Sign(ctx, artifact.Repository, artifact.Digest)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if err := s.Verifier.Verify(ctx, artifact.Repository, signature); err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.Wrap(domain.CodePlatformFailure, "platform signature failed verification", err))
	}
	signatureDocument, err := json.Marshal(signature)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.Wrap(domain.CodePlatformFailure, "encode signature attachment", err))
	}
	signatureAttachment, err := s.Registry.StoreAttachment(ctx, build.TenantID, publishedArtifact(*artifact), "application/vnd.dev.cosign.simplesigning.v1+json", signatureDocument)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if !buildv1.ValidDigest(signatureAttachment) {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "registry returned invalid signature attachment digest"))
	}
	signature.AttachmentDigest = signatureAttachment
	artifactExpected := artifact.Version
	if err := artifact.AttachSignature(signature, s.Clock.Now()); err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}

	finishedAt := s.Clock.Now()
	provenance, err := s.Provenance.Attest(ctx, ProvenanceMaterials{
		BuildID: build.ID, Repository: artifact.Repository, SourceSHA: build.Source.CommitSHA,
		BuildSpecDigest: build.BuildSpecDigest, BuilderDigest: build.BuilderDigest, OutputDigest: artifact.Digest,
		StartedAt: build.StartedAt, FinishedAt: finishedAt,
	})
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	verifiedProvenance, err := s.ProvenanceVerifier.Verify(ctx, provenance.Document)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.Wrap(domain.CodePlatformFailure, "platform provenance failed verification", err))
	}
	if verifiedProvenance.Digest != provenance.Digest || verifiedProvenance.MediaType != provenance.MediaType {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "provenance verifier returned a mismatched attachment"))
	}
	provenanceAttachment, err := s.Registry.StoreAttachment(ctx, build.TenantID, publishedArtifact(*artifact), provenance.MediaType, provenance.Document)
	if err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if provenanceAttachment != provenance.Digest {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "registry changed provenance digest"))
	}
	if err := artifact.AttachProvenance(provenanceAttachment, provenance.MediaType, s.Clock.Now()); err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	if err := artifact.MarkReleasable(s.Clock.Now()); err != nil {
		return s.failReceipt(ctx, cmd.ActorID, build, artifact, err)
	}
	buildExpected := build.Version
	if err := build.Succeed(artifact.ID, s.Clock.Now()); err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	result = receiptResult(build, artifact)
	if result.TrustChain == nil {
		return IngestVerifiedBuildReceiptResult{}, domain.NewError(domain.CodePlatformFailure, "verified receipt trust chain is incomplete")
	}
	payload, _ := json.Marshal(map[string]any{"build_id": build.ID, "artifact_id": artifact.ID, "trust_chain": result.TrustChain})
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if err := tx.UpdateArtifact(*artifact, artifactExpected); err != nil {
			return err
		}
		if err := tx.UpdateBuild(build, buildExpected); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.verified_receipt.accepted.v2", AggregateID: build.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "artifact.releasable.v2", AggregateID: artifact.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: build.TenantID, ActorID: cmd.ActorID, Action: "build.verified_receipt.accept", ResourceType: "build", ResourceID: build.ID, Data: payload, CreatedAt: now}); err != nil {
			return err
		}
		return s.completeVerifiedReceiptIdempotency(tx, cmd, requestHash, result)
	})
	if err != nil {
		return IngestVerifiedBuildReceiptResult{}, err
	}
	return result, nil
}

func canonicalVerifiedBuildReceipt(receipt buildv2.VerifiedBuildReceipt) (buildv2.VerifiedBuildReceipt, error) {
	receipt.SourceSHA = strings.ToLower(strings.TrimSpace(receipt.SourceSHA))
	receipt.SpecDigest = strings.TrimSpace(receipt.SpecDigest)
	receipt.Repository = strings.TrimSpace(receipt.Repository)
	receipt.Digest = strings.TrimSpace(receipt.Digest)
	receipt.MediaType = strings.TrimSpace(receipt.MediaType)
	receipt.Builder = strings.TrimSpace(receipt.Builder)
	receipt.BuilderAddr = strings.TrimSpace(receipt.BuilderAddr)
	receipt.CapturedAt = receipt.CapturedAt.UTC()
	if err := receipt.Validate(); err != nil {
		return buildv2.VerifiedBuildReceipt{}, domain.NewError(domain.CodeInvalidArgument, "invalid verified build receipt")
	}
	return receipt, nil
}

func validateReceiptForBuild(build domain.Build, receipt buildv2.VerifiedBuildReceipt, expectedRepository string) error {
	if build.RequestContract != buildv2.APIVersion || build.BuildSpec == nil || build.BuildSpecDigest == "" {
		return domain.NewError(domain.CodePolicyRejected, "verified build receipt requires an explicit v2 build")
	}
	if build.BuildSpec.Driver != buildv2.DriverDockerfile {
		return domain.NewError(domain.CodePolicyRejected, "verified build receipt requires Dockerfile build execution")
	}
	if receipt.SourceSHA != strings.ToLower(build.Source.CommitSHA) || receipt.SpecDigest != build.BuildSpecDigest {
		return domain.NewError(domain.CodePolicyRejected, "verified build receipt does not match build source or spec")
	}
	if receipt.Repository != expectedRepository {
		return domain.NewError(domain.CodePolicyRejected, "verified build receipt repository is outside project scope")
	}
	return nil
}

func (s *Service) completeVerifiedReceiptIdempotency(tx Tx, cmd IngestVerifiedBuildReceiptCommand, requestHash string, result IngestVerifiedBuildReceiptResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return domain.Wrap(domain.CodePlatformFailure, "encode verified receipt result", err)
	}
	now := s.Clock.Now()
	return tx.PutIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: "build.verified_receipt.v2", RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: now, CompletedAt: now})
}

func receiptResult(build domain.Build, artifact *domain.Artifact) IngestVerifiedBuildReceiptResult {
	result := IngestVerifiedBuildReceiptResult{Build: build, Artifact: artifact}
	if artifact != nil && artifact.Scan != nil && artifact.Signature != nil && buildv1.ValidDigest(artifact.ProvenanceDigest) {
		ref := buildv2.ArtifactRef{
			ArtifactID: artifact.ID, Repository: artifact.Repository, Digest: artifact.Digest,
			SBOMDigest: artifact.SBOMDigest, ScanDigest: artifact.Scan.FindingsDigest,
			SignatureDigest: artifact.Signature.AttachmentDigest, ProvenanceDigest: artifact.ProvenanceDigest,
		}
		if ref.Validate() == nil {
			result.TrustChain = &ref
		}
	}
	return result
}

func publishedArtifact(artifact domain.Artifact) PublishedArtifact {
	return PublishedArtifact{Repository: artifact.Repository, Digest: artifact.Digest, MediaType: artifact.MediaType}
}

func (s *Service) failReceipt(ctx context.Context, actorID string, build domain.Build, artifact *domain.Artifact, cause error) (IngestVerifiedBuildReceiptResult, error) {
	failed, failedArtifact, err := s.fail(ctx, actorID, build, artifact, cause)
	return IngestVerifiedBuildReceiptResult{Build: failed, Artifact: failedArtifact}, err
}
