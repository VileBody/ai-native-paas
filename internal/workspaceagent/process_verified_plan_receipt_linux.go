//go:build linux

package workspaceagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func ExecuteVerifiedTofuPlan(arguments []string) error {
	planPath, expectedSourceSHA, err := validateVerifiedPlanArguments(arguments)
	if err != nil {
		return err
	}
	if err := verifyWorkspaceSource(expectedSourceSHA, planPath); err != nil {
		return err
	}
	configFile := os.Getenv("WORKSPACE_AGENT_CONFIG_FILE")
	if configFile == "" {
		configFile = "/var/lib/ai-native-paas/identity/workspace-agent.json"
	}
	config, err := LoadConfig(configFile)
	if err != nil {
		return err
	}
	client, err := NewClient(config)
	if err != nil {
		return err
	}
	commandID := os.Getenv("PLATFORM_COMMAND_ID")
	sessionID := os.Getenv("PLATFORM_EXECUTION_SESSION_ID")
	if !identityPattern.MatchString(commandID) || !identityPattern.MatchString(sessionID) {
		return errors.New("verified plan command binding is unavailable")
	}
	tofu, err := exec.LookPath("tofu")
	if err != nil {
		return errors.New("OpenTofu executable is unavailable")
	}
	plan := exec.Command(tofu, "plan", "-input=false", "-no-color", "-out="+planPath)
	plan.Stdout, plan.Stderr = os.Stdout, os.Stderr
	if err := plan.Run(); err != nil {
		return errors.New("OpenTofu plan failed")
	}
	fd, err := syscall.Open(planPath, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("open generated plan artifact")
	}
	file := os.NewFile(uintptr(fd), "generated-tofu-plan")
	if file == nil {
		_ = syscall.Close(fd)
		return errors.New("open generated plan artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 8<<30 {
		return errors.New("generated plan artifact is not a bounded regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return errors.New("hash generated plan artifact")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errors.New("rewind generated plan artifact")
	}
	show := exec.Command(tofu, "show", "-json", "/proc/self/fd/3")
	show.ExtraFiles = []*os.File{file}
	show.Stderr = os.Stderr
	stdout, err := show.StdoutPipe()
	if err != nil {
		return errors.New("prepare OpenTofu plan inspection")
	}
	if err := show.Start(); err != nil {
		return errors.New("start OpenTofu plan inspection")
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdout, (32<<20)+1))
	waitErr := show.Wait()
	if readErr != nil || waitErr != nil || len(raw) > 32<<20 {
		return errors.New("inspect OpenTofu plan artifact")
	}
	normalized, err := normalizePlanReceipt(raw)
	for index := range raw {
		raw[index] = 0
	}
	if err != nil {
		return err
	}
	receipt := infrastructurev1.AgentPlanReceipt{
		SessionID: sessionID, ExecutionSessionID: sessionID, CommandID: commandID,
		ArtifactDigest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), PlanJSON: normalized, CapturedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := client.SubmitPlanReceipt(ctx, receipt); err != nil {
		return fmt.Errorf("submit authenticated OpenTofu plan receipt: %w", err)
	}
	return nil
}

func verifyWorkspaceSource(expected, planPath string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return errors.New("Git executable is unavailable")
	}
	head := exec.Command(git, "rev-parse", "--verify", "HEAD^{commit}")
	rawHead, err := head.Output()
	actual := strings.TrimSpace(string(rawHead))
	if err != nil || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return errors.New("workspace source revision does not match command binding")
	}
	trackedPlan := exec.Command(git, "ls-files", "--error-unmatch", "--", planPath)
	if err := trackedPlan.Run(); err == nil {
		return errors.New("OpenTofu plan artifact path must not be tracked by Git")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		return errors.New("inspect OpenTofu plan artifact path")
	}
	status := exec.Command(git, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	rawStatus, err := status.Output()
	if err != nil || !onlyUntrackedPlan(rawStatus, planPath) {
		return errors.New("workspace source tree is not clean")
	}
	return nil
}

func onlyUntrackedPlan(raw []byte, planPath string) bool {
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 || string(entry[:2]) != "??" || string(entry[3:]) != planPath {
			return false
		}
	}
	return true
}
