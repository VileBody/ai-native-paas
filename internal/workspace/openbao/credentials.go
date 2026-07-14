package openbao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

const maximumCredentialTTL = time.Hour

var (
	credentialScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	credentialEnvPattern   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	credentialRefPattern   = regexp.MustCompile(`^(credential|secret|input|state|registry)://[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

type CredentialConfig struct {
	Address    string
	TokenFile  string
	Mount      string
	MaximumTTL time.Duration
	HTTPClient *http.Client
	Clock      workspace.Clock
}

type CredentialSource struct {
	base       *url.URL
	tokenFile  string
	mount      string
	maximumTTL time.Duration
	client     *http.Client
	clock      workspace.Clock
}

func NewCredentialSource(config CredentialConfig) (*CredentialSource, error) {
	base, err := url.Parse(strings.TrimSpace(config.Address))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, errors.New("OpenBao address is invalid")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())) {
		return nil, errors.New("OpenBao address requires HTTPS")
	}
	if strings.TrimSpace(config.TokenFile) == "" || !namePattern.MatchString(config.Mount) || config.Clock == nil {
		return nil, errors.New("OpenBao credential source configuration is incomplete")
	}
	maximumTTL := config.MaximumTTL
	if maximumTTL < time.Minute || maximumTTL > maximumCredentialTTL {
		return nil, errors.New("OpenBao credential maximum TTL is invalid")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &CredentialSource{base: base, tokenFile: config.TokenFile, mount: config.Mount, maximumTTL: maximumTTL, client: client, clock: config.Clock}, nil
}

func (s *CredentialSource) Resolve(ctx context.Context, request workspace.CredentialSourceRequest) (workspacev1.AgentCredentialView, error) {
	if s == nil || s.base == nil || s.client == nil || s.clock == nil || len(request.EnvironmentRefs) == 0 || len(request.EnvironmentRefs) > 128 {
		return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential request is invalid")
	}
	for _, value := range []string{request.TenantID, request.ProjectID, request.WorkspaceID, request.TaskID, request.CommandID, request.AgentSessionID, request.VMID} {
		if !credentialScopePattern.MatchString(value) {
			return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential scope is invalid")
		}
	}
	leases := make(map[string]bool, len(request.CredentialLeases))
	for _, lease := range request.CredentialLeases {
		if !credentialRefPattern.MatchString("credential://" + lease) {
			return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential lease is invalid")
		}
		leases[lease] = false
	}
	token, err := readToken(s.tokenFile)
	if err != nil {
		return workspacev1.AgentCredentialView{}, err
	}
	defer clear(token)
	now := s.clock.Now().UTC()
	view := workspacev1.AgentCredentialView{Values: make(map[string]string, len(request.EnvironmentRefs))}
	total := 0
	for name, reference := range request.EnvironmentRefs {
		if !credentialEnvPattern.MatchString(name) || !credentialRefPattern.MatchString(reference) {
			return workspacev1.AgentCredentialView{}, errors.New("OpenBao environment reference is invalid")
		}
		if lease, ok := strings.CutPrefix(reference, "credential://"); ok {
			if _, allowed := leases[lease]; !allowed {
				return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential reference is not command-scoped")
			}
			leases[lease] = true
		}
		value, expiresAt, err := s.read(ctx, token, request, reference)
		if err != nil {
			return workspacev1.AgentCredentialView{}, err
		}
		total += len(value)
		if total > 256<<10 {
			return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential response exceeds limit")
		}
		view.Values[name] = value
		if view.ExpiresAt.IsZero() || expiresAt.Before(view.ExpiresAt) {
			view.ExpiresAt = expiresAt
		}
	}
	for _, used := range leases {
		if !used {
			return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential lease is unused")
		}
	}
	if !view.ExpiresAt.After(now.Add(30*time.Second)) || view.ExpiresAt.After(now.Add(s.maximumTTL)) {
		return workspacev1.AgentCredentialView{}, errors.New("OpenBao credential expiry is invalid")
	}
	return view, nil
}

func (s *CredentialSource) read(ctx context.Context, token []byte, request workspace.CredentialSourceRequest, reference string) (string, time.Time, error) {
	hash := sha256.Sum256([]byte(reference))
	endpoint := *s.base
	endpoint.Path = path.Join("/v1", s.mount, "data", request.TenantID, request.ProjectID, request.WorkspaceID, hex.EncodeToString(hash[:]))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", time.Time{}, errors.New("create OpenBao credential request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+string(token))
	response, err := s.client.Do(httpRequest)
	if err != nil {
		return "", time.Time{}, errors.New("OpenBao credential request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return "", time.Time{}, fmt.Errorf("OpenBao credential lookup failed with status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(raw) == 0 || len(raw) > 128<<10 {
		return "", time.Time{}, errors.New("OpenBao credential response is invalid")
	}
	var envelope struct {
		Data struct {
			Data struct {
				Value       string    `json:"value"`
				ExpiresAt   time.Time `json:"expires_at"`
				TenantID    string    `json:"tenant_id"`
				ProjectID   string    `json:"project_id"`
				WorkspaceID string    `json:"workspace_id"`
				TaskID      string    `json:"task_id"`
				CommandID   string    `json:"command_id"`
				Reference   string    `json:"reference"`
			} `json:"data"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", time.Time{}, errors.New("OpenBao credential response is invalid")
	}
	value := envelope.Data.Data
	if value.Value == "" || len(value.Value) > 64<<10 || strings.ContainsRune(value.Value, '\x00') || value.TenantID != request.TenantID || value.ProjectID != request.ProjectID || value.WorkspaceID != request.WorkspaceID || value.TaskID != request.TaskID || value.CommandID != request.CommandID || value.Reference != reference {
		return "", time.Time{}, errors.New("OpenBao credential binding is invalid")
	}
	return value.Value, value.ExpiresAt.UTC(), nil
}

var _ workspace.CredentialSource = (*CredentialSource)(nil)
