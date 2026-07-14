//go:build !linux && !darwin

package workspaceagent

import (
	"errors"
	"os"
	"os/exec"
)

type processControl struct{}

func newProcessControl(_ string, _ bool) (*processControl, error) {
	return nil, errors.New("workspace command process isolation is unsupported")
}
func (*processControl) Configure(*exec.Cmd) {}
func (*processControl) Terminate(pid int)   { _ = os.FindProcess(pid) }
func (*processControl) Kill(pid int)        { _ = os.FindProcess(pid) }
func (*processControl) Close()              {}
