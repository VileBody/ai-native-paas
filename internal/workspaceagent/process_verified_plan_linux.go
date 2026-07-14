//go:build linux

package workspaceagent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

func ExecuteVerifiedTofuApply(arguments []string) error {
	expected, planPath, err := validateVerifiedApplyArguments(arguments)
	if err != nil {
		return err
	}
	fd, err := syscall.Open(planPath, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("open exact plan artifact")
	}
	file := os.NewFile(uintptr(fd), "verified-tofu-plan")
	if file == nil {
		_ = syscall.Close(fd)
		return errors.New("open exact plan artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 8<<30 {
		return errors.New("exact plan artifact is not a bounded regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return errors.New("hash exact plan artifact")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return errors.New("exact plan artifact digest mismatch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errors.New("rewind exact plan artifact")
	}
	tofu, err := exec.LookPath("tofu")
	if err != nil {
		return errors.New("OpenTofu executable is unavailable")
	}
	// syscall.Open leaves this descriptor available across exec. OpenTofu reads
	// the already-verified inode, not a path that can be swapped after hashing.
	return syscall.Exec(tofu, []string{"tofu", "apply", "-input=false", "-auto-approve", fmt.Sprintf("/proc/self/fd/%d", fd)}, os.Environ())
}
