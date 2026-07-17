// Package harbor implements the narrow OCI Distribution subset required by
// the verified build-receipt path. Harbor exposes this API, including OCI 1.1
// referrers, so it is deliberately kept free of Harbor-specific admin APIs
// and of any platform master credential.
package harbor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

const (
	ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ociEmptyMediaType    = "application/vnd.oci.empty.v1+json"
	maximumAttachment    = 16 << 20
)

// Registry authenticates with one project-scoped Harbor robot account. The
// caller is expected to obtain its password as a short-lived lease; neither
// this adapter nor its errors serialise it.
type Registry struct {
	BaseURL    *url.URL
	Username   string
	Password   string
	HTTPClient *http.Client
}

func New(baseURL, username, password string) (*Registry, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Harbor registry URL must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, errors.New("Harbor robot credentials are required")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Registry{BaseURL: parsed, Username: strings.TrimSpace(username), Password: password, HTTPClient: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Publish intentionally does not copy an OCI layout from the control plane.
// Rootless BuildKit must publish directly from its disposable workspace; the
// receipt then proves the exact immutable object now present in Harbor.
func (r *Registry) Publish(ctx context.Context, tenant, repository string, output application.BuildOutput) (application.PublishedArtifact, error) {
	if strings.TrimSpace(output.OCILayoutPath) != "" {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeUnavailable, "Harbor publish must be performed by the disposable workspace")
	}
	if !buildv1.ValidDigest(output.ManifestDigest) {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeInvalidArgument, "Harbor publish requires an immutable manifest digest")
	}
	artifact, err := r.Resolve(ctx, tenant, strings.TrimSpace(repository)+"@"+strings.TrimSpace(output.ManifestDigest))
	if err != nil {
		return application.PublishedArtifact{}, err
	}
	if output.MediaType != "" && strings.TrimSpace(output.MediaType) != artifact.MediaType {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "Harbor media type differs from build output")
	}
	return artifact, nil
}

func (r *Registry) Resolve(ctx context.Context, tenant, reference string) (application.PublishedArtifact, error) {
	repository, digest, err := r.reference(tenant, reference)
	if err != nil {
		return application.PublishedArtifact{}, err
	}
	endpoint := r.endpoint("v2", repository, "manifests", digest)
	request, err := r.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return application.PublishedArtifact{}, err
	}
	request.Header.Set("Accept", strings.Join([]string{
		ociManifestMediaType,
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))
	response, err := r.client().Do(request)
	if err != nil {
		return application.PublishedArtifact{}, external("resolve immutable Harbor artifact", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNotFound {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeNotFound, "Harbor artifact not found")
	}
	if response.StatusCode != http.StatusOK {
		return application.PublishedArtifact{}, registryStatus("resolve immutable Harbor artifact", response.StatusCode)
	}
	actual := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if !buildv1.ValidDigest(actual) || actual != digest {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "Harbor immutable digest verification failed")
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" || len(mediaType) > 256 {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "Harbor manifest media type is missing")
	}
	return application.PublishedArtifact{Repository: r.repositoryName(repository), Digest: actual, MediaType: mediaType}, nil
}

