//go:build linux

package workspaceagent

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// HardenVerifiedSubcommand seals the command-scoped identity FDs before any
// Git/OpenTofu child can start. The verified process may use them, but its
// same-UID descendants cannot inherit or ptrace the key-bearing process.
func HardenVerifiedSubcommand() error {
	credentials := len(os.Args) >= 2 && (os.Args[1] == "verified-git-commit" || os.Args[1] == "verified-tofu-plan")
	return hardenVerifiedSubcommand(credentials)
}

func hardenVerifiedSubcommand(credentials bool) error {
	if os.Getenv("WORKSPACE_AGENT_CONFIG_FILE") != "/proc/self/fd/3" || os.Geteuid() == 0 {
		return errors.New("verified workspace identity handoff is unavailable")
	}
	lastDescriptor := 3
	if credentials {
		lastDescriptor = 6
	}
	for descriptor := 3; descriptor <= lastDescriptor; descriptor++ {
		if _, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0); err != nil {
			return errors.New("verified workspace identity descriptor is unavailable")
		}
		syscall.CloseOnExec(descriptor)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return errors.New("disable verified workspace process dumping")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return errors.New("disable verified workspace privilege gain")
	}
	return nil
}
