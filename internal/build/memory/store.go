package memory

import (
	"context"
	"reflect"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type state struct {
	builds      map[string]domain.Build
	artifacts   map[string]domain.Artifact
	idempotency map[string]application.IdempotencyRecord
	outbox      []application.OutboxRecord
	audit       []application.AuditRecord
}
type Store struct {
	mu sync.Mutex
	s  state
}

func New() *Store { return &Store{s: newState()} }
func newState() state {
	return state{builds: map[string]domain.Build{}, artifacts: map[string]domain.Artifact{}, idempotency: map[string]application.IdempotencyRecord{}}
}
func clone(in state) state {
	out := newState()
	for key, value := range in.builds {
		out.builds[key] = cloneBuild(value)
	}
	for key, value := range in.artifacts {
		out.artifacts[key] = cloneArtifact(value)
	}
	for key, value := range in.idempotency {
		value.Result = append([]byte(nil), value.Result...)
		out.idempotency[key] = value
	}
	for _, value := range in.outbox {
		value.Payload = append([]byte(nil), value.Payload...)
		out.outbox = append(out.outbox, value)
	}
	for _, value := range in.audit {
		value.Data = append([]byte(nil), value.Data...)
		out.audit = append(out.audit, value)
	}
	return out
}
func cloneBuild(value domain.Build) domain.Build {
	value.Config.BuildCommand = append([]string(nil), value.Config.BuildCommand...)
	value.Config.BuildSecretRef = append([]string(nil), value.Config.BuildSecretRef...)
	if value.Config.BuildEnv != nil {
		env := make(map[string]string, len(value.Config.BuildEnv))
		for key, item := range value.Config.BuildEnv {
			env[key] = item
		}
		value.Config.BuildEnv = env
	}
	return value
}
func cloneArtifact(value domain.Artifact) domain.Artifact {
	value.RejectionNotes = append([]string(nil), value.RejectionNotes...)
	if value.Scan != nil {
		scan := *value.Scan
		scan.Reasons = append([]string(nil), scan.Reasons...)
		value.Scan = &scan
	}
	if value.Signature != nil {
		sig := *value.Signature
		value.Signature = &sig
	}
	return value
}
func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.s)
	if err := fn((*tx)(&next)); err != nil {
		return err
	}
	s.s = next
	return nil
}

type tx state

