//go:build linux

package workspaceagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceAgent_IdentityIsDelegatedOnlyThroughHardenedFDs(t *testing.T) {
	directory := t.TempDir()
	paths := make([]string, 3)
	for index, name := range []string{"agent.crt", "agent.key", "ca.crt"} {
		paths[index] = filepath.Join(directory, name)
		if err := os.WriteFile(paths[index], []byte("identity-material-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{
		ControlPlaneURL: "https://workspace.example.com", EgressGatewayURL: "https://egress.example.com:8443",
		WorkspaceID: "workspace-1", CorrelationID: "correlation-1", CertificateFile: paths[0], PrivateKeyFile: paths[1], CAFile: paths[2],
		JournalDirectory: "/state/journal", WorkspaceRoot: "/workspace",
	}
	configPath, files, err := inheritedIdentityFiles(config, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(files)
	if configPath != "/proc/self/fd/3" || len(files) != 4 {
		t.Fatalf("config path=%q files=%d", configPath, len(files))
	}
	var delegated Config
	if err := json.NewDecoder(files[0]).Decode(&delegated); err != nil {
		t.Fatal(err)
	}
	if delegated.CertificateFile != "/proc/self/fd/4" || delegated.PrivateKeyFile != "/proc/self/fd/5" || delegated.CAFile != "/proc/self/fd/6" || config.PrivateKeyFile != paths[1] {
		t.Fatalf("delegated=%#v original=%#v", delegated, config)
	}
	configOnlyPath, configOnlyFiles, err := inheritedIdentityFiles(config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(configOnlyFiles)
	if configOnlyPath != "/proc/self/fd/3" || len(configOnlyFiles) != 1 {
		t.Fatalf("config-only path=%q files=%d", configOnlyPath, len(configOnlyFiles))
	}

	if os.Getenv("WORKSPACE_IDENTITY_FD_HELPER") == "1" {
		if err := hardenVerifiedSubcommand(true); err != nil {
			t.Fatal(err)
		}
		for descriptor := 3; descriptor <= 6; descriptor++ {
			flags, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC == 0 {
				t.Fatalf("descriptor %d flags=%d err=%v", descriptor, flags, err)
			}
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceAgent_IdentityIsDelegatedOnlyThroughHardenedFDs$")
	command.Env = append(os.Environ(), "WORKSPACE_IDENTITY_FD_HELPER=1", "WORKSPACE_AGENT_CONFIG_FILE=/proc/self/fd/3")
	command.ExtraFiles = files
	if os.Geteuid() == 0 {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, NoSetGroups: true}}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("harden delegated identity descriptors: %v: %s", err, output)
	}
}
