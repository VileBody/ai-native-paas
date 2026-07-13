package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type Local struct {
	Root        string
	mu          sync.Mutex
	attachments map[string]map[string]string
}

func NewLocal(root string) *Local {
	return &Local{Root: root, attachments: map[string]map[string]string{}}
}
func (l *Local) Publish(ctx context.Context, tenant, repository string, output application.BuildOutput) (application.PublishedArtifact, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return application.PublishedArtifact{}, err
	}
	if !owned(tenant, repository) {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "registry repository is outside tenant scope")
	}
	if output.OCILayoutPath == "" {
		if !buildv1.ValidDigest(output.ManifestDigest) {
			return application.PublishedArtifact{}, domain.NewError(domain.CodeInvalidArgument, "valid OCI layout or immutable digest required")
		}
		return application.PublishedArtifact{Repository: repository, Digest: output.ManifestDigest, MediaType: coalesce(output.MediaType, localoci.ManifestMediaType)}, nil
	}
	digest, err := localoci.ManifestDigest(output.OCILayoutPath)
	if err != nil {
		return application.PublishedArtifact{}, domain.Wrap(domain.CodePlatformFailure, "invalid OCI layout", err)
	}
	if output.ManifestDigest != "" && output.ManifestDigest != digest {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "builder manifest digest mismatch")
	}
	destination := l.artifactPath(repository, digest)
	if err := copyTree(output.OCILayoutPath, destination); err != nil {
		return application.PublishedArtifact{}, domain.Wrap(domain.CodePlatformFailure, "store OCI layout", err)
	}
	meta, _ := json.Marshal(application.PublishedArtifact{Repository: repository, Digest: digest, MediaType: coalesce(output.MediaType, localoci.ManifestMediaType)})
	if err := os.WriteFile(filepath.Join(destination, "paas-artifact.json"), meta, 0644); err != nil {
		return application.PublishedArtifact{}, err
	}
	return application.PublishedArtifact{Repository: repository, Digest: digest, MediaType: coalesce(output.MediaType, localoci.ManifestMediaType)}, nil
}
func (l *Local) Resolve(ctx context.Context, tenant, reference string) (application.PublishedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return application.PublishedArtifact{}, err
	}
	at := strings.LastIndex(reference, "@sha256:")
	if at < 0 {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeInvalidArgument, "immutable repository@digest reference required")
	}
	repository, digest := reference[:at], reference[at+1:]
	if !owned(tenant, repository) {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeNotFound, "artifact not found")
	}
	path := l.artifactPath(repository, digest)
	raw, err := os.ReadFile(filepath.Join(path, "paas-artifact.json"))
	if err != nil {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeNotFound, "artifact not found")
	}
	var value application.PublishedArtifact
	if err := json.Unmarshal(raw, &value); err != nil {
		return application.PublishedArtifact{}, err
	}
	actual, err := localoci.ManifestDigest(path)
	if err != nil || actual != value.Digest {
		return application.PublishedArtifact{}, domain.NewError(domain.CodeConflict, "stored OCI artifact failed digest verification")
	}
	return value, nil
}
func (l *Local) StoreAttachment(ctx context.Context, tenant, repository, mediaType string, raw []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !owned(tenant, repository) {
		return "", domain.NewError(domain.CodeConflict, "attachment repository outside tenant scope")
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	dir := filepath.Join(l.Root, "attachments", repositoryKey(repository))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, strings.TrimPrefix(digest, "sha256:")), raw, 0644); err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attachments[repository] == nil {
		l.attachments[repository] = map[string]string{}
	}
	l.attachments[repository][digest] = mediaType
	return digest, nil
}
func (l *Local) AttachmentMediaType(repository, digest string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.attachments[repository][digest]
	return v, ok
}
func (l *Local) artifactPath(repository, digest string) string {
	return filepath.Join(l.Root, "repositories", repositoryKey(repository), strings.TrimPrefix(digest, "sha256:"))
}
func repositoryKey(repository string) string {
	sum := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(sum[:])
}
func owned(tenant, repository string) bool {
	if tenant == "" || strings.ContainsAny(tenant, "/\\\x00") {
		return false
	}
	parts := strings.Split(strings.Trim(repository, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "tenants" && parts[i+1] == tenant && parts[i+2] == "apps" {
			return true
		}
	}
	return false
}
func copyTree(source, destination string) error {
	_ = os.RemoveAll(destination)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("registry rejects non-regular OCI file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
}
func coalesce(v, f string) string {
	if v == "" {
		return f
	}
	return v
}

var _ application.Registry = (*Local)(nil)
