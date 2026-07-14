package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"gopkg.in/yaml.v3"
)

const (
	agentConfigPath    = "/var/lib/ai-native-paas/identity/workspace-agent.json"
	agentCertPath      = "/var/lib/ai-native-paas/identity/workspace-agent.crt"
	agentKeyPath       = "/var/lib/ai-native-paas/identity/workspace-agent.key"
	agentCAPath        = "/var/lib/ai-native-paas/identity/workspace-ca.crt"
	agentUnit          = "ai-native-paas-workspace-agent.service"
	agentJournalPath   = "/var/lib/ai-native-paas/journal"
	agentWorkspaceRoot = "/workspace"
)

type CloudInitRenderer struct {
	Issuer           CertificateIssuer
	ControlPlaneURL  string
	MaximumByteCount int
}

type agentConfig struct {
	ControlPlaneURL  string `json:"control_plane_url"`
	WorkspaceID      string `json:"workspace_id"`
	CorrelationID    string `json:"correlation_id"`
	CertificateFile  string `json:"certificate_file"`
	PrivateKeyFile   string `json:"private_key_file"`
	CAFile           string `json:"ca_file"`
	JournalDirectory string `json:"journal_directory"`
	WorkspaceRoot    string `json:"workspace_root"`
}

type cloudInitFile struct {
	Path        string `yaml:"path"`
	Owner       string `yaml:"owner"`
	Permissions string `yaml:"permissions"`
	Encoding    string `yaml:"encoding"`
	Content     string `yaml:"content"`
}

type cloudInitDocument struct {
	Users       []string        `yaml:"users"`
	SSHPassword bool            `yaml:"ssh_pwauth"`
	DisableRoot bool            `yaml:"disable_root"`
	WriteFiles  []cloudInitFile `yaml:"write_files"`
	RunCommands [][]string      `yaml:"runcmd"`
}

func (r CloudInitRenderer) Render(ctx context.Context, request workspace.ProviderCreateRequest) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(r.ControlPlaneURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", errors.New("workspace agent control-plane URL is invalid")
	}
	if r.Issuer == nil || strings.TrimSpace(request.CorrelationID) == "" {
		return "", errors.New("workspace bootstrap dependencies are unavailable")
	}
	identity := Identity{TenantID: request.TenantID, ProjectID: request.ProjectID, WorkspaceID: request.WorkspaceID, TaskID: request.TaskID, AgentID: request.AgentID}
	if err := identity.Validate(); err != nil {
		return "", err
	}
	bundle, err := r.Issuer.Issue(ctx, identity)
	if err != nil {
		return "", err
	}
	defer bundle.Clear()
	config, err := json.Marshal(agentConfig{
		ControlPlaneURL: strings.TrimRight(endpoint.String(), "/"), WorkspaceID: request.WorkspaceID, CorrelationID: request.CorrelationID,
		CertificateFile: agentCertPath, PrivateKeyFile: agentKeyPath, CAFile: agentCAPath,
		JournalDirectory: agentJournalPath, WorkspaceRoot: agentWorkspaceRoot,
	})
	if err != nil {
		return "", err
	}
	document := cloudInitDocument{
		Users: []string{}, SSHPassword: false, DisableRoot: true,
		WriteFiles: []cloudInitFile{
			encodedFile(agentConfigPath, "0400", config),
			encodedFile(agentCertPath, "0400", bundle.Certificate),
			encodedFile(agentKeyPath, "0400", bundle.PrivateKey),
			encodedFile(agentCAPath, "0400", bundle.CAChain),
		},
		RunCommands: [][]string{{"systemctl", "enable", "--now", agentUnit}},
	}
	raw, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	rendered := "#cloud-config\n" + string(raw)
	limit := r.MaximumByteCount
	if limit <= 0 || limit > 64<<10 {
		limit = 64 << 10
	}
	if len(rendered) > limit {
		return "", errors.New("workspace cloud-init exceeds provider limit")
	}
	return rendered, nil
}

func encodedFile(path, permissions string, content []byte) cloudInitFile {
	return cloudInitFile{Path: path, Owner: "workspace-agent:workspace-agent", Permissions: permissions, Encoding: "b64", Content: base64.StdEncoding.EncodeToString(content)}
}
