//go:build darwin

package workspaceagent

import (
	"errors"
	"os/exec"
	"syscall"
)

type processControl struct{}

func newProcessControl(_ string, required bool) (*processControl, error) {
	if required {
		return nil, errors.New("delegated workspace command cgroup is unavailable")
	}
	return &processControl{}, nil
}

func (*processControl) Configure(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
func (*processControl) Terminate(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }
func (*processControl) Kill(pid int)      { _ = syscall.Kill(-pid, syscall.SIGKILL) }
func (*processControl) Close()            {}
