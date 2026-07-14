//go:build linux

package workspaceagent

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func configureProcessIdentity(command *exec.Cmd, spec workspacev1.CommandSpec, config processIdentityConfig) (string, string, func(), error) {
	closeIdentity := func() {}
	if !config.Required {
		return "", "", closeIdentity, nil
	}
	identity, err := selectProcessIdentity(spec, config)
	if err != nil {
		return "", "", closeIdentity, err
	}
	if command == nil || command.SysProcAttr == nil || os.Geteuid() != 0 {
		return "", "", closeIdentity, errors.New("workspace command identity separation is unavailable")
	}
	// NoSetGroups intentionally preserves only the supervisor's supplementary
	// workspace-shared group. Its primary identity-reader group is replaced by
	// Gid below, so command processes cannot open the agent identity directory.
	command.SysProcAttr.Credential = &syscall.Credential{Uid: identity.UID, Gid: identity.GID, NoSetGroups: true}
	if !identity.AttachIdentity {
		return identity.Home, "", closeIdentity, nil
	}
	configPath, files, err := inheritedIdentityFiles(config.IdentityConfig, identity.AttachCredentials)
	if err != nil || len(command.ExtraFiles) != 0 {
		closeFiles(files)
		return "", "", closeIdentity, errors.New("workspace agent identity handoff is unavailable")
	}
	command.ExtraFiles = files
	closeIdentity = func() { closeFiles(files) }
	return identity.Home, configPath, closeIdentity, nil
}

func inheritedIdentityFiles(config Config, credentials bool) (string, []*os.File, error) {
	if !credentials {
		configFile, err := delegatedConfigFile(config)
		if err != nil {
			return "", nil, err
		}
		return "/proc/self/fd/3", []*os.File{configFile}, nil
	}
	certificate, err := openIdentityFile(config.CertificateFile)
	if err != nil {
		return "", nil, err
	}
	privateKey, err := openIdentityFile(config.PrivateKeyFile)
	if err != nil {
		_ = certificate.Close()
		return "", nil, err
	}
	ca, err := openIdentityFile(config.CAFile)
	if err != nil {
		_ = certificate.Close()
		_ = privateKey.Close()
		return "", nil, err
	}
	files := []*os.File{nil, certificate, privateKey, ca}
	delegated := config
	delegated.CertificateFile = "/proc/self/fd/4"
	delegated.PrivateKeyFile = "/proc/self/fd/5"
	delegated.CAFile = "/proc/self/fd/6"
	configFile, err := delegatedConfigFile(delegated)
	if err != nil {
		closeFiles(files[1:])
		return "", nil, err
	}
	files[0] = configFile
	return "/proc/self/fd/3", files, nil
}

func delegatedConfigFile(config Config) (*os.File, error) {
	configFile, err := os.CreateTemp("", ".workspace-agent-delegated-config-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(configFile.Name())
	if err := configFile.Chmod(0o600); err == nil {
		err = json.NewEncoder(configFile).Encode(config)
	}
	if err == nil {
		err = configFile.Sync()
	}
	if err == nil {
		_, err = configFile.Seek(0, 0)
	}
	if err != nil {
		_ = configFile.Close()
		return nil, err
	}
	return configFile, nil
}

func openIdentityFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("workspace identity path is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o037 != 0 || info.Size() <= 0 || info.Size() > 256<<10 {
		_ = file.Close()
		return nil, errors.New("workspace identity file is unsafe")
	}
	return file, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