func (r *Registry) StoreAttachment(ctx context.Context, tenant string, subject application.PublishedArtifact, mediaType string, raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > maximumAttachment || strings.TrimSpace(mediaType) == "" || len(strings.TrimSpace(mediaType)) > 256 {
		return "", domain.NewError(domain.CodeInvalidArgument, "Harbor attachment is invalid")
	}
	repository, err := r.repository(tenant, subject.Repository)
	if err != nil {
		return "", err
	}
	if !buildv1.ValidDigest(subject.Digest) || strings.TrimSpace(subject.MediaType) == "" {
		return "", domain.NewError(domain.CodeInvalidArgument, "Harbor attachment subject is invalid")
	}
	// Resolve the subject first. Besides preventing a dangling referrer, this
	// detects a host/path mismatch before we upload any tenant data.
	resolved, err := r.Resolve(ctx, tenant, subject.Repository+"@"+subject.Digest)
	if err != nil {
		return "", err
	}
	if resolved != subject {
		return "", domain.NewError(domain.CodeConflict, "Harbor attachment subject changed")
	}

	empty := []byte("{}")
	emptyDigest := digest(empty)
	if err := r.uploadBlob(ctx, repository, emptyDigest, empty); err != nil {
		return "", err
	}
	attachmentDigest := digest(raw)
	if err := r.uploadBlob(ctx, repository, attachmentDigest, raw); err != nil {
		return "", err
	}
	manifest := referrerManifest{
		SchemaVersion: 2, MediaType: ociManifestMediaType, ArtifactType: strings.TrimSpace(mediaType),
		Subject:     descriptor{MediaType: subject.MediaType, Digest: subject.Digest, Size: 0},
		Config:      descriptor{MediaType: ociEmptyMediaType, Digest: emptyDigest, Size: int64(len(empty))},
		Layers:      []descriptor{{MediaType: strings.TrimSpace(mediaType), Digest: attachmentDigest, Size: int64(len(raw))}},
		Annotations: map[string]string{"org.opencontainers.artifact.description": "AI-native PaaS verified build attachment"},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", domain.Wrap(domain.CodePlatformFailure, "encode Harbor OCI referrer", err)
	}
	tag := "paas-att-" + strings.TrimPrefix(subject.Digest, "sha256:")[:16] + "-" + strings.TrimPrefix(attachmentDigest, "sha256:")[:16]
	endpoint := r.endpoint("v2", repository, "manifests", tag)
	request, err := r.newRequest(ctx, http.MethodPut, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", ociManifestMediaType)
	response, err := r.client().Do(request)
	if err != nil {
		return "", external("store Harbor OCI referrer", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusCreated {
		return "", registryStatus("store Harbor OCI referrer", response.StatusCode)
	}
	if manifestDigest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest")); !buildv1.ValidDigest(manifestDigest) {
		return "", domain.NewError(domain.CodeConflict, "Harbor referrer response has no immutable digest")
	}
	return attachmentDigest, nil
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type referrerManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Subject       descriptor        `json:"subject"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

func (r *Registry) uploadBlob(ctx context.Context, repository, expectedDigest string, raw []byte) error {
	start, err := r.newRequest(ctx, http.MethodPost, r.endpoint("v2", repository, "blobs", "uploads"), nil)
	if err != nil {
		return err
	}
	response, err := r.client().Do(start)
	if err != nil {
		return external("begin Harbor blob upload", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return registryStatus("begin Harbor blob upload", response.StatusCode)
	}
	location, err := r.uploadLocation(response.Header.Get("Location"))
	if err != nil {
		return err
	}
	query := location.Query()
	query.Set("digest", expectedDigest)
	location.RawQuery = query.Encode()
	request, err := r.newRequest(ctx, http.MethodPut, location.String(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = r.client().Do(request)
	if err != nil {
		return external("complete Harbor blob upload", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusCreated {
		return registryStatus("complete Harbor blob upload", response.StatusCode)
	}
	if actual := strings.TrimSpace(response.Header.Get("Docker-Content-Digest")); actual != expectedDigest {
		return domain.NewError(domain.CodeConflict, "Harbor blob digest verification failed")
	}
	return nil
}

func (r *Registry) reference(tenant, reference string) (string, string, error) {
	at := strings.LastIndex(strings.TrimSpace(reference), "@")
	if at <= 0 {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "Harbor reference must use an immutable digest")
	}
	repository, err := r.repository(tenant, reference[:at])
	if err != nil {
		return "", "", err
	}
	digest := strings.TrimSpace(reference[at+1:])
	if !buildv1.ValidDigest(digest) {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "Harbor reference digest is invalid")
	}
	return repository, digest, nil
}

func (r *Registry) repository(tenant, repository string) (string, error) {
	if r == nil || r.BaseURL == nil || strings.TrimSpace(tenant) == "" || strings.ContainsAny(tenant, "/\\\x00") {
		return "", domain.NewError(domain.CodeUnavailable, "Harbor registry is not configured")
	}
	parts := strings.Split(strings.Trim(strings.TrimSpace(repository), "/"), "/")
	if len(parts) < 4 || parts[0] != r.BaseURL.Host || parts[1] != "tenants" || parts[2] != tenant || parts[3] != "apps" {
		return "", domain.NewError(domain.CodeConflict, "Harbor repository is outside tenant scope")
	}
	for _, segment := range parts[1:] {
		if !safeRepositorySegment(segment) {
			return "", domain.NewError(domain.CodeInvalidArgument, "Harbor repository path is invalid")
		}
	}
	return strings.Join(parts[1:], "/"), nil
}

func safeRepositorySegment(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (r *Registry) repositoryName(path string) string { return r.BaseURL.Host + "/" + path }

func (r *Registry) endpoint(elements ...string) string {
	copyURL := *r.BaseURL
	segments := []string{strings.Trim(copyURL.Path, "/")}
	for _, element := range elements {
		if element = strings.Trim(element, "/"); element != "" {
			segments = append(segments, element)
		}
	}
	copyURL.Path = "/" + path.Join(segments...)
	return copyURL.String()
}

func (r *Registry) uploadLocation(raw string) (*url.URL, error) {
	location, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || location == nil || location.Fragment != "" || location.User != nil {
		return nil, domain.NewError(domain.CodeConflict, "Harbor upload location is invalid")
	}
	if !location.IsAbs() {
		location = r.BaseURL.ResolveReference(location)
	}
	if location.Scheme != r.BaseURL.Scheme || location.Host != r.BaseURL.Host {
		return nil, domain.NewError(domain.CodeConflict, "Harbor upload location escapes registry")
	}
	return location, nil
}

func (r *Registry) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, domain.Wrap(domain.CodeInvalidArgument, "construct Harbor request", err)
	}
	request.SetBasicAuth(r.Username, r.Password)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func external(action string, cause error) error {
	return domain.Wrap(domain.CodeUnavailable, action+" failed", cause)
}

func registryStatus(action string, status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return domain.NewError(domain.CodeUnavailable, "Harbor robot credential is unavailable")
	}
	if status >= 500 {
		return domain.Retryable(domain.CodeUnavailable, action+" is temporarily unavailable", fmt.Errorf("Harbor status %d", status))
	}
	return domain.NewError(domain.CodeConflict, action+" was rejected")
}

var _ application.Registry = (*Registry)(nil)
