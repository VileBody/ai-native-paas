package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func NormalizeReleaseConfig(input runtimev1.ReleaseConfig) (runtimev1.ReleaseConfig, error) {
	out := input
	out.Region = strings.ToLower(strings.TrimSpace(out.Region))
	out.Unit = strings.ToLower(strings.TrimSpace(out.Unit))
	out.GeneratedHostname = strings.ToLower(strings.TrimSpace(out.GeneratedHostname))
	out.AttachmentSnapshotRef = strings.TrimSpace(out.AttachmentSnapshotRef)
	out.EgressProfile = strings.ToLower(strings.TrimSpace(out.EgressProfile))
	if out.AttachmentSnapshotRef == "" {
		out.AttachmentSnapshotRef = "none"
	}
	if out.EgressProfile == "" {
		out.EgressProfile = "public-default"
	}
	if out.RolloutTimeoutSeconds == 0 {
		out.RolloutTimeoutSeconds = 300
	}
	if out.Region == "" || !runtimev1.ValidDNSLabel(out.Region) || !runtimev1.ValidDNSLabel(out.Unit) {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "region and runtime unit must be DNS labels")
	}
	if out.Isolation != runtimev1.IsolationSandboxed && out.Isolation != runtimev1.IsolationDedicated {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "unsupported isolation class")
	}
	if len(out.Processes) == 0 || len(out.Processes) > 32 {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "at least one process is required")
	}
	processes := make(map[string]runtimev1.ProcessSpec, len(out.Processes))
	names := make([]string, 0, len(out.Processes))
	for name := range out.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := out.Processes[name]
		name = strings.ToLower(strings.TrimSpace(name))
		if !runtimev1.ValidDNSLabel(name) || p.MinReplicas < 0 || p.MaxReplicas < p.MinReplicas || p.MaxReplicas > 1000 || (p.MaxReplicas > p.MinReplicas && p.MinReplicas == 0) {
			return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "invalid process configuration")
		}
		if p.Port < 0 || p.Port > 65535 {
			return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "invalid process port")
		}
		if p.HealthPath != "" && (!strings.HasPrefix(p.HealthPath, "/") || strings.ContainsAny(p.HealthPath, "\r\n\x00")) {
			return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "invalid process health path")
		}
		if p.StartupTimeout == 0 {
			p.StartupTimeout = 60
		}
		if p.ReadinessTimeout == 0 {
			p.ReadinessTimeout = 30
		}
		p.Command = append([]string(nil), p.Command...)
		processes[name] = p
	}
	if _, ok := runtimev1.SelectRouteProcess(processes); !ok {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "release has no routable process")
	}
	out.Processes = processes
	out.Migration.Command = append([]string(nil), out.Migration.Command...)
	if len(out.Migration.Command) > 0 && out.Migration.TimeoutSeconds == 0 {
		out.Migration.TimeoutSeconds = 600
	}
	if out.RolloutTimeoutSeconds <= 0 || out.RolloutTimeoutSeconds > 86400 {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "invalid rollout timeout")
	}
	if out.Migration.TimeoutSeconds < 0 || out.Migration.TimeoutSeconds > 86400 {
		return runtimev1.ReleaseConfig{}, NewError(CodeInvalidArgument, "invalid migration timeout")
	}
	return out, nil
}

func ReleaseIdentity(environmentID string, artifact buildv1.ArtifactRef, config runtimev1.ReleaseConfig) (string, error) {
	environmentID = strings.TrimSpace(environmentID)
	if !runtimev1.ValidPlatformID(environmentID) || artifact.Validate() != nil {
		return "", NewError(CodeInvalidArgument, "release identity input is invalid")
	}
	normalized, err := NormalizeReleaseConfig(config)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		EnvironmentID string                  `json:"environment_id"`
		Artifact      buildv1.ArtifactRef     `json:"artifact"`
		Configuration runtimev1.ReleaseConfig `json:"configuration"`
	}{environmentID, artifact, normalized})
	if err != nil {
		return "", Wrap(CodeInternal, "encode release identity", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
