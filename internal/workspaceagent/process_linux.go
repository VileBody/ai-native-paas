//go:build linux

package workspaceagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type processControl struct {
	directory string
	file      *os.File
	cgroup    bool
}

func newProcessControl(commandID string, required bool) (*processControl, error) {
	control, err := newCgroupControl(commandID)
	if err == nil {
		return control, nil
	}
	if required {
		return nil, errors.New("delegated workspace command cgroup is unavailable")
	}
	return &processControl{}, nil
}

func newCgroupControl(commandID string) (*processControl, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	relative := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if relative == "" || !filepath.IsAbs(relative) || strings.Contains(relative, "..") {
		return nil, errors.New("unified cgroup path is invalid")
	}
	base := filepath.Join("/sys/fs/cgroup", relative, "commands")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(commandID))
	directory := filepath.Join(base, hex.EncodeToString(digest[:16]))
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	file, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	return &processControl{directory: directory, file: file, cgroup: true}, nil
}

func (c *processControl) Configure(command *exec.Cmd) {
	attributes := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if c.cgroup {
		attributes.UseCgroupFD = true
		attributes.CgroupFD = int(c.file.Fd())
	}
	command.SysProcAttr = attributes
}

func (c *processControl) Terminate(pid int) {
	if c.cgroup {
		if err := os.WriteFile(filepath.Join(c.directory, "cgroup.kill"), []byte("1\n"), 0o200); err == nil {
			return
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func (c *processControl) Kill(pid int) {
	if c.cgroup {
		if err := os.WriteFile(filepath.Join(c.directory, "cgroup.kill"), []byte(strconv.Itoa(1)+"\n"), 0o200); err == nil {
			return
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func (c *processControl) Close() {
	if c == nil {
		return
	}
	if c.file != nil {
		_ = c.file.Close()
	}
	if c.directory != "" {
		_ = os.Remove(c.directory)
	}
}
