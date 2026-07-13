package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type RepositoryAccess struct {
	TenantID, RepositoryID, RemoteURL, Username, Token string
}
type Resolver interface {
	ResolveRepository(context.Context, string, string) (RepositoryAccess, error)
}
type Fetcher struct {
	Root     string
	Resolver Resolver
}

func (f Fetcher) Fetch(ctx context.Context, tenantID string, revision sourcev1.SourceRevision, limits application.SourceLimits) (application.SourceSnapshot, error) {
	if err := revision.Validate(); err != nil {
		return application.SourceSnapshot{}, domain.NewError(domain.CodeInvalidArgument, "invalid source revision")
	}
	if f.Resolver == nil {
		return application.SourceSnapshot{}, domain.NewError(domain.CodeUnavailable, "source resolver unavailable")
	}
	access, err := f.Resolver.ResolveRepository(ctx, tenantID, revision.RepositoryID)
	if err != nil {
		return application.SourceSnapshot{}, err
	}
	if access.TenantID != tenantID || access.RepositoryID != revision.RepositoryID {
		return application.SourceSnapshot{}, domain.NewError(domain.CodeNotFound, "repository not found")
	}
	root := f.Root
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return application.SourceSnapshot{}, domain.Wrap(domain.CodePlatformFailure, "create source root", err)
	}
	dir, err := os.MkdirTemp(root, "paas-source-")
	if err != nil {
		return application.SourceSnapshot{}, domain.Wrap(domain.CodePlatformFailure, "create source checkout", err)
	}
	_ = os.Chmod(dir, 0700)
	cleanup := func() error { return os.RemoveAll(dir) }
	fail := func(err error) (application.SourceSnapshot, error) {
		_ = cleanup()
		return application.SourceSnapshot{}, err
	}
	cred := credential{Username: access.Username, Token: access.Token}
	if _, err = runGit(ctx, dir, cred, "init", "--quiet"); err != nil {
		return fail(classifyGit(err))
	}
	if _, err = runGit(ctx, dir, cred, "remote", "add", "origin", access.RemoteURL); err != nil {
		return fail(classifyGit(err))
	}
	if _, err = runGit(ctx, dir, cred, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", revision.CommitSHA); err != nil {
		return fail(classifyGit(err))
	}
	if _, err = runGit(ctx, dir, cred, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return fail(classifyGit(err))
	}
	head, err := runGit(ctx, dir, credential{}, "rev-parse", "HEAD")
	if err != nil {
		return fail(classifyGit(err))
	}
	if !strings.EqualFold(strings.TrimSpace(head), revision.CommitSHA) {
		return fail(domain.NewError(domain.CodeUserFailure, "source checkout does not match exact commit"))
	}
	if !limits.AllowSubmodules {
		if _, err := os.Lstat(filepath.Join(dir, ".gitmodules")); err == nil {
			return fail(domain.NewError(domain.CodeUserFailure, "git submodules are disabled"))
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		return fail(domain.Wrap(domain.CodePlatformFailure, "remove source credentials", err))
	}
	scope, err := safeScope(dir, revision.SourceRoot)
	if err != nil {
		return fail(err)
	}
	size, files, err := inspect(scope, limits)
	if err != nil {
		return fail(err)
	}
	return application.SourceSnapshot{Revision: revision, Path: scope, SizeBytes: size, FileCount: files, Cleanup: cleanup}, nil
}

func safeScope(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", domain.Wrap(domain.CodePlatformFailure, "resolve checkout root", err)
	}
	target := rootAbs
	if relative != "" {
		target = filepath.Join(rootAbs, filepath.FromSlash(relative))
	}
	// Check both the lexical path and the fully resolved path. The lexical
	// check rejects traversal; the resolved check prevents a source_root
	// symlink from turning an in-checkout path into host filesystem access.
	lexicalRel, err := filepath.Rel(rootAbs, target)
	if err != nil || lexicalRel == ".." || strings.HasPrefix(lexicalRel, ".."+string(filepath.Separator)) {
		return "", domain.NewError(domain.CodeInvalidArgument, "source root escapes checkout")
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", domain.NewError(domain.CodeUserFailure, "source root does not exist")
	}
	resolvedRel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", domain.NewError(domain.CodeUserFailure, "source root symlink escapes checkout")
	}
	info, err := os.Stat(targetReal)
	if err != nil || !info.IsDir() {
		return "", domain.NewError(domain.CodeUserFailure, "source root does not exist")
	}
	return targetReal, nil
}
func inspect(root string, limits application.SourceLimits) (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return domain.NewError(domain.CodeUserFailure, "broken source symlink")
			}
			rel, err := filepath.Rel(root, target)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return domain.NewError(domain.CodeUserFailure, "source symlink escapes checkout")
			}
			return nil
		}
		if info.Mode().IsRegular() {
			count++
			total += info.Size()
			if limits.MaxFiles > 0 && count > limits.MaxFiles {
				return domain.NewError(domain.CodeUserFailure, "repository file limit exceeded")
			}
			if limits.MaxBytes > 0 && total > limits.MaxBytes {
				return domain.NewError(domain.CodeUserFailure, "repository size limit exceeded")
			}
		}
		return nil
	})
	return total, count, err
}

type credential struct{ Username, Token string }

func runGit(ctx context.Context, dir string, cred credential, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+nullDevice())
	var askpass string
	if cred.Token != "" {
		file, err := os.CreateTemp(dir, ".source-askpass-")
		if err != nil {
			return "", err
		}
		askpass = file.Name()
		script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$PAAS_GIT_USERNAME\" ;;\n*) printf '%s\\n' \"$PAAS_GIT_TOKEN\" ;;\nesac\n"
		if _, err = file.WriteString(script); err != nil {
			_ = file.Close()
			_ = os.Remove(askpass)
			return "", err
		}
		_ = file.Chmod(0700)
		_ = file.Close()
		defer os.Remove(askpass)
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "PAAS_GIT_USERNAME="+cred.Username, "PAAS_GIT_TOKEN="+cred.Token)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git %s failed: %w: %s", args[0], err, redact(stderr.String(), cred.Token))
	}
	return out.String(), nil
}
func classifyGit(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return domain.Retryable(domain.CodePlatformFailure, "source provider timeout", err)
	}
	return domain.Wrap(domain.CodePlatformFailure, "source checkout failed", err)
}
func redact(v, secret string) string {
	if secret != "" {
		v = strings.ReplaceAll(v, secret, "[REDACTED]")
	}
	return v
}
func nullDevice() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}

var _ application.SourceFetcher = Fetcher{}
