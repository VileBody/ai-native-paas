package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type Service struct {
	Store          Store
	Fetcher        SourceFetcher
	Detector       Detector
	Buildpacks     Builder
	Dockerfile     Builder
	Registry       Registry
	SBOM           SBOMGenerator
	Scanner        Scanner
	Signer         Signer
	Verifier       SignatureVerifier
	SecretProvider BuildSecretProvider
	Logs           LogStore
	Clock          Clock
	IDs            IDGenerator
	SourceLimits   SourceLimits
	RepositoryBase string
}

type RequestBuildCommand struct {
	TenantID, ActorID, CorrelationID, IdempotencyKey string
	Source                                           sourcev1.SourceRevision
	Config                                           domain.BuildConfig
	BuilderDigest, RunImageDigest, PlatformVersion   string
}
type RequestBuildResult struct {
	Build    domain.Build
	Artifact *domain.Artifact
	Reused   bool
}

func (s *Service) RequestBuild(ctx context.Context, cmd RequestBuildCommand) (RequestBuildResult, error) {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return RequestBuildResult{}, domain.NewError(domain.CodeUnavailable, "build service is not configured")
	}
	if strings.TrimSpace(cmd.TenantID) == "" || strings.TrimSpace(cmd.ActorID) == "" || strings.TrimSpace(cmd.CorrelationID) == "" || strings.TrimSpace(cmd.IdempotencyKey) == "" {
		return RequestBuildResult{}, domain.NewError(domain.CodeInvalidArgument, "missing request-build fields")
	}
	identity, err := domain.ComputeBuildIdentity(cmd.Source, cmd.Config, cmd.BuilderDigest, cmd.RunImageDigest, cmd.PlatformVersion)
	if err != nil {
		return RequestBuildResult{}, err
	}
	requestHash := hashJSON(struct {
		Identity string `json:"identity"`
	}{identity})
	var result RequestBuildResult
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != "build.request.v1" || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if !record.Completed {
				return domain.NewError(domain.CodeConflict, "build request is in progress")
			}
			return json.Unmarshal(record.Result, &result)
		}
		if existing, ok := tx.FindBuildByIdentity(cmd.TenantID, identity); ok {
			result.Build = existing
			if artifact, found := tx.FindArtifactByBuild(existing.ID); found {
				copyArtifact := artifact
				result.Artifact = &copyArtifact
			}
			result.Reused = existing.State == buildv1.BuildSucceeded && result.Artifact != nil && result.Artifact.State == domain.ArtifactReleasable
			return s.completeIdempotency(tx, cmd, requestHash, result)
		}
		now := s.Clock.Now()
		build, err := domain.NewBuild(s.IDs.NewID("bld"), cmd.TenantID, identity, cmd.CorrelationID, cmd.Source, cmd.Config, cmd.BuilderDigest, cmd.RunImageDigest, cmd.PlatformVersion, now)
		if err != nil {
			return err
		}
		if err := tx.InsertBuild(build); err != nil {
			return err
		}
		result.Build = build
		if err := s.completeIdempotency(tx, cmd, requestHash, result); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"build_id": build.ID, "tenant_id": build.TenantID, "identity": build.Identity, "commit_sha": build.Source.CommitSHA})
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.requested.v1", AggregateID: build.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "build.request", ResourceType: "build", ResourceID: build.ID, Data: []byte(`{"source":"exact-revision"}`), CreatedAt: now})
	})
	return result, err
}

type RetryBuildCommand struct {
	TenantID, ActorID, CorrelationID, IdempotencyKey string
	BuildID                                          string
}

