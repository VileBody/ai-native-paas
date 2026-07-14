package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	"gopkg.in/yaml.v3"
)

const (
	maximumHelmChartFiles = 4096
	maximumHelmChartBytes = 64 << 20
)

var (
	helmVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	helmNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	// These template functions make output depend on wall time, entropy, the
	// host environment, DNS or live Kubernetes state. Controlled-beta renders
	// must receive every capability as an explicit pinned input instead.
	nondeterministicHelmTemplate = regexp.MustCompile(`(?s){{-?(?:[[:space:]]*(?:now|randAlphaNum|randAlpha|randNumeric|randAscii|uuidv4|env|expandenv|getHostByName|lookup)\b|[^}]*(?:[[:space:]|(])(?:now|randAlphaNum|randAlpha|randNumeric|randAscii|uuidv4|env|expandenv|getHostByName|lookup)\b)[^}]*-?}}`)
)

type helmChartMetadata struct {
	APIVersion   string `yaml:"apiVersion"`
	Name         string `yaml:"name"`
	Version      string `yaml:"version"`
	Dependencies []struct {
		Name, Version, Repository string
	} `yaml:"dependencies"`
}

// PinnedHelmChartDigest hashes a canonical, symlink-free local chart tree and
// rejects template functions that can observe time, randomness, DNS, process
// environment or a live cluster. Helm execution itself belongs in the
// disposable workspace, not on a control-plane host.
func PinnedHelmChartDigest(chartRoot string) (string, error) {
	root, err := canonicalHelmRoot(chartRoot)
	if err != nil {
		return "", err
	}
	type chartFile struct {
		path string
		raw  []byte
	}
	files := make([]chartFile, 0)
	total := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || relative == ".git" || strings.HasPrefix(relative, ".git/") {
			return domain.NewError(domain.CodeInvalidArgument, "Helm chart contains a forbidden path")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maximumHelmChartBytes {
			return domain.NewError(domain.CodeInvalidArgument, "Helm chart contains a non-regular or oversized file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(raw)
		if len(files) >= maximumHelmChartFiles || total > maximumHelmChartBytes {
			return domain.NewError(domain.CodeInvalidArgument, "Helm chart exceeds controlled-beta limits")
		}
		if strings.HasPrefix(relative, "templates/") && nondeterministicHelmTemplate.Match(raw) {
			return domain.NewError(domain.CodeForbidden, "Helm template uses nondeterministic or live-state input")
		}
		files = append(files, chartFile{path: relative, raw: raw})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart is empty")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	hash := sha256.New()
	for _, file := range files {
		hash.Write([]byte(file.path))
		hash.Write([]byte{0})
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(file.raw)))
		hash.Write(size[:])
		hash.Write([]byte{0})
		hash.Write(file.raw)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyPinnedHelmChart checks the chart metadata and tree digest immediately
// before a governed workspace schedules `helm template`.
func VerifyPinnedHelmChart(chartRoot, expectedVersion, expectedDigest string) error {
	if !helmVersionPattern.MatchString(expectedVersion) || !buildv1.ValidDigest(expectedDigest) {
		return domain.NewError(domain.CodeInvalidArgument, "Helm pin is invalid")
	}
	root, err := canonicalHelmRoot(chartRoot)
	if err != nil {
		return err
	}
	chartRaw, err := os.ReadFile(filepath.Join(root, "Chart.yaml"))
	if err != nil || len(chartRaw) > 1<<20 {
		return domain.NewError(domain.CodeInvalidArgument, "Helm Chart.yaml is invalid")
	}
	var metadata helmChartMetadata
	decoder := yaml.NewDecoder(bytes.NewReader(chartRaw))
	if err := decoder.Decode(&metadata); err != nil || metadata.APIVersion != "v2" || !helmNamePattern.MatchString(metadata.Name) || metadata.Version != expectedVersion {
		return domain.NewError(domain.CodeConflict, "Helm chart metadata does not match the pin")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.NewError(domain.CodeInvalidArgument, "Helm Chart.yaml contains multiple documents")
	}
	if len(metadata.Dependencies) > 0 {
		if info, err := os.Lstat(filepath.Join(root, "Chart.lock")); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return domain.NewError(domain.CodeConflict, "Helm dependencies require a pinned Chart.lock")
		}
		for _, dependency := range metadata.Dependencies {
			if !helmNamePattern.MatchString(dependency.Name) || !helmVersionPattern.MatchString(dependency.Version) || strings.TrimSpace(dependency.Repository) == "" {
				return domain.NewError(domain.CodeConflict, "Helm dependency is not exactly pinned")
			}
		}
	}
	digest, err := PinnedHelmChartDigest(root)
	if err != nil {
		return err
	}
	if digest != expectedDigest {
		return domain.NewError(domain.CodeConflict, "Helm chart digest does not match the pin")
	}
	return nil
}

func canonicalHelmRoot(chartRoot string) (string, error) {
	if strings.TrimSpace(chartRoot) == "" {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart root is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(chartRoot))
	if err != nil || abs == string(filepath.Separator) {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart root is invalid")
	}
	leaf, err := os.Lstat(abs)
	if err != nil || leaf.Mode()&os.ModeSymlink != 0 {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart root must not be a symbolic link")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart root must be canonical and symlink-free")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", domain.NewError(domain.CodeInvalidArgument, "Helm chart root is not a directory")
	}
	return resolved, nil
}