func (t *tx) GetBuild(id string) (domain.Build, bool) {
	v, ok := t.builds[id]
	return cloneBuild(v), ok
}
func (t *tx) FindBuildByIdentity(tenant, identity string) (domain.Build, bool) {
	var latest domain.Build
	found := false
	for _, v := range t.builds {
		if v.TenantID != tenant || v.Identity != identity {
			continue
		}
		if !found || v.Attempt > latest.Attempt || (v.Attempt == latest.Attempt && v.CreatedAt.After(latest.CreatedAt)) {
			latest = v
			found = true
		}
	}
	return cloneBuild(latest), found
}
func (t *tx) InsertBuild(v domain.Build) error {
	if _, ok := t.builds[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "build exists")
	}
	latest, found := t.FindBuildByIdentity(v.TenantID, v.Identity)
	if found {
		if v.Attempt != latest.Attempt+1 {
			return domain.NewError(domain.CodeConflict, "build attempt already exists")
		}
		if !latest.Terminal() || latest.State == buildv1.BuildSucceeded {
			return domain.NewError(domain.CodeConflict, "build identity has an active or successful attempt")
		}
	} else if v.Attempt != 1 {
		return domain.NewError(domain.CodeConflict, "first build attempt must be one")
	}
	t.builds[v.ID] = cloneBuild(v)
	return nil
}
func (t *tx) UpdateBuild(v domain.Build, expected int64) error {
	old, ok := t.builds[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "build not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "build version mismatch")
	}
	if v.Version != old.Version+1 {
		return domain.NewError(domain.CodeConflict, "build version must increment exactly once")
	}
	if old.TenantID != v.TenantID || old.Identity != v.Identity || old.Attempt != v.Attempt ||
		!reflect.DeepEqual(old.Source, v.Source) || !reflect.DeepEqual(old.Config, v.Config) ||
		old.BuilderDigest != v.BuilderDigest || old.RunImageDigest != v.RunImageDigest ||
		old.PlatformBuildVersion != v.PlatformBuildVersion || old.CreatedAt != v.CreatedAt ||
		old.CorrelationID != v.CorrelationID || old.OriginalCorrelationID != v.OriginalCorrelationID {
		return domain.NewError(domain.CodeConflict, "build identity is immutable")
	}
	if old.Backend != "" && (old.Backend != v.Backend || old.Runtime != v.Runtime || old.BuildpackID != v.BuildpackID) {
		return domain.NewError(domain.CodeConflict, "build execution selection is immutable")
	}
	if !validBuildStateChange(old.State, v.State) {
		return domain.NewError(domain.CodeConflict, "invalid persisted build transition")
	}
	if requiresExecutionSelection(v.State) {
		if v.Runtime == "" || v.Backend == "" {
			return domain.NewError(domain.CodeConflict, "running build requires persisted execution selection")
		}
		if v.Backend == domain.ExecutionBuildpacks && v.BuildpackID == "" {
			return domain.NewError(domain.CodeConflict, "buildpacks backend requires buildpack id")
		}
		if v.Backend == domain.ExecutionDockerfile && v.BuildpackID != "" {
			return domain.NewError(domain.CodeConflict, "dockerfile backend cannot have buildpack id")
		}
	}
	if v.State == buildv1.BuildSucceeded && v.ArtifactID == "" {
		return domain.NewError(domain.CodeConflict, "successful build requires artifact id")
	}
	t.builds[v.ID] = cloneBuild(v)
	return nil
}
func (t *tx) ListBuildsByProject(tenant, project string) []domain.Build {
	var out []domain.Build
	for _, v := range t.builds {
		if v.TenantID == tenant && v.Source.ProjectID == project {
			out = append(out, cloneBuild(v))
		}
	}
	return out
}
func (t *tx) GetArtifact(id string) (domain.Artifact, bool) {
	v, ok := t.artifacts[id]
	return cloneArtifact(v), ok
}
func (t *tx) FindArtifactByBuild(buildID string) (domain.Artifact, bool) {
	for _, v := range t.artifacts {
		if v.BuildID == buildID {
			return cloneArtifact(v), true
		}
	}
	return domain.Artifact{}, false
}
func (t *tx) InsertArtifact(v domain.Artifact) error {
	if _, ok := t.artifacts[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "artifact exists")
	}
	if _, ok := t.FindArtifactByBuild(v.BuildID); ok {
		return domain.NewError(domain.CodeConflict, "build artifact exists")
	}
	t.artifacts[v.ID] = cloneArtifact(v)
	return nil
}
func (t *tx) UpdateArtifact(v domain.Artifact, expected int64) error {
	old, ok := t.artifacts[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "artifact not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "artifact version mismatch")
	}
	if v.Version <= old.Version {
		return domain.NewError(domain.CodeConflict, "artifact version must advance")
	}
	if old.TenantID != v.TenantID || old.BuildID != v.BuildID || old.Digest != v.Digest || old.Repository != v.Repository || old.MediaType != v.MediaType || old.CreatedAt != v.CreatedAt {
		return domain.NewError(domain.CodeConflict, "artifact identity immutable")
	}
	if old.SBOMDigest != "" && (old.SBOMDigest != v.SBOMDigest || old.SBOMMediaType != v.SBOMMediaType) {
		return domain.NewError(domain.CodeConflict, "artifact SBOM reference is immutable")
	}
	if (old.SBOMDigest != v.SBOMDigest || old.SBOMMediaType != v.SBOMMediaType) && old.State != domain.ArtifactQuarantined {
		return domain.NewError(domain.CodeConflict, "SBOM can only attach while artifact is quarantined")
	}
	if v.SBOMDigest != "" && v.SBOMMediaType == "" {
		return domain.NewError(domain.CodeConflict, "SBOM media type is required")
	}
	if old.Scan != nil && !reflect.DeepEqual(old.Scan, v.Scan) {
		return domain.NewError(domain.CodeConflict, "scan result is immutable")
	}
	if old.Signature != nil && !reflect.DeepEqual(old.Signature, v.Signature) {
		return domain.NewError(domain.CodeConflict, "signature record is immutable")
	}
	if !validArtifactStateChange(old.State, v.State) {
		return domain.NewError(domain.CodeConflict, "invalid persisted artifact transition")
	}
	if v.State == domain.ArtifactReleasable && (v.Scan == nil || !v.Scan.Passed || v.Scan.Scanner == "" || v.Scan.PolicyVersion == "" || v.Signature == nil || v.Signature.Issuer == "" || v.Signature.Algorithm == "" || v.Signature.Signature == "" || !buildv1.ValidDigest(v.Signature.AttachmentDigest) || !buildv1.ValidDigest(v.SBOMDigest) || v.SBOMMediaType == "") {
		return domain.NewError(domain.CodeConflict, "artifact trust chain is incomplete")
	}
	t.artifacts[v.ID] = cloneArtifact(v)
	return nil
}
func requiresExecutionSelection(state buildv1.BuildState) bool {
	switch state {
	case buildv1.BuildBuilding, buildv1.BuildExporting, buildv1.BuildScanning, buildv1.BuildSigning, buildv1.BuildSucceeded:
		return true
	default:
		return false
	}
}