// RetryBuild creates a new immutable attempt for a failed terminal build while
// retaining the original correlation chain. A successful or running build is
// never duplicated by this command.
func (s *Service) RetryBuild(ctx context.Context, cmd RetryBuildCommand) (RequestBuildResult, error) {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return RequestBuildResult{}, domain.NewError(domain.CodeUnavailable, "build service is not configured")
	}
	if strings.TrimSpace(cmd.TenantID) == "" || strings.TrimSpace(cmd.ActorID) == "" || strings.TrimSpace(cmd.CorrelationID) == "" || strings.TrimSpace(cmd.IdempotencyKey) == "" || strings.TrimSpace(cmd.BuildID) == "" {
		return RequestBuildResult{}, domain.NewError(domain.CodeInvalidArgument, "missing retry-build fields")
	}
	requestHash := hashJSON(struct {
		BuildID string `json:"build_id"`
	}{BuildID: cmd.BuildID})
	var result RequestBuildResult
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != "build.retry.v1" || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if !record.Completed {
				return domain.NewError(domain.CodeConflict, "build retry is in progress")
			}
			return json.Unmarshal(record.Result, &result)
		}
		original, ok := tx.GetBuild(cmd.BuildID)
		if !ok || original.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "build not found")
		}
		if latest, found := tx.FindBuildByIdentity(cmd.TenantID, original.Identity); found && latest.Attempt > original.Attempt {
			return domain.NewError(domain.CodeConflict, "a newer retry attempt already exists")
		}
		retry, err := original.Retry(s.IDs.NewID("bld"), cmd.CorrelationID, s.Clock.Now())
		if err != nil {
			return err
		}
		if err := tx.InsertBuild(retry); err != nil {
			return err
		}
		result.Build = retry
		raw, err := json.Marshal(result)
		if err != nil {
			return domain.Wrap(domain.CodePlatformFailure, "encode retry result", err)
		}
		now := s.Clock.Now()
		if err := tx.PutIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: "build.retry.v1", RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: now, CompletedAt: now}); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"build_id": retry.ID, "retry_of": original.ID, "attempt": retry.Attempt, "original_correlation_id": retry.OriginalCorrelationID})
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.requested.v1", AggregateID: retry.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "build.retry", ResourceType: "build", ResourceID: retry.ID, Data: []byte(`{"retry":true}`), CreatedAt: now})
	})
	return result, err
}

func (s *Service) GetBuild(ctx context.Context, tenantID, buildID string) (domain.Build, *domain.Artifact, error) {
	return s.load(ctx, tenantID, buildID)
}

func (s *Service) StreamBuildLogs(ctx context.Context, tenantID, buildID string) ([]byte, error) {
	if s.Logs == nil {
		return nil, domain.NewError(domain.CodeUnavailable, "build log store is unavailable")
	}
	if _, _, err := s.load(ctx, tenantID, buildID); err != nil {
		return nil, err
	}
	return s.Logs.Read(ctx, buildID)
}

func (s *Service) EvaluateArtifact(ctx context.Context, tenantID, artifactID string) (buildv1.ReleasabilityDecision, error) {
	var artifact domain.Artifact
	err := s.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetArtifact(artifactID)
		if !ok || value.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "artifact not found")
		}
		artifact = value
		return nil
	})
	if err != nil {
		return buildv1.ReleasabilityDecision{}, err
	}
	decision := buildv1.ReleasabilityDecision{Allowed: artifact.State == domain.ArtifactReleasable, PolicyVersion: "artifact-trust-v1"}
	if !decision.Allowed {
		decision.Reasons = []string{"artifact state is " + string(artifact.State)}
		if len(artifact.RejectionNotes) > 0 {
			decision.Reasons = append(decision.Reasons, artifact.RejectionNotes...)
		}
	}
	return decision, nil
}

func (s *Service) completeIdempotency(tx Tx, cmd RequestBuildCommand, requestHash string, result RequestBuildResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return domain.Wrap(domain.CodePlatformFailure, "encode build result", err)
	}
	now := s.Clock.Now()
	return tx.PutIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: "build.request.v1", RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: now, CompletedAt: now})
}

