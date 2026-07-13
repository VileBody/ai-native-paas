package application

import (
	"encoding/json"
	"errors"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	"regexp"
	"strings"
)

type PatchFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type CreateProjectArguments struct {
	Name string `json:"name"`
}
type GetProjectArguments struct {
	ProjectID string `json:"project_id"`
}
type ApplyPatchArguments struct {
	ProjectID     string      `json:"project_id"`
	BaseCommitSHA string      `json:"base_commit_sha"`
	Branch        string      `json:"branch"`
	Message       string      `json:"message"`
	Files         []PatchFile `json:"files"`
}
type CreateBranchArguments struct {
	ProjectID     string `json:"project_id"`
	Branch        string `json:"branch"`
	BaseCommitSHA string `json:"base_commit_sha"`
}
type CreateMergeRequestArguments struct {
	ProjectID    string `json:"project_id"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
}
type RequestBuildArguments struct {
	Revision struct {
		ProjectID    string `json:"project_id"`
		RepositoryID string `json:"repository_id"`
		Branch       string `json:"branch"`
		CommitSHA    string `json:"commit_sha"`
		SourceRoot   string `json:"source_root,omitempty"`
	} `json:"revision"`
	EstimatedMinutes int64 `json:"estimated_minutes,omitempty"`
}
type GetBuildArguments struct {
	BuildID string `json:"build_id"`
}
type DeployArguments struct {
	BuildID                     string                  `json:"build_id"`
	ApplicationID               string                  `json:"application_id"`
	EnvironmentID               string                  `json:"environment_id"`
	EnvironmentName             string                  `json:"environment_name"`
	ExpectedEnvironmentRevision int64                   `json:"expected_environment_revision"`
	Configuration               runtimev1.ReleaseConfig `json:"configuration"`
}
type GetDeploymentArguments struct {
	DeploymentID string `json:"deployment_id"`
}
type RollbackArguments struct {
	DeploymentID                string `json:"deployment_id"`
	TargetReleaseID             string `json:"target_release_id"`
	ExpectedEnvironmentRevision int64  `json:"expected_environment_revision"`
}
type SetSecretArguments struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	Value         string `json:"value"`
}
type ListSecretMetadataArguments struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
}
type ProvisionServiceArguments struct {
	ServiceType string `json:"service_type"`
	Plan        string `json:"plan"`
	Name        string `json:"name"`
}
type BindServiceArguments struct {
	ServiceInstanceID string `json:"service_instance_id"`
	ApplicationID     string `json:"application_id"`
	EnvironmentID     string `json:"environment_id"`
}
type AddDomainArguments struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	Hostname      string `json:"hostname"`
}
type GetLogsArguments struct {
	ApplicationID string `json:"application_id"`
	Limit         int    `json:"limit"`
}
type GetUsageArguments struct {
	PeriodID string `json:"period_id"`
}
type RequestApprovalArguments struct {
	Action     agentv1.ApprovalAction   `json:"action"`
	Resource   agentv1.ApprovalResource `json:"resource"`
	Payload    json.RawMessage          `json:"payload"`
	TTLSeconds int64                    `json:"ttl_seconds"`
}
type GetOperationArguments struct {
	OperationID string `json:"operation_id"`
}
type CancelOperationArguments struct {
	OperationID string `json:"operation_id"`
}

var secretNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func validateToolArguments(tool agentv1.Tool, raw json.RawMessage) error {
	valid := func(v string) bool { return agentv1.ValidID(strings.TrimSpace(v)) }
	switch tool {
	case agentv1.ToolCreateProject:
		var a CreateProjectArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if len(strings.TrimSpace(a.Name)) < 1 || len(a.Name) > 100 {
			return errors.New("invalid project name")
		}
	case agentv1.ToolGetProject:
		var a GetProjectArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ProjectID) {
			return errors.New("invalid project id")
		}
	case agentv1.ToolApplyRepositoryPatch:
		var a ApplyPatchArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ProjectID) || !validGitBranch(a.Branch) || !validCommitSHA(a.BaseCommitSHA) || len(a.Files) < 1 || len(a.Files) > 1000 || len(strings.TrimSpace(a.Message)) < 1 || len(a.Message) > 500 {
			return errors.New("invalid patch")
		}
		for _, f := range a.Files {
			if err := validatePatchPath(f.Path); err != nil {
				return err
			}
			if len(f.Content) > 1<<20 {
				return errors.New("patch file too large")
			}
		}
	case agentv1.ToolCreateBranch:
		var a CreateBranchArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ProjectID) || !validGitBranch(a.Branch) || !validCommitSHA(a.BaseCommitSHA) {
			return errors.New("invalid branch")
		}
	case agentv1.ToolCreateMergeRequest:
		var a CreateMergeRequestArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ProjectID) || !validGitBranch(a.SourceBranch) || !validGitBranch(a.TargetBranch) || len(strings.TrimSpace(a.Title)) < 1 || len(a.Title) > 500 {
			return errors.New("invalid merge request")
		}
	case agentv1.ToolRequestBuild:
		var a RequestBuildArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.Revision.ProjectID) || !valid(a.Revision.RepositoryID) || !validGitBranch(a.Revision.Branch) || !validCommitSHA(a.Revision.CommitSHA) || !validSourceRoot(a.Revision.SourceRoot) || a.EstimatedMinutes < 0 || a.EstimatedMinutes > 1440 {
			return errors.New("invalid build request")
		}
	case agentv1.ToolGetBuild:
		var a GetBuildArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.BuildID) {
			return errors.New("invalid build id")
		}
	case agentv1.ToolDeploy:
		var a DeployArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.BuildID) || !valid(a.ApplicationID) || !valid(a.EnvironmentID) || !valid(a.EnvironmentName) || a.ExpectedEnvironmentRevision < 0 || validateReleaseConfig(a.Configuration) != nil {
			return errors.New("invalid deploy request")
		}
	case agentv1.ToolGetDeployment:
		var a GetDeploymentArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.DeploymentID) {
			return errors.New("invalid deployment id")
		}
	case agentv1.ToolRollback:
		var a RollbackArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.DeploymentID) || !valid(a.TargetReleaseID) || a.ExpectedEnvironmentRevision < 0 {
			return errors.New("invalid rollback")
		}
	case agentv1.ToolSetSecret:
		var a SetSecretArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ApplicationID) || !valid(a.EnvironmentID) || !secretNamePattern.MatchString(a.Name) || len(a.Value) < 1 || len(a.Value) > 65536 {
			return errors.New("invalid secret")
		}
	case agentv1.ToolListSecretMetadata:
		var a ListSecretMetadataArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ApplicationID) || !valid(a.EnvironmentID) {
			return errors.New("invalid secret scope")
		}
	case agentv1.ToolProvisionService:
		var a ProvisionServiceArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ServiceType) || !valid(a.Plan) || !valid(a.Name) {
			return errors.New("invalid service request")
		}
	case agentv1.ToolBindService:
		var a BindServiceArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ServiceInstanceID) || !valid(a.ApplicationID) || !valid(a.EnvironmentID) {
			return errors.New("invalid binding request")
		}
	case agentv1.ToolAddDomain:
		var a AddDomainArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ApplicationID) || !valid(a.EnvironmentID) || !validHostname(a.Hostname) {
			return errors.New("invalid domain")
		}
	case agentv1.ToolGetLogs:
		var a GetLogsArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.ApplicationID) || a.Limit < 1 || a.Limit > 10000 {
			return errors.New("invalid log request")
		}
	case agentv1.ToolGetUsage:
		var a GetUsageArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.PeriodID) {
			return errors.New("invalid period")
		}
	case agentv1.ToolRequestApproval:
		var a RequestApprovalArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !agentv1.ValidApprovalAction(a.Action) || a.Resource.Validate() != nil || len(a.Payload) == 0 || !json.Valid(a.Payload) || len(a.Payload) > 1<<20 || a.TTLSeconds < 1 || a.TTLSeconds > 86400 {
			return errors.New("invalid approval request")
		}
	case agentv1.ToolGetOperation, agentv1.ToolCancelOperation:
		var a GetOperationArguments
		if err := agentv1.DecodeStrict(raw, &a); err != nil {
			return err
		}
		if !valid(a.OperationID) {
			return errors.New("invalid operation id")
		}
	default:
		return errors.New("unsupported tool")
	}
	return nil
}
func validatePatchPath(v string) error {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\\", "/")
	if v == "" || strings.HasPrefix(v, "/") || v == ".git" || strings.HasPrefix(v, ".git/") || strings.Contains(v, "\x00") {
		return errors.New("unsafe patch path")
	}
	depth := 0
	for _, p := range strings.Split(v, "/") {
		switch p {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return errors.New("path escape")
			}
		default:
			depth++
		}
	}
	if depth < 1 {
		return errors.New("unsafe patch path")
	}
	return nil
}
func validCommitSHA(v string) bool {
	if len(v) < 7 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
func validGitBranch(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 255 || v == "HEAD" || strings.HasPrefix(v, "-") || strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") || strings.HasSuffix(v, ".") || strings.Contains(v, "..") || strings.Contains(v, "@{") || strings.Contains(v, "//") || strings.HasSuffix(v, ".lock") {
		return false
	}
	for _, c := range v {
		if c <= 0x20 || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, p := range strings.Split(v, "/") {
		if p == "" || strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") {
			return false
		}
	}
	return true
}
func validSourceRoot(v string) bool {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\\", "/")
	if v == "" || v == "." {
		return true
	}
	if len(v) > 4096 || strings.HasPrefix(v, "/") || strings.Contains(v, "\x00") {
		return false
	}
	d := 0
	for _, p := range strings.Split(v, "/") {
		switch p {
		case "", ".":
		case "..":
			d--
			if d < 0 {
				return false
			}
		default:
			d++
		}
	}
	return d > 0
}
func validateReleaseConfig(c runtimev1.ReleaseConfig) error {
	if !runtimev1.ValidDNSLabel(strings.ToLower(strings.TrimSpace(c.Region))) || !runtimev1.ValidDNSLabel(strings.ToLower(strings.TrimSpace(c.Unit))) {
		return errors.New("invalid placement")
	}
	if c.Isolation != runtimev1.IsolationSandboxed && c.Isolation != runtimev1.IsolationDedicated {
		return errors.New("invalid isolation")
	}
	if len(c.Processes) < 1 || len(c.Processes) > 32 {
		return errors.New("invalid processes")
	}
	routable := false
	for n, p := range c.Processes {
		if !runtimev1.ValidDNSLabel(strings.ToLower(strings.TrimSpace(n))) || p.MinReplicas < 0 || p.MaxReplicas < p.MinReplicas || p.MaxReplicas > 1000 || (p.MaxReplicas > p.MinReplicas && p.MinReplicas == 0) || p.Port < 0 || p.Port > 65535 || len(p.Command) > 128 || p.StartupTimeout < 0 || p.StartupTimeout > 86400 || p.ReadinessTimeout < 0 || p.ReadinessTimeout > 86400 {
			return errors.New("invalid process")
		}
		for _, a := range p.Command {
			if len(a) > 4096 || strings.ContainsRune(a, '\x00') {
				return errors.New("invalid command")
			}
		}
		if p.HealthPath != "" && (!strings.HasPrefix(p.HealthPath, "/") || strings.ContainsAny(p.HealthPath, "\r\n\x00")) {
			return errors.New("invalid health")
		}
		if p.Port > 0 && p.MinReplicas >= 1 {
			routable = true
		}
	}
	if !routable {
		return errors.New("no routable process")
	}
	if c.GeneratedHostname != "" && !validHostname(c.GeneratedHostname) {
		return errors.New("invalid hostname")
	}
	if c.AttachmentSnapshotRef != "" && !agentv1.ValidID(c.AttachmentSnapshotRef) {
		return errors.New("invalid snapshot")
	}
	if c.RolloutTimeoutSeconds < 0 || c.RolloutTimeoutSeconds > 86400 || c.Migration.TimeoutSeconds < 0 || c.Migration.TimeoutSeconds > 86400 || len(c.Migration.Command) > 128 {
		return errors.New("invalid timeout")
	}
	if c.EgressProfile != "" && c.EgressProfile != "public-default" && c.EgressProfile != "deny-all" {
		return errors.New("invalid egress")
	}
	return nil
}
func validHostname(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) < 3 || len(v) > 253 || strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") {
		return false
	}
	for _, p := range strings.Split(v, ".") {
		if !runtimev1.ValidDNSLabel(p) {
			return false
		}
	}
	return true
}
