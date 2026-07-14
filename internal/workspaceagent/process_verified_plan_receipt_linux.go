//go:build linux

package workspaceagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func ExecuteVerifiedTofuPlan(arguments []string) error {
	if len(arguments) != 1 {
		return errors.New("canonical relative plan path is required")
	}
	planPath, err := validateReceiptPlanPath(arguments[0])
	if err != nil {
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
