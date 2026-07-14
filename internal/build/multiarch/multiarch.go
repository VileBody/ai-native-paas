// Package multiarch assembles an immutable OCI image index only from platform
// manifests that completed the full trust policy.
package multiarch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

const (
	IndexMediaType    = "application/vnd.oci.image.index.v1+json"
	ManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
)

type PlatformResult struct {
	Platform, Digest, MediaType string
	SizeBytes                   int64
	Verified                    bool
}

type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform"`
}

type indexDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []Descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type Result struct {
	Digest            string
	MediaType         string
	Document          []byte
	Descriptors       []Descriptor
	Partial           bool
	ProductionAllowed bool
}

func Assemble(requested []string, results []PlatformResult, allowPartial bool) (Result, error) {
	platforms, err := canonicalPlatforms(requested)
	if err != nil {
		return Result{}, err
	}
	byPlatform := make(map[string]PlatformResult, len(results))
	requestedSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		requestedSet[platform] = struct{}{}
	}
	for _, result := range results {
		result.Platform = strings.ToLower(strings.TrimSpace(result.Platform))
		if _, requested := requestedSet[result.Platform]; !requested {
			return Result{}, domain.NewError(domain.CodeInvalidArgument, "unexpected platform build result")
		}
		if _, exists := byPlatform[result.Platform]; exists {
			return Result{}, domain.NewError(domain.CodeConflict, "duplicate platform build result")
		}
		byPlatform[result.Platform] = result
	}
	var descriptors []Descriptor
	partial := false
	for _, platform := range platforms {
		result, exists := byPlatform[platform]
		if !exists || !result.Verified || !buildv1.ValidDigest(result.Digest) || result.SizeBytes <= 0 || result.MediaType != ManifestMediaType {
			partial = true
			if !allowPartial {
				return Result{}, domain.NewError(domain.CodePolicyRejected, "every requested platform requires a verified immutable manifest")
			}
			continue
		}
		parts := strings.Split(platform, "/")
		descriptor := Descriptor{MediaType: result.MediaType, Digest: result.Digest, Size: result.SizeBytes}
		descriptor.Platform.OS, descriptor.Platform.Architecture = parts[0], parts[1]
		descriptors = append(descriptors, descriptor)
	}
	if len(descriptors) == 0 {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "multi-arch index has no verified manifests")
	}
	document := indexDocument{SchemaVersion: 2, MediaType: IndexMediaType, Manifests: descriptors}
	if partial {
		document.Annotations = map[string]string{"ai-native-paas.example.com/partial": "true"}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return Result{}, err
	}
	return Result{Digest: digest(raw), MediaType: IndexMediaType, Document: raw, Descriptors: append([]Descriptor(nil), descriptors...), Partial: partial, ProductionAllowed: !partial}, nil
}

// WriteOCILayout creates the minimal layout needed to publish the immutable
// index itself through a registry adapter. Child manifests are already
// content-addressed registry objects and are not copied into this layout.
func WriteOCILayout(root string, result Result) error {
	if !buildv1.ValidDigest(result.Digest) || result.MediaType != IndexMediaType || digest(result.Document) != result.Digest || len(result.Descriptors) == 0 {
		return domain.NewError(domain.CodeInvalidArgument, "invalid multi-arch result")
	}
	blobs := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(blobs, strings.TrimPrefix(result.Digest, "sha256:")), result.Document, 0644); err != nil {
		return err
	}
	index := map[string]any{"schemaVersion": 2, "mediaType": IndexMediaType, "manifests": []map[string]any{{"mediaType": IndexMediaType, "digest": result.Digest, "size": len(result.Document)}}}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), indexRaw, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0644)
}

func canonicalPlatforms(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > 8 {
		return nil, domain.NewError(domain.CodeInvalidArgument, "multi-arch platforms are required")
	}
	seen := map[string]struct{}{}
	platforms := make([]string, 0, len(input))
	for _, platform := range input {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return nil, domain.NewError(domain.CodeInvalidArgument, "unsupported multi-arch platform")
		}
		if _, exists := seen[platform]; exists {
			continue
		}
		seen[platform] = struct{}{}
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
