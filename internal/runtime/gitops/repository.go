package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type LocalRepository struct {
	Root string
	mu   sync.Mutex
}

type releaseIndex struct {
	CellID       string `json:"cell_id"`
	ReleaseID    string `json:"release_id"`
	DeploymentID string `json:"deployment_id"`
	Path         string `json:"path"`
	ManifestHash string `json:"manifest_hash"`
}

func NewLocalRepository(ctx context.Context, root string) (*LocalRepository, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(filepath.Separator) || strings.TrimSpace(root) == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "GitOps repository root is invalid")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, domain.NewError(domain.CodeInvalidArgument, "GitOps repository root must be a directory")
	}
	repo := &LocalRepository{Root: root}
	if _, err := os.Stat(filepath.Join(root, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err = repo.git(ctx, "init", "-b", "main"); err != nil {
			return nil, err
		}
		if _, err = repo.git(ctx, "config", "user.name", "PaaS Control Plane"); err != nil {
			return nil, err
		}
		if _, err = repo.git(ctx, "config", "user.email", "paas-control-plane@example.invalid"); err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".work/\n"), 0o640); err != nil {
			return nil, err
		}
		if _, err = repo.git(ctx, "add", ".gitignore"); err != nil {
			return nil, err
		}
		if _, err = repo.git(ctx, "commit", "-m", "chore: initialize GitOps repository"); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *LocalRepository) Commit(ctx context.Context, req application.CommitRequest) (application.CommitResult, error) {
	if r == nil || r.Root == "" {
		return application.CommitResult{}, domain.NewError(domain.CodeUnavailable, "GitOps repository is not configured")
	}
	if !runtimev1.ValidPlatformID(req.Bundle.ReleaseID) || !runtimev1.ValidDNSLabel(req.Bundle.CellID) || !buildv1.ValidDigest(req.Bundle.ManifestHash) || len(req.Bundle.Files) != 2 || !runtimev1.ValidPlatformID(req.DeploymentID) || !validCommitActor(req.ActorID) {
		return application.CommitResult{}, domain.NewError(domain.CodeInvalidArgument, "GitOps commit request is incomplete or unsafe")
	}
	if err := validateBundlePath(req.Bundle.Path, req.Bundle.CellID); err != nil {
		return application.CommitResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	indexRel := filepath.ToSlash(filepath.Join(".platform", "releases", req.Bundle.ReleaseID+".json"))
	if existing, err := r.readIndex(indexRel); err == nil {
		if existing.ManifestHash != req.Bundle.ManifestHash || existing.Path != req.Bundle.Path || existing.CellID != req.Bundle.CellID {
			return application.CommitResult{}, domain.NewError(domain.CodeConflict, "release id already has different GitOps content")
		}
		sha, err := r.commitForPath(ctx, indexRel)
		if err != nil {
			return application.CommitResult{}, err
		}
		return application.CommitResult{CommitSHA: sha, Path: existing.Path, ManifestHash: existing.ManifestHash}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return application.CommitResult{}, err
	}
	for name, raw := range req.Bundle.Files {
		if name != "namespace.yaml" && name != "paasapp.yaml" {
			return application.CommitResult{}, domain.NewError(domain.CodeInvalidArgument, "unexpected GitOps manifest file")
		}
		relative := filepath.ToSlash(filepath.Join(req.Bundle.Path, name))
		if err := secureWriteAtomic(r.Root, relative, raw, 0o640); err != nil {
			return application.CommitResult{}, err
		}
	}
	index := releaseIndex{CellID: req.Bundle.CellID, ReleaseID: req.Bundle.ReleaseID, DeploymentID: req.DeploymentID, Path: req.Bundle.Path, ManifestHash: req.Bundle.ManifestHash}
	raw, _ := json.MarshalIndent(index, "", "  ")
	raw = append(raw, '\n')
	if err := secureWriteAtomic(r.Root, indexRel, raw, 0o640); err != nil {
		return application.CommitResult{}, err
	}
	if _, err := r.git(ctx, "add", "--", req.Bundle.Path, indexRel); err != nil {
		return application.CommitResult{}, err
	}
	message := fmt.Sprintf("deploy(%s): release %s\n\nRelease-ID: %s\nDeployment-ID: %s\nManifest-Hash: %s\nActor-ID: %s", req.Bundle.CellID, req.Bundle.ReleaseID, req.Bundle.ReleaseID, req.DeploymentID, req.Bundle.ManifestHash, req.ActorID)
	if _, err := r.git(ctx, "commit", "-m", message); err != nil {
		return application.CommitResult{}, err
	}
	sha, err := r.head(ctx)
	if err != nil {
		return application.CommitResult{}, err
	}
	return application.CommitResult{CommitSHA: sha, Path: req.Bundle.Path, ManifestHash: req.Bundle.ManifestHash}, nil
}

func (r *LocalRepository) FindByRelease(ctx context.Context, cellID, releaseID string) (application.CommitResult, bool, error) {
	if r == nil {
		return application.CommitResult{}, false, domain.NewError(domain.CodeUnavailable, "GitOps repository is not configured")
	}
	if !runtimev1.ValidDNSLabel(cellID) || !runtimev1.ValidPlatformID(releaseID) {
		return application.CommitResult{}, false, domain.NewError(domain.CodeInvalidArgument, "GitOps lookup identity is unsafe")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	indexRel := filepath.ToSlash(filepath.Join(".platform", "releases", releaseID+".json"))
	index, err := r.readIndex(indexRel)
	if errors.Is(err, os.ErrNotExist) {
		return application.CommitResult{}, false, nil
	}
	if err != nil {
		return application.CommitResult{}, false, err
	}
	if index.CellID != cellID {
		return application.CommitResult{}, false, domain.NewError(domain.CodeConflict, "release belongs to another runtime cell")
	}
	sha, err := r.commitForPath(ctx, indexRel)
	if err != nil {
		return application.CommitResult{}, false, err
	}
	return application.CommitResult{CommitSHA: sha, Path: index.Path, ManifestHash: index.ManifestHash}, true, nil
}

func (r *LocalRepository) readIndex(relative string) (releaseIndex, error) {
	var value releaseIndex
	raw, err := secureReadFile(r.Root, relative)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		return value, domain.Wrap(domain.CodeInternal, "decode GitOps release index", err)
	}
	return value, nil
}
func (r *LocalRepository) commitForPath(ctx context.Context, relative string) (string, error) {
	value, err := r.git(ctx, "log", "-1", "--format=%H", "--", relative)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domain.NewError(domain.CodeInternal, "GitOps release index has no commit")
	}
	return value, nil
}
func (r *LocalRepository) head(ctx context.Context) (string, error) {
	value, err := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(value), err
}
func (r *LocalRepository) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", domain.Wrap(domain.CodeUnavailable, "git "+strings.Join(args, " "), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(raw))))
	}
	return string(raw), nil
}
func safeRelativePath(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return domain.NewError(domain.CodeInvalidArgument, "unsafe GitOps path")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return domain.NewError(domain.CodeInvalidArgument, "unsafe GitOps path")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return domain.NewError(domain.CodeInvalidArgument, "unsafe GitOps path")
		}
	}
	return nil
}