func (s *Service) RunBuild(ctx context.Context, tenantID, actorID, buildID string) (domain.Build, *domain.Artifact, error) {
	build, artifact, err := s.load(ctx, tenantID, buildID)
	if err != nil {
		return build, artifact, err
	}
	if build.Terminal() {
		return build, artifact, nil
	}
	if build.State != buildv1.BuildQueued {
		return build, artifact, domain.NewError(domain.CodeConflict, "automatic resume of partial build is not implemented")
	}
	if s.Fetcher == nil || s.Detector == nil || s.Registry == nil || s.SBOM == nil || s.Scanner == nil || s.Signer == nil || s.Verifier == nil || s.Logs == nil {
		return build, artifact, domain.NewError(domain.CodeUnavailable, "build pipeline is not configured")
	}
	if err := s.startBuild(ctx, actorID, &build); err != nil {
		return build, artifact, err
	}
	snapshot, err := s.Fetcher.Fetch(ctx, build.TenantID, build.Source, s.SourceLimits)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if snapshot.Cleanup != nil {
		defer snapshot.Cleanup()
	}
	if err := s.advance(ctx, &build, buildv1.BuildDetecting); err != nil {
		return build, artifact, err
	}
	detection, err := s.Detector.Detect(ctx, snapshot.Path, build.Config)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := s.selectExecution(ctx, &build, detection); err != nil {
		return build, artifact, err
	}
	secrets, err := s.resolveSecrets(ctx, build)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	secretValues := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		secretValues = append(secretValues, secret.Value)
	}
	writer := s.Logs.Writer(build.ID, secretValues)
	builder := s.Buildpacks
	if detection.Backend == BackendDockerfile {
		builder = s.Dockerfile
	}
	if builder == nil {
		return s.fail(ctx, actorID, build, artifact, domain.NewError(domain.CodeUnavailable, "selected build backend is unavailable"))
	}
	repository := s.repository(build)
	output, err := builder.Build(ctx, BuildExecutionRequest{BuildID: build.ID, TenantID: build.TenantID, BuilderDigest: build.BuilderDigest, RunImageDigest: build.RunImageDigest, Source: snapshot, Detection: detection, Config: build.Config, Environment: cloneMap(build.Config.BuildEnv), Secrets: secretsForPhase(secrets, PhaseBuild), LogWriter: writer, Repository: repository})
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := s.advance(ctx, &build, buildv1.BuildExporting); err != nil {
		return build, artifact, err
	}
	published, err := s.Registry.Publish(ctx, build.TenantID, repository, output)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	created, err := domain.NewArtifact(s.IDs.NewID("art"), build.TenantID, build.ID, published.Repository, published.Digest, published.MediaType, s.Clock.Now())
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := created.Quarantine(s.Clock.Now()); err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := s.insertArtifact(ctx, created); err != nil {
		return build, artifact, err
	}
	artifact = &created
	sbom, err := s.SBOM.Generate(ctx, snapshot, published)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	storedSBOM, err := s.Registry.StoreAttachment(ctx, build.TenantID, published.Repository, sbom.MediaType, sbom.Document)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if storedSBOM != sbom.Digest {
		return s.fail(ctx, actorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "registry changed SBOM digest"))
	}
	if err := s.updateArtifact(ctx, artifact, func(a *domain.Artifact) error { return a.AttachSBOM(sbom.Digest, sbom.MediaType, s.Clock.Now()) }); err != nil {
		return build, artifact, err
	}
	if err := s.advance(ctx, &build, buildv1.BuildScanning); err != nil {
		return build, artifact, err
	}
	scan, err := s.Scanner.Scan(ctx, artifact.Ref(), sbom)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := s.updateArtifact(ctx, artifact, func(a *domain.Artifact) error { return a.ApplyScan(scan, s.Clock.Now()) }); err != nil {
		return build, artifact, err
	}
	if artifact.State == domain.ArtifactRejected {
		return s.fail(ctx, actorID, build, artifact, domain.NewError(domain.CodePolicyRejected, strings.Join(artifact.RejectionNotes, "; ")))
	}
	if err := s.advance(ctx, &build, buildv1.BuildSigning); err != nil {
		return build, artifact, err
	}
	signature, err := s.Signer.Sign(ctx, artifact.Repository, artifact.Digest)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := s.Verifier.Verify(ctx, artifact.Repository, signature); err != nil {
		return s.fail(ctx, actorID, build, artifact, domain.Wrap(domain.CodePlatformFailure, "platform signature failed verification", err))
	}
	signatureDocument, err := json.Marshal(signature)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, domain.Wrap(domain.CodePlatformFailure, "encode signature attachment", err))
	}
	signatureAttachment, err := s.Registry.StoreAttachment(ctx, build.TenantID, artifact.Repository, "application/vnd.dev.cosign.simplesigning.v1+json", signatureDocument)
	if err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if !buildv1.ValidDigest(signatureAttachment) {
		return s.fail(ctx, actorID, build, artifact, domain.NewError(domain.CodePlatformFailure, "registry returned invalid signature attachment digest"))
	}
	signature.AttachmentDigest = signatureAttachment
	artifactExpected := artifact.Version
	if err := artifact.AttachSignature(signature, s.Clock.Now()); err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	if err := artifact.MarkReleasable(s.Clock.Now()); err != nil {
		return s.fail(ctx, actorID, build, artifact, err)
	}
	buildExpected := build.Version
	if err := build.Succeed(artifact.ID, s.Clock.Now()); err != nil {
		return build, artifact, err
	}
	payload, _ := json.Marshal(map[string]any{"build_id": build.ID, "artifact_id": artifact.ID, "repository": artifact.Repository, "digest": artifact.Digest})
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if err := tx.UpdateArtifact(*artifact, artifactExpected); err != nil {
			return err
		}
		if err := tx.UpdateBuild(build, buildExpected); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.completed.v1", AggregateID: build.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "artifact.releasable.v1", AggregateID: artifact.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: build.TenantID, ActorID: actorID, Action: "artifact.releasable", ResourceType: "artifact", ResourceID: artifact.ID, Data: []byte(`{"digest_bound":true}`), CreatedAt: now})
	})
	if err != nil {
		return build, artifact, err
	}
	return build, artifact, nil
}

