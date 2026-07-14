package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"gopkg.in/yaml.v3"
)

const (
	maxKustomizationBytes = 1 << 20
	maxResourceBytes      = 2 << 20
)

type KustomizeResult struct {
	Manifest  []byte
	Digest    string
	Resources []string
}

type kustomizationFile struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Resources  []string `yaml:"resources"`
}

// RenderKustomize implements the controlled-beta local-resource subset. It is
// intentionally strict: remote bases, generators, plugins, symlinks and
// unrecognized fields are rejected instead of delegating execution to a
// control-plane shell.
func RenderKustomize(repositoryRoot, relativePath string) (KustomizeResult, error) {
	base, err := safeDirectory(repositoryRoot, relativePath)
	if err != nil {
		return KustomizeResult{}, err
	}
	kustomizationPath, err := findKustomization(base)
	if err != nil {
		return KustomizeResult{}, err
	}
	raw, err := boundedRead(kustomizationPath, maxKustomizationBytes)
	if err != nil {
		return KustomizeResult{}, err
	}
	var config kustomizationFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return KustomizeResult{}, domain.NewError(domain.CodeInvalidArgument, "invalid or unsupported kustomization")
	}
	if err := requireYAMLEOF(decoder); err != nil || config.APIVersion != "kustomize.config.k8s.io/v1beta1" || config.Kind != "Kustomization" || len(config.Resources) == 0 || len(config.Resources) > 256 {
		return KustomizeResult{}, domain.NewError(domain.CodeInvalidArgument, "invalid controlled-beta kustomization")
	}
	var manifest bytes.Buffer
	resources := make([]string, 0, len(config.Resources))
	seen := map[string]struct{}{}
	for _, resource := range config.Resources {
		canonical, target, err := safeResource(base, resource)
		if err != nil {
			return KustomizeResult{}, err
		}
		if _, duplicate := seen[canonical]; duplicate {
			return KustomizeResult{}, domain.NewError(domain.CodeConflict, "duplicate kustomize resource")
		}
		seen[canonical] = struct{}{}
		resourceRaw, err := boundedRead(target, maxResourceBytes)
		if err != nil {
			return KustomizeResult{}, err
		}
		if err := appendCanonicalDocuments(&manifest, resourceRaw); err != nil {
			return KustomizeResult{}, err
		}
		resources = append(resources, canonical)
	}
	if manifest.Len() == 0 {
		return KustomizeResult{}, domain.NewError(domain.CodeInvalidArgument, "kustomization rendered no resources")
	}
	result := append([]byte(nil), manifest.Bytes()...)
	sum := sha256.Sum256(result)
	return KustomizeResult{Manifest: result, Digest: "sha256:" + hex.EncodeToString(sum[:]), Resources: resources}, nil
}

func safeDirectory(repositoryRoot, relativePath string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(repositoryRoot))
	if err != nil || root == string(filepath.Separator) {
		return "", domain.NewError(domain.CodeInvalidArgument, "invalid repository root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", domain.Wrap(domain.CodeInvalidArgument, "resolve repository root", err)
	}
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		relativePath = "."
	}
	if strings.ContainsAny(relativePath, "\x00\\") || filepath.IsAbs(relativePath) ||
		(filepath.ToSlash(filepath.Clean(relativePath)) != relativePath && relativePath != ".") ||
		relativePath == ".." || strings.HasPrefix(relativePath, "../") {
		return "", domain.NewError(domain.CodeInvalidArgument, "unsafe kustomize path")
	}
	base := filepath.Join(root, filepath.FromSlash(relativePath))
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil || resolved != base || !inside(root, resolved) {
		return "", domain.NewError(domain.CodeInvalidArgument, "kustomize path escapes repository")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", domain.NewError(domain.CodeInvalidArgument, "kustomize path is not a directory")
	}
	return resolved, nil
}

func findKustomization(base string) (string, error) {
	var found []string
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		path := filepath.Join(base, name)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return "", domain.NewError(domain.CodeInvalidArgument, "kustomization must be a regular file")
			}
			found = append(found, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if len(found) != 1 {
		return "", domain.NewError(domain.CodeInvalidArgument, "exactly one kustomization file is required")
	}
	return found[0], nil
}

func safeResource(base, value string) (string, string, error) {
	value = strings.TrimSpace(value)
	clean := filepath.ToSlash(filepath.Clean(value))
	if value == "" || clean != value || filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\\") || value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "://") || strings.HasPrefix(value, "git@") {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "unsafe kustomize resource path")
	}
	extension := strings.ToLower(filepath.Ext(value))
	if extension != ".yaml" && extension != ".yml" {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "controlled-beta kustomize resources must be YAML files")
	}
	target := filepath.Join(base, filepath.FromSlash(value))
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || resolved != target || !inside(base, resolved) {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "kustomize resource escapes root")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", domain.NewError(domain.CodeInvalidArgument, "kustomize resource must be a regular file")
	}
	return clean, resolved, nil
}

func inside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func boundedRead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, domain.NewError(domain.CodeInvalidArgument, "GitOps input exceeds size limit")
	}
	return raw, nil
}

func appendCanonicalDocuments(destination *bytes.Buffer, raw []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return domain.NewError(domain.CodeInvalidArgument, "invalid Kubernetes YAML resource")
		}
		if len(document.Content) == 0 || len(document.Content[0].Content) == 0 {
			continue
		}
		var metadata struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := document.Decode(&metadata); err != nil || strings.TrimSpace(metadata.APIVersion) == "" || strings.TrimSpace(metadata.Kind) == "" || strings.TrimSpace(metadata.Metadata.Name) == "" {
			return domain.NewError(domain.CodeInvalidArgument, "Kubernetes resource identity is required")
		}
		destination.WriteString("---\n")
		encoder := yaml.NewEncoder(destination)
		encoder.SetIndent(2)
		if err := encoder.Encode(document.Content[0]); err != nil {
			return err
		}
		_ = encoder.Close()
	}
}

func requireYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("multiple YAML documents")
}