func validateBundlePath(value, cellID string) error {
	if err := safeRelativePath(value); err != nil {
		return err
	}
	parts := strings.Split(value, "/")
	if len(parts) != 7 || parts[0] != "cells" || parts[1] != cellID || parts[2] != "tenants" || parts[4] != "apps" {
		return domain.NewError(domain.CodeInvalidArgument, "GitOps bundle path does not match the platform layout")
	}
	if !runtimev1.ValidPlatformID(parts[3]) || !runtimev1.ValidPlatformID(parts[5]) || !runtimev1.ValidDNSLabel(parts[6]) {
		return domain.NewError(domain.CodeInvalidArgument, "GitOps bundle path contains an unsafe identity")
	}
	return nil
}

func validCommitActor(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "\r\n\x00")
}

func securePath(root, relative string, allowMissingLeaf bool) (string, error) {
	if err := safeRelativePath(relative); err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", domain.NewError(domain.CodeInvalidArgument, "GitOps path escapes repository root")
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if allowMissingLeaf || index < len(parts)-1 {
				continue
			}
			return "", statErr
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", domain.NewError(domain.CodeConflict, "GitOps path contains a symbolic link")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", domain.NewError(domain.CodeConflict, "GitOps path component is not a directory")
		}
	}
	return target, nil
}

func secureReadFile(root, relative string) ([]byte, error) {
	path, err := securePath(root, relative, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, domain.NewError(domain.CodeConflict, "GitOps file is not a regular file")
	}
	return os.ReadFile(path)
}

func secureWriteAtomic(root, relative string, raw []byte, mode os.FileMode) error {
	path, err := securePath(root, relative, true)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	// Re-check every component after creation. The repository directory is
	// platform-owned (0750); this prevents pre-existing symlink escapes while
	// avoiding a dependency on platform-specific openat2 syscalls.
	if _, err = securePath(root, relative, true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(raw)
	}
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if existing, statErr := os.Lstat(path); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
		return domain.NewError(domain.CodeConflict, "GitOps target is a symbolic link")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return os.Rename(name, path)
}

var _ application.GitOpsRepository = (*LocalRepository)(nil)
