//go:build !linux

package workspaceagent

import (
	"errors"
	"os/exec"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func configureProcessIdentity(_ *exec.Cmd, _ workspacev1.CommandSpec, config processIdentityConfig) (string, string, func(), error) {
	closeIdentity := func() {}
	if config.Required {
		return "", "", closeIdentity, errors.New("workspace command identity separation requires Linux")
	}
	return "", "", closeIdentity, nil
}
