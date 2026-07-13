package workspace

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

	"github.com/keir-research/ai-native-paas/internal/source/application"
)

type Git struct{ Root string }

func (g Git) CloneExact(ctx context.Context, remoteURL, commitSHA, branch string, credential application.ProviderCredential) (string, error) {
	root := g.Root
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "paas-workspace-")
	if err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0700)
	cleanup := func(e error) (string, error) { _ = os.RemoveAll(dir); return "", e }
	if _, err = runGit(ctx, dir, credential, "init", "--quiet"); err != nil {
		return cleanup(err)
	}
	if _, err = runGit(ctx, dir, credential, "remote", "add", "origin", remoteURL); err != nil {
		return cleanup(err)
	}
	if _, err = runGit(ctx, dir, credential, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", commitSHA); err != nil {
		return cleanup(err)
	}
	if _, err = runGit(ctx, dir, credential, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return cleanup(err)
	}
	if branch != "" {
		if _, err = runGit(ctx, dir, credential, "branch", "-f", branch, "HEAD"); err != nil {
			return cleanup(err)
		}
	}
	return dir, nil
}
func (g Git) Apply(ctx context.Context, dir string, ops []application.PatchOperation) error {
	for _, op := range ops {
		target, err := securePath(dir, op.Path)
		if err != nil {
			return err
		}
		if op.Delete {
			if err = os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if _, err = securePath(dir, op.Path); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if op.Executable {
			mode = 0755
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".paas-write-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		ok := false
		defer func() {
			if !ok {
				_ = os.Remove(name)
			}
		}()
		if err = tmp.Chmod(mode); err == nil {
			_, err = tmp.Write(op.Content)
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
		if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to replace symlink")
		}
		if err = os.Rename(name, target); err != nil {
			return err
		}
		ok = true
	}
	return ctx.Err()
}
func (g Git) Commit(ctx context.Context, dir, message, authorName, authorEmail string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("commit message required")
	}
	if authorName == "" {
		authorName = "PaaS Agent"
	}
	if authorEmail == "" {
		authorEmail = "agent@paas.invalid"
	}
	if _, err := runGit(ctx, dir, application.ProviderCredential{}, "add", "--all"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, application.ProviderCredential{}, "-c", "user.name="+authorName, "-c", "user.email="+authorEmail, "commit", "--quiet", "--no-gpg-sign", "-m", message); err != nil {
		return "", err
	}
	out, err := runGit(ctx, dir, application.ProviderCredential{}, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}
func (g Git) Push(ctx context.Context, dir, branch, expectedSHA, commitSHA string, credential application.ProviderCredential) (bool, error) {
	if branch == "" || commitSHA == "" {
		return false, errors.New("branch and commit are required")
	}
	remoteBefore, remoteErr := g.RemoteHead(ctx, dir, branch, credential)
	if remoteErr != nil {
		return false, remoteErr
	}
	leaseExpected := expectedSHA
	if remoteBefore == "" {
		// A new branch must be leased against non-existence, not against the base
		// commit from another branch.
		leaseExpected = ""
	}
	lease := "--force-with-lease=refs/heads/" + branch + ":" + leaseExpected
	_, err := runGit(ctx, dir, credential, "push", "--porcelain", "origin", commitSHA+":refs/heads/"+branch, lease)
	if err == nil {
		return true, nil
	}
	remote, checkErr := g.RemoteHead(ctx, dir, branch, credential)
	if checkErr == nil && remote == commitSHA {
		return true, nil
	}
	return false, err
}
func (g Git) RemoteHead(ctx context.Context, dir, branch string, credential application.ProviderCredential) (string, error) {
	out, err := runGit(ctx, dir, credential, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
func (g Git) Cleanup(_ context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

func securePath(root, relative string) (string, error) {
	if relative == "" || strings.ContainsRune(relative, '\x00') || filepath.IsAbs(relative) {
		return "", errors.New("invalid patch path")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal rejected")
	}
	slash := filepath.ToSlash(clean)
	if slash == ".git" || strings.HasPrefix(slash, ".git/") {
		return "", errors.New("git metadata is not writable")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	current := rootAbs
	parts := strings.Split(clean, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("symlink traversal rejected")
			}
			if i < len(parts)-1 && !info.IsDir() {
				return "", errors.New("non-directory path component")
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}
	}
	target := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return target, nil
}
func ValidatePatchPath(root, relative string) error { _, err := securePath(root, relative); return err }
func runGit(ctx context.Context, dir string, credential application.ProviderCredential, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+nullDevice())
	var askpass string
	if credential.Token != "" {
		f, err := os.CreateTemp(dir, ".askpass-")
		if err != nil {
			return "", err
		}
		askpass = f.Name()
		script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$PAAS_GIT_USERNAME\" ;;\n*) printf '%s\\n' \"$PAAS_GIT_TOKEN\" ;;\nesac\n"
		if _, err = f.WriteString(script); err != nil {
			_ = f.Close()
			_ = os.Remove(askpass)
			return "", err
		}
		_ = f.Chmod(0700)
		_ = f.Close()
		defer os.Remove(askpass)
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "PAAS_GIT_USERNAME="+credential.Username, "PAAS_GIT_TOKEN="+credential.Token)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("git %s failed: %w: %s", args[0], err, redact(stderr.String(), credential.Token))
	}
	return stdout.String(), nil
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