func (s *Service) CancelBuild(ctx context.Context, tenantID, buildID string) (domain.Build, error) {
	build, _, err := s.load(ctx, tenantID, buildID)
	if err != nil {
		return build, err
	}
	if !build.Terminal() {
		if build.State != buildv1.BuildQueued && build.Backend != "" {
			builder := s.builderForBackend(build.Backend)
			if builder == nil {
				return build, domain.NewError(domain.CodeUnavailable, "selected build backend is unavailable")
			}
			if err := builder.Cancel(ctx, build.ID); err != nil {
				return build, domain.Wrap(domain.CodePlatformFailure, "cancel build backend", err)
			}
		}
		if err := s.updateBuild(ctx, &build, func(b *domain.Build) error { return b.Cancel(s.Clock.Now()) }); err != nil {
			return build, err
		}
	}
	return build, nil
}

func (s *Service) load(ctx context.Context, tenantID, buildID string) (domain.Build, *domain.Artifact, error) {
	var build domain.Build
	var artifact *domain.Artifact
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		build, ok = tx.GetBuild(buildID)
		if !ok || build.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "build not found")
		}
		if value, found := tx.FindArtifactByBuild(build.ID); found {
			copyValue := value
			artifact = &copyValue
		}
		return nil
	})
	return build, artifact, err
}
func (s *Service) startBuild(ctx context.Context, actorID string, build *domain.Build) error {
	expected := build.Version
	if err := build.Transition(buildv1.BuildFetchingSource, s.Clock.Now()); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"build_id": build.ID, "commit_sha": build.Source.CommitSHA, "attempt": build.Attempt})
	return s.Store.Transact(ctx, func(tx Tx) error {
		now := s.Clock.Now()
		if err := tx.UpdateBuild(*build, expected); err != nil {
			return err
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.started.v1", AggregateID: build.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: build.TenantID, ActorID: actorID, Action: "build.start", ResourceType: "build", ResourceID: build.ID, Data: []byte(`{"exact_revision":true}`), CreatedAt: now})
	})
}

func (s *Service) selectExecution(ctx context.Context, build *domain.Build, detection Detection) error {
	return s.updateBuild(ctx, build, func(value *domain.Build) error {
		return value.SelectExecution(detection.Runtime, domain.ExecutionBackend(detection.Backend), detection.BuildpackID, s.Clock.Now())
	})
}

func (s *Service) builderForBackend(backend domain.ExecutionBackend) Builder {
	switch backend {
	case domain.ExecutionBuildpacks:
		return s.Buildpacks
	case domain.ExecutionDockerfile:
		return s.Dockerfile
	default:
		return nil
	}
}