func validBuildStateChange(from, to buildv1.BuildState) bool {
	if from == to {
		return true
	}
	allowed := map[buildv1.BuildState]map[buildv1.BuildState]bool{
		buildv1.BuildQueued:         {buildv1.BuildFetchingSource: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true},
		buildv1.BuildFetchingSource: {buildv1.BuildDetecting: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true, buildv1.BuildTimedOut: true},
		buildv1.BuildDetecting:      {buildv1.BuildBuilding: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true, buildv1.BuildTimedOut: true},
		buildv1.BuildBuilding:       {buildv1.BuildExporting: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true, buildv1.BuildTimedOut: true},
		buildv1.BuildExporting:      {buildv1.BuildScanning: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true, buildv1.BuildTimedOut: true},
		buildv1.BuildScanning:       {buildv1.BuildSigning: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true},
		buildv1.BuildSigning:        {buildv1.BuildSucceeded: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true, buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true},
	}
	return allowed[from][to]
}

func validArtifactStateChange(from, to domain.ArtifactState) bool {
	if from == to {
		return true
	}
	switch from {
	case domain.ArtifactDiscovered:
		return to == domain.ArtifactQuarantined
	case domain.ArtifactQuarantined:
		return to == domain.ArtifactScanned || to == domain.ArtifactRejected
	case domain.ArtifactScanned:
		return to == domain.ArtifactSigned || to == domain.ArtifactReleasable
	case domain.ArtifactSigned:
		return to == domain.ArtifactReleasable
	default:
		return false
	}
}

func idemKey(tenant, key string) string { return tenant + "\x00" + key }
func (t *tx) GetIdempotency(tenant, key string) (application.IdempotencyRecord, bool) {
	v, ok := t.idempotency[idemKey(tenant, key)]
	v.Result = append([]byte(nil), v.Result...)
	return v, ok
}
func (t *tx) PutIdempotency(v application.IdempotencyRecord) error {
	k := idemKey(v.TenantID, v.Key)
	if old, ok := t.idempotency[k]; ok && (old.Command != v.Command || old.RequestHash != v.RequestHash) {
		return domain.NewError(domain.CodeConflict, "idempotency conflict")
	}
	v.Result = append([]byte(nil), v.Result...)
	t.idempotency[k] = v
	return nil
}
func (t *tx) AppendOutbox(v application.OutboxRecord) error {
	v.Payload = append([]byte(nil), v.Payload...)
	t.outbox = append(t.outbox, v)
	return nil
}
func (t *tx) AppendAudit(v application.AuditRecord) error {
	v.Data = append([]byte(nil), v.Data...)
	t.audit = append(t.audit, v)
	return nil
}
func (s *Store) Snapshot() (builds []domain.Build, artifacts []domain.Artifact, outbox []application.OutboxRecord, audit []application.AuditRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.s.builds {
		builds = append(builds, cloneBuild(v))
	}
	for _, v := range s.s.artifacts {
		artifacts = append(artifacts, cloneArtifact(v))
	}
	for _, value := range s.s.outbox {
		value.Payload = append([]byte(nil), value.Payload...)
		outbox = append(outbox, value)
	}
	for _, value := range s.s.audit {
		value.Data = append([]byte(nil), value.Data...)
		audit = append(audit, value)
	}
	return
}
