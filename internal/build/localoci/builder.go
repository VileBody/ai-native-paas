package localoci

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

const (
	ManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	LayerMediaType    = "application/vnd.oci.image.layer.v1.tar"
)

type Builder struct {
	Root string
}

func (b Builder) Build(ctx context.Context, request application.BuildExecutionRequest) (application.BuildOutput, error) {
	if request.Detection.Backend != application.BackendBuildpacks {
		return application.BuildOutput{}, domain.NewError(domain.CodeInvalidArgument, "local OCI builder only accepts buildpack detection")
	}
	root := b.Root
	if root == "" {
		root = os.TempDir()
	}
	work, err := os.MkdirTemp(root, "local-oci-build-")
	if err != nil {
		return application.BuildOutput{}, domain.Wrap(domain.CodePlatformFailure, "create build directory", err)
	}
	defer os.RemoveAll(work)
	rootfs := filepath.Join(work, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "workspace"), 0755); err != nil {
		return application.BuildOutput{}, domain.Wrap(domain.CodePlatformFailure, "create rootfs", err)
	}
	entrypoint, err := b.compile(ctx, request, rootfs)
	if err != nil {
		return application.BuildOutput{}, err
	}
	layout := filepath.Join(root, "oci-layout-"+sanitize(request.BuildID))
	_ = os.RemoveAll(layout)
	if err := writeLayout(layout, rootfs, entrypoint, request.Environment, request.Source.Revision.CommitSHA); err != nil {
		return application.BuildOutput{}, domain.Wrap(domain.CodePlatformFailure, "write OCI layout", err)
	}
	digest, err := ManifestDigest(layout)
	if err != nil {
		_ = os.RemoveAll(layout)
		return application.BuildOutput{}, domain.Wrap(domain.CodePlatformFailure, "read OCI digest", err)
	}
	if request.LogWriter != nil {
		_, _ = fmt.Fprintf(request.LogWriter, "runtime=%s buildpack=%s source=%s artifact=%s\n", request.Detection.Runtime, request.Detection.BuildpackID, request.Source.Revision.CommitSHA, digest)
		for _, secret := range request.Secrets {
			_, _ = fmt.Fprintf(request.LogWriter, "build secret mounted: %s\n", secret.Name)
		}
	}
	return application.BuildOutput{OCILayoutPath: layout, ManifestDigest: digest, MediaType: ManifestMediaType, Metadata: map[string]string{"runtime": request.Detection.Runtime, "source_revision": request.Source.Revision.CommitSHA}}, nil
}
func (Builder) Cancel(context.Context, string) error { return nil }