func (s *Service) advance(ctx context.Context, build *domain.Build, state buildv1.BuildState) error {
	return s.updateBuild(ctx, build, func(b *domain.Build) error { return b.Transition(state, s.Clock.Now()) })
}
func (s *Service) updateBuild(ctx context.Context, build *domain.Build, mutate func(*domain.Build) error) error {
	expected := build.Version
	if err := mutate(build); err != nil {
		return err
	}
	return s.Store.Transact(ctx, func(tx Tx) error { return tx.UpdateBuild(*build, expected) })
}
func (s *Service) insertArtifact(ctx context.Context, artifact domain.Artifact) error {
	return s.Store.Transact(ctx, func(tx Tx) error { return tx.InsertArtifact(artifact) })
}
func (s *Service) updateArtifact(ctx context.Context, artifact *domain.Artifact, mutate func(*domain.Artifact) error) error {
	expected := artifact.Version
	if err := mutate(artifact); err != nil {
		return err
	}
	return s.Store.Transact(ctx, func(tx Tx) error { return tx.UpdateArtifact(*artifact, expected) })
}
func (s *Service) resolveSecrets(ctx context.Context, build domain.Build) ([]BuildSecret, error) {
	if len(build.Config.BuildSecretRef) == 0 {
		return nil, nil
	}
	if s.SecretProvider == nil {
		return nil, domain.NewError(domain.CodeUnavailable, "build secret provider is unavailable")
	}
	return s.SecretProvider.ResolveBuildSecrets(ctx, build.TenantID, build.Config.BuildSecretRef)
}
func (s *Service) fail(ctx context.Context, actorID string, build domain.Build, artifact *domain.Artifact, cause error) (domain.Build, *domain.Artifact, error) {
	state, code, retryable := classifyFailure(cause)
	if build.Terminal() {
		return build, artifact, cause
	}
	expected := build.Version
	if err := build.Fail(state, code, cause.Error(), retryable, s.Clock.Now()); err != nil {
		return build, artifact, err
	}
	payload, _ := json.Marshal(map[string]any{"build_id": build.ID, "state": build.State, "failure_code": build.FailureCode, "retryable": build.Retryable})
	persistErr := s.Store.Transact(ctx, func(tx Tx) error {
		now := s.Clock.Now()
		if err := tx.UpdateBuild(build, expected); err != nil {
			return err
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "build.failed.v1", AggregateID: build.ID, Payload: payload, CreatedAt: now}); err != nil {
			return err
		}
		if artifact != nil && artifact.State == domain.ArtifactRejected {
			rejected, _ := json.Marshal(map[string]any{"artifact_id": artifact.ID, "build_id": build.ID, "reasons": artifact.RejectionNotes})
			if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "artifact.rejected.v1", AggregateID: artifact.ID, Payload: rejected, CreatedAt: now}); err != nil {
				return err
			}
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: build.TenantID, ActorID: actorID, Action: "build.failed", ResourceType: "build", ResourceID: build.ID, Data: payload, CreatedAt: now})
	})
	if persistErr != nil {
		return build, artifact, persistErr
	}
	return build, artifact, cause
}
func classifyFailure(err error) (buildv1.BuildState, string, bool) {
	var typed *domain.Error
	if errors.As(err, &typed) {
		switch typed.Code {
		case domain.CodeUserFailure, domain.CodeInvalidArgument, domain.CodePolicyRejected:
			return buildv1.BuildFailedUserCode, string(typed.Code), typed.Retryable
		case domain.CodeTimeout:
			return buildv1.BuildTimedOut, string(typed.Code), false
		default:
			return buildv1.BuildFailedPlatform, string(typed.Code), typed.Retryable
		}
	}
	return buildv1.BuildFailedPlatform, "UNCLASSIFIED_PLATFORM_FAILURE", false
}
func secretsForPhase(secrets []BuildSecret, phase BuildPhase) []BuildSecret {
	out := make([]BuildSecret, 0, len(secrets))
	for _, secret := range secrets {
		for _, allowed := range secret.AllowedPhases {
			if allowed == phase {
				copySecret := secret
				copySecret.AllowedPhases = append([]BuildPhase(nil), secret.AllowedPhases...)
				out = append(out, copySecret)
				break
			}
		}
	}
	return out
}
func (s *Service) repository(build domain.Build) string {
	base := strings.TrimSuffix(strings.TrimSpace(s.RepositoryBase), "/")
	if base == "" {
		base = "registry.local/tenants"
	}
	return fmt.Sprintf("%s/%s/apps/%s", base, build.TenantID, build.Source.ProjectID)
}
func hashJSON(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
