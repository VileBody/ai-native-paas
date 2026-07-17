package testkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type Clock struct{ T time.Time }

func (c *Clock) Now() time.Time          { return c.T }
func (c *Clock) Advance(d time.Duration) { c.T = c.T.Add(d) }

type IDs struct{ n atomic.Uint64 }

func (i *IDs) NewID(prefix string) string { return prefix + "-" + itoa(i.n.Add(1)) }
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for v > 0 {
		n--
		b[n] = byte('0' + v%10)
		v /= 10
	}
	return string(b[n:])
}

type Fetcher struct {
	Snapshot application.SourceSnapshot
	Err      error
	Calls    int
	mu       sync.Mutex
}

func (f *Fetcher) Fetch(_ context.Context, tenant string, revision sourcev1.SourceRevision, _ application.SourceLimits) (application.SourceSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Err != nil {
		return application.SourceSnapshot{}, f.Err
	}
	value := f.Snapshot
	value.Revision = revision
	return value, nil
}

type Detector struct {
	Detection application.Detection
	Err       error
	Calls     int
}

func (d *Detector) Detect(context.Context, string, domain.BuildConfig) (application.Detection, error) {
	d.Calls++
	return d.Detection, d.Err
}

type Builder struct {
	Output      application.BuildOutput
	Err         error
	CancelErr   error
	Requests    []application.BuildExecutionRequest
	CancelCalls int
	mu          sync.Mutex
}

func (b *Builder) Build(_ context.Context, r application.BuildExecutionRequest) (application.BuildOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Requests = append(b.Requests, cloneRequest(r))
	if r.LogWriter != nil {
		_, _ = io.WriteString(r.LogWriter, "builder log\n")
	}
	return b.Output, b.Err
}
func (b *Builder) Cancel(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.CancelCalls++
	return b.CancelErr
}
func (b *Builder) CancelCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.CancelCalls
}
func (b *Builder) LastRequest() (application.BuildExecutionRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Requests) == 0 {
		return application.BuildExecutionRequest{}, false
	}
	return cloneRequest(b.Requests[len(b.Requests)-1]), true
}

type IsolatedBuilder struct {
	*Builder
	Isolation application.IsolationBoundary
}

func (b *IsolatedBuilder) IsolationBoundary() application.IsolationBoundary {
	return b.Isolation
}

func cloneRequest(r application.BuildExecutionRequest) application.BuildExecutionRequest {
	r.Environment = cloneMap(r.Environment)
	r.Secrets = append([]application.BuildSecret(nil), r.Secrets...)
	if r.BuildSpec != nil {
		spec := *r.BuildSpec
		spec.Platforms = append([]string(nil), r.BuildSpec.Platforms...)
		spec.SecretRefs = append([]string(nil), r.BuildSpec.SecretRefs...)
		spec.BuildArguments = cloneMap(r.BuildSpec.BuildArguments)
		r.BuildSpec = &spec
	}
	return r
}

type Registry struct {
	Published                             application.PublishedArtifact
	PublishErr, ResolveErr, AttachmentErr error
	PublishCalls, ResolveCalls            int
	Attachments                           map[string][]byte
	mu                                    sync.Mutex
}

func (r *Registry) Publish(context.Context, string, string, application.BuildOutput) (application.PublishedArtifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.PublishCalls++
	return r.Published, r.PublishErr
}
func (r *Registry) Resolve(context.Context, string, string) (application.PublishedArtifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResolveCalls++
	return r.Published, r.ResolveErr
}
func (r *Registry) StoreAttachment(_ context.Context, _ string, subject application.PublishedArtifact, mediaType string, raw []byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.AttachmentErr != nil {
		return "", r.AttachmentErr
	}
	if subject.Repository == "" || !buildv1.ValidDigest(subject.Digest) || subject.MediaType == "" {
		return "", domain.NewError(domain.CodeInvalidArgument, "attachment subject is invalid")
	}
	if r.Attachments == nil {
		r.Attachments = map[string][]byte{}
	}
	r.Attachments[mediaType] = append([]byte(nil), raw...)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type SBOM struct {
	Result application.SBOMResult
	Err    error
}

func (s SBOM) Generate(context.Context, application.SourceSnapshot, application.PublishedArtifact) (application.SBOMResult, error) {
	return s.Result, s.Err
}

type Scanner struct {
	Result domain.ScanResult
	Err    error
}

func (s Scanner) Scan(context.Context, buildv1.ArtifactRef, application.SBOMResult) (domain.ScanResult, error) {
	return s.Result, s.Err
}

type Signer struct {
	Record domain.SignatureRecord
	Err    error
}

func (s Signer) Sign(context.Context, string, string) (domain.SignatureRecord, error) {
	return s.Record, s.Err
}

type Verifier struct {
	Err     error
	Records []domain.SignatureRecord
	mu      sync.Mutex
}

func (v *Verifier) Verify(_ context.Context, _ string, record domain.SignatureRecord) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Records = append(v.Records, record)
	return v.Err
}

type ProvenanceAttestor struct {
	Result    application.ProvenanceResult
	Err       error
	Materials []application.ProvenanceMaterials
	mu        sync.Mutex
}

func (a *ProvenanceAttestor) Attest(_ context.Context, materials application.ProvenanceMaterials) (application.ProvenanceResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Materials = append(a.Materials, materials)
	return cloneProvenanceResult(a.Result), a.Err
}

type ProvenanceVerifier struct {
	Result    application.ProvenanceResult
	Err       error
	Documents [][]byte
	mu        sync.Mutex
}

func (v *ProvenanceVerifier) Verify(_ context.Context, document []byte) (application.ProvenanceResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Documents = append(v.Documents, append([]byte(nil), document...))
	return cloneProvenanceResult(v.Result), v.Err
}

type SecretProvider struct {
	BuildSecrets  []application.BuildSecret
	RuntimeSecret string
	Err           error
}

func (s SecretProvider) ResolveBuildSecrets(context.Context, string, []string) ([]application.BuildSecret, error) {
	return append([]application.BuildSecret(nil), s.BuildSecrets...), s.Err
}
func cloneMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneProvenanceResult(in application.ProvenanceResult) application.ProvenanceResult {
	return application.ProvenanceResult{Digest: in.Digest, MediaType: in.MediaType, Document: append([]byte(nil), in.Document...)}
}