func (b Builder) compile(ctx context.Context, request application.BuildExecutionRequest, rootfs string) ([]string, error) {
	env := cleanEnv(request.Environment)
	switch request.Detection.Runtime {
	case "go":
		output := filepath.Join(rootfs, "workspace", "app")
		args := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", output, "."}
		if len(request.Config.BuildCommand) > 0 {
			args = request.Config.BuildCommand
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = request.Source.Path
		cmd.Env = append(env, "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if err := run(ctx, cmd, request.LogWriter); err != nil {
			return nil, classifyCommand("GO_BUILD_FAILED", err)
		}
		if err := os.Chmod(output, 0755); err != nil {
			return nil, domain.Wrap(domain.CodePlatformFailure, "chmod Go artifact", err)
		}
		return []string{"/workspace/app"}, nil
	case "nodejs":
		entry := "index.js"
		if _, err := os.Stat(filepath.Join(request.Source.Path, entry)); err != nil {
			return nil, domain.NewError(domain.CodeUserFailure, "NODE_ENTRYPOINT_MISSING: index.js not found")
		}
		cmd := exec.CommandContext(ctx, "node", "--check", entry)
		cmd.Dir = request.Source.Path
		cmd.Env = env
		if err := run(ctx, cmd, request.LogWriter); err != nil {
			return nil, classifyCommand("NODE_SYNTAX_ERROR", err)
		}
		if err := copyTree(request.Source.Path, filepath.Join(rootfs, "workspace")); err != nil {
			return nil, domain.Wrap(domain.CodePlatformFailure, "copy Node.js source", err)
		}
		return []string{"node", "/workspace/index.js"}, nil
	case "python":
		entry := "main.py"
		if _, err := os.Stat(filepath.Join(request.Source.Path, entry)); err != nil {
			return nil, domain.NewError(domain.CodeUserFailure, "PYTHON_ENTRYPOINT_MISSING: main.py not found")
		}
		cmd := exec.CommandContext(ctx, "python3", "-c", "import pathlib; p=pathlib.Path('main.py'); compile(p.read_bytes(), str(p), 'exec')")
		cmd.Dir = request.Source.Path
		cmd.Env = append(env, "PYTHONDONTWRITEBYTECODE=1")
		if err := run(ctx, cmd, request.LogWriter); err != nil {
			return nil, classifyCommand("PYTHON_SYNTAX_ERROR", err)
		}
		if err := copyTree(request.Source.Path, filepath.Join(rootfs, "workspace")); err != nil {
			return nil, domain.Wrap(domain.CodePlatformFailure, "copy Python source", err)
		}
		return []string{"python3", "/workspace/main.py"}, nil
	default:
		return nil, domain.NewError(domain.CodeUserFailure, "UNSUPPORTED_RUNTIME: "+request.Detection.Runtime)
	}
}

func writeLayout(layout, rootfs string, entrypoint []string, env map[string]string, revision string) error {
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0755); err != nil {
		return err
	}
	layer, err := deterministicTar(rootfs)
	if err != nil {
		return err
	}
	layerDigest := digestBytes(layer)
	if err := writeBlob(layout, layerDigest, layer); err != nil {
		return err
	}
	config := map[string]any{
		"created":      "1970-01-01T00:00:00Z",
		"architecture": runtime.GOARCH,
		"os":           "linux",
		"config": map[string]any{
			"Entrypoint": entrypoint,
			"Env":        normalizedEnv(env),
			"WorkingDir": "/workspace",
			"Labels":     map[string]string{"org.opencontainers.image.revision": revision},
		},
		"rootfs":  map[string]any{"type": "layers", "diff_ids": []string{layerDigest}},
		"history": []map[string]any{{"created": "1970-01-01T00:00:00Z", "created_by": "ai-native-paas local fixture builder"}},
	}
	configRaw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	configDigest := digestBytes(configRaw)
	if err := writeBlob(layout, configDigest, configRaw); err != nil {
		return err
	}
	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     ManifestMediaType,
		"config":        map[string]any{"mediaType": ConfigMediaType, "digest": configDigest, "size": len(configRaw)},
		"layers":        []map[string]any{{"mediaType": LayerMediaType, "digest": layerDigest, "size": len(layer)}},
		"annotations":   map[string]string{"org.opencontainers.image.revision": revision},
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestDigest := digestBytes(manifestRaw)
	if err := writeBlob(layout, manifestDigest, manifestRaw); err != nil {
		return err
	}
	index := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []map[string]any{{"mediaType": ManifestMediaType, "digest": manifestDigest, "size": len(manifestRaw), "annotations": map[string]string{"org.opencontainers.image.ref.name": "build"}}}}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), indexRaw, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(layout, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0644)
}

func ManifestDigest(layout string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		return "", err
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return "", err
	}
	if len(index.Manifests) != 1 || !strings.HasPrefix(index.Manifests[0].Digest, "sha256:") {
		return "", errors.New("OCI index must contain one sha256 manifest")
	}
	blob, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")))
	if err != nil {
		return "", err
	}
	if digestBytes(blob) != index.Manifests[0].Digest {
		return "", errors.New("manifest digest mismatch")
	}
	return index.Manifests[0].Digest, nil
}

func deterministicTar(root string) ([]byte, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlinks are not permitted in generated OCI layer")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, err
		}
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
		}
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source symlink rejected by local builder")
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeIn := input.Close()
		closeOut := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeIn != nil {
			return closeIn
		}
		return closeOut
	})
}
func run(ctx context.Context, cmd *exec.Cmd, logs io.Writer) error {
	var buffer bytes.Buffer
	if logs == nil {
		logs = io.Discard
	}
	cmd.Stdout = io.MultiWriter(logs, &buffer)
	cmd.Stderr = io.MultiWriter(logs, &buffer)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w: %s", err, truncate(buffer.String(), 4096))
	}
	return ctx.Err()
}
func classifyCommand(code string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return domain.Wrap(domain.CodeTimeout, code, err)
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return domain.Wrap(domain.CodePlatformFailure, code, err)
	}
	return domain.Wrap(domain.CodeUserFailure, code, err)
}
func cleanEnv(env map[string]string) []string {
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		base = append(base, key+"="+env[key])
	}
	return base
}
func normalizedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}
func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func writeBlob(layout, digest string, raw []byte) error {
	return os.WriteFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), raw, 0644)
}
func sanitize(v string) string {
	v = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, v)
	if v == "" {
		return "build"
	}
	return v
}
func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

var _ application.Builder = Builder{}
