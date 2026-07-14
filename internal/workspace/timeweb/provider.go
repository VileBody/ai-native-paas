// Package timeweb implements disposable workspace VMs through the Timeweb Cloud REST API.
package timeweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
)

const defaultBaseURL = "https://api.timeweb.cloud/api/v1"

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type CloudInitRenderer func(context.Context, workspace.ProviderCreateRequest) (string, error)

type Config struct {
	BaseURL            string
	Token              string
	ProjectID          int64
	ConfiguratorID     int64
	AvailabilityZone   string
	BandwidthMbps      int64
	SystemDiskMiB      int64
	ImageIDs           map[string]string
	EgressGatewayCIDRs []string
	DNSResolverCIDRs   []string
	HTTPClient         *http.Client
	RenderCloudInit    CloudInitRenderer
}

type Provider struct {
	baseURL            *url.URL
	token              string
	projectID          int64
	configuratorID     int64
	availabilityZone   string
	bandwidthMbps      int64
	systemDiskMiB      int64
	imageIDs           map[string]string
	egressGatewayCIDRs []string
	dnsResolverCIDRs   []string
	client             *http.Client
	renderCloudInit    CloudInitRenderer
}

func New(config Config) (*Provider, error) {
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid Timeweb API base URL")
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("Timeweb API requires HTTPS")
	}
	if strings.TrimSpace(config.Token) == "" || config.ProjectID <= 0 || config.ConfiguratorID <= 0 || strings.TrimSpace(config.AvailabilityZone) == "" || config.BandwidthMbps < 1 || config.SystemDiskMiB < 10240 || len(config.ImageIDs) == 0 || len(config.EgressGatewayCIDRs) == 0 || len(config.DNSResolverCIDRs) == 0 || config.RenderCloudInit == nil {
		return nil, errors.New("Timeweb workspace provider configuration is incomplete")
	}
	for digest, imageID := range config.ImageIDs {
		if !validDigest(digest) || !providerIDPattern.MatchString(strings.TrimSpace(imageID)) {
			return nil, errors.New("Timeweb workspace image mapping is invalid")
		}
	}
	if !singleHostCIDRs(config.EgressGatewayCIDRs) || !singleHostCIDRs(config.DNSResolverCIDRs) {
		return nil, errors.New("Timeweb workspace gateway and resolver policies require explicit host CIDRs")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Provider{
		baseURL: parsed, token: config.Token, projectID: config.ProjectID, configuratorID: config.ConfiguratorID,
		availabilityZone: config.AvailabilityZone, bandwidthMbps: config.BandwidthMbps, systemDiskMiB: config.SystemDiskMiB,
		imageIDs: cloneMap(config.ImageIDs), egressGatewayCIDRs: sortedCopy(config.EgressGatewayCIDRs),
		dnsResolverCIDRs: sortedCopy(config.DNSResolverCIDRs), client: client, renderCloudInit: config.RenderCloudInit,
	}, nil
}

type workspaceMetadata struct {
	WorkspaceID    string `json:"workspace_id"`
	CorrelationID  string `json:"correlation_id"`
	ImageDigest    string `json:"image_digest"`
	NetworkProfile string `json:"network_profile"`
}

type serverJSON struct {
	ID       opaqueID      `json:"id"`
	Name     string        `json:"name"`
	Comment  string        `json:"comment"`
	Disks    []diskJSON    `json:"disks"`
	Networks []networkJSON `json:"networks"`
}

type diskJSON struct {
	ID opaqueID `json:"id"`
}
type networkJSON struct {
	Type string `json:"type"`
	IPs  []struct {
		IP string `json:"ip"`
	} `json:"ips"`
}
type serverEnvelope struct {
	Server serverJSON `json:"server"`
}
type serversEnvelope struct {
	Servers []serverJSON `json:"servers"`
}

func (p *Provider) Create(ctx context.Context, request workspace.ProviderCreateRequest) (workspace.ProviderVM, error) {
	if err := p.validateCreate(request); err != nil {
		return workspace.ProviderVM{}, err
	}
	cloudInit, err := p.renderCloudInit(ctx, request)
	if err != nil {
		return workspace.ProviderVM{}, fmt.Errorf("render workspace cloud-init: %w", err)
	}
	if len(cloudInit) == 0 || len(cloudInit) > 64<<10 {
		return workspace.ProviderVM{}, errors.New("workspace cloud-init size is invalid")
	}
	metadata := workspaceMetadata{WorkspaceID: request.WorkspaceID, CorrelationID: request.CorrelationID, ImageDigest: request.ImageDigest, NetworkProfile: request.NetworkProfile}
	comment, err := encodeMetadata(metadata)
	if err != nil {
		return workspace.ProviderVM{}, err
	}
	payload := map[string]any{
		"name": nameFor(request.WorkspaceID), "comment": comment, "bandwidth": p.bandwidthMbps,
		"configuration": map[string]any{"configurator_id": p.configuratorID, "cpu": request.CPUMillis / 1000, "ram": request.MemoryMiB, "disk": p.systemDiskMiB},
		"image_id":      p.imageIDs[request.ImageDigest], "network": map[string]any{"id": request.NetworkIsolation.VPCID},
		"availability_zone": p.availabilityZone, "is_root_password_required": false, "ssh_keys_ids": []int{},
		"project_id": p.projectID, "cloud_init": cloudInit,
	}
	var response serverEnvelope
	if err := p.doJSON(ctx, http.MethodPost, "/servers", nil, payload, &response, http.StatusOK, http.StatusCreated, http.StatusAccepted); err != nil {
		return workspace.ProviderVM{}, err
	}
	if response.Server.ID.String() == "" {
		return workspace.ProviderVM{}, errors.New("Timeweb create response has no server ID")
	}
	if hasPublicIP(response.Server) {
		cause := errors.New("Timeweb unexpectedly assigned a public IP to workspace VM")
		return workspace.ProviderVM{}, errors.Join(cause, p.failClosedCleanup(context.WithoutCancel(ctx), response.Server.ID.String(), request.WorkspaceID))
	}
	group, err := p.ensureFirewall(ctx, response.Server.ID.String(), request.WorkspaceID)
	if err != nil {
		return workspace.ProviderVM{}, errors.Join(err, p.failClosedCleanup(context.WithoutCancel(ctx), response.Server.ID.String(), request.WorkspaceID))
	}
	return providerVM(response.Server, metadata, []string{group.ID.String()}), nil
}

func (p *Provider) FindByCorrelation(ctx context.Context, correlationID string) (workspace.ProviderVM, error) {
	if strings.TrimSpace(correlationID) == "" {
		return workspace.ProviderVM{}, workspace.ErrNotFound
	}
	query := url.Values{"limit": []string{"500"}, "offset": []string{"0"}}
	var response serversEnvelope
	if err := p.doJSON(ctx, http.MethodGet, "/servers", query, nil, &response, http.StatusOK); err != nil {
		return workspace.ProviderVM{}, err
	}
	type match struct {
		server   serverJSON
		metadata workspaceMetadata
	}
	var matches []match
	for _, server := range response.Servers {
		metadata, err := decodeMetadata(server.Comment)
		if err == nil && metadata.CorrelationID == correlationID {
			matches = append(matches, match{server: server, metadata: metadata})
		}
	}
	if len(matches) == 0 {
		return workspace.ProviderVM{}, workspace.ErrNotFound
	}
	if len(matches) != 1 {
		return workspace.ProviderVM{}, errors.New("multiple Timeweb servers share workspace correlation identity")
	}
	selected := matches[0]
	if hasPublicIP(selected.server) {
		cause := errors.New("discovered workspace VM has a forbidden public IP")
		return workspace.ProviderVM{}, errors.Join(cause, p.failClosedCleanup(context.WithoutCancel(ctx), selected.server.ID.String(), selected.metadata.WorkspaceID))
	}
	group, err := p.ensureFirewall(ctx, selected.server.ID.String(), selected.metadata.WorkspaceID)
	if err != nil {
		return workspace.ProviderVM{}, errors.Join(err, p.failClosedCleanup(context.WithoutCancel(ctx), selected.server.ID.String(), selected.metadata.WorkspaceID))
	}
	return providerVM(selected.server, selected.metadata, []string{group.ID.String()}), nil
}

func (p *Provider) Destroy(ctx context.Context, target workspace.ProviderVM) (workspace.DestroyEvidence, error) {
	if target.VMID == "" {
		discovered, err := p.FindByCorrelation(ctx, target.CorrelationID)
		if errors.Is(err, workspace.ErrNotFound) {
			if err := p.deleteFirewallGroups(ctx, target.FirewallGroupIDs); err != nil {
				return workspace.DestroyEvidence{}, err
			}
			return workspace.DestroyEvidence{VMAbsent: true, AbsentDiskIDs: append([]string(nil), target.DiskIDs...), AbsentFirewallGroupIDs: append([]string(nil), target.FirewallGroupIDs...)}, nil
		}
		if err != nil {
			return workspace.DestroyEvidence{}, err
		}
		target = discovered
	}
	groups, err := p.resourceFirewallGroups(ctx, target.VMID)
	if err != nil && !errors.Is(err, workspace.ErrNotFound) {
		return workspace.DestroyEvidence{}, err
	}
	if err := p.deleteServer(ctx, target.VMID); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		return workspace.DestroyEvidence{}, err
	}
	if _, err := p.getServer(ctx, target.VMID); err == nil {
		return workspace.DestroyEvidence{}, errors.New("Timeweb workspace deletion is still pending")
	} else if !errors.Is(err, workspace.ErrNotFound) {
		return workspace.DestroyEvidence{}, err
	}
	groupIDs := append([]string(nil), target.FirewallGroupIDs...)
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID.String())
	}
	groupIDs = uniqueStrings(groupIDs)
	if err := p.deleteFirewallGroups(ctx, groupIDs); err != nil {
		return workspace.DestroyEvidence{}, err
	}
	return workspace.DestroyEvidence{VMAbsent: true, AbsentDiskIDs: append([]string(nil), target.DiskIDs...), AbsentFirewallGroupIDs: groupIDs}, nil
}

func (p *Provider) deleteFirewallGroups(ctx context.Context, groupIDs []string) error {
	for _, groupID := range uniqueStrings(groupIDs) {
		if groupID == "" {
			continue
		}
		if err := p.doJSON(ctx, http.MethodDelete, "/firewall/groups/"+pathSegment(groupID), nil, nil, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			return err
		}
		if err := p.doJSON(ctx, http.MethodGet, "/firewall/groups/"+pathSegment(groupID), nil, nil, nil, http.StatusNotFound); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) failClosedCleanup(ctx context.Context, serverID, workspaceID string) error {
	var cleanupErrors []error
	var groupIDs []string
	groups, err := p.resourceFirewallGroups(ctx, serverID)
	if err == nil {
		for _, group := range groups {
			groupIDs = append(groupIDs, group.ID.String())
		}
	} else if !errors.Is(err, workspace.ErrNotFound) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("discover workspace firewall during cleanup: %w", err))
	}
	allGroups, err := p.listFirewallGroups(ctx)
	if err == nil {
		groupName := nameFor(workspaceID)
		for _, group := range allGroups {
			if group.Name == groupName {
				groupIDs = append(groupIDs, group.ID.String())
			}
		}
	} else {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("discover orphan workspace firewall during cleanup: %w", err))
	}
	if err := p.deleteServer(ctx, serverID); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete workspace server during cleanup: %w", err))
	}
	if err := p.deleteFirewallGroups(ctx, groupIDs); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete workspace firewall during cleanup: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func (p *Provider) validateCreate(request workspace.ProviderCreateRequest) error {
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.TaskID) == "" || strings.TrimSpace(request.AgentID) == "" || strings.TrimSpace(request.CorrelationID) == "" || request.CPUMillis < 1000 || request.CPUMillis%1000 != 0 || request.MemoryMiB < 1024 || request.NetworkProfile != "isolated-governed" {
		return errors.New("invalid Timeweb workspace request")
	}
	if _, ok := p.imageIDs[request.ImageDigest]; !ok || !request.NetworkIsolation.PrivateAddressOnly || !request.NetworkIsolation.DenyAllInbound || !request.NetworkIsolation.OutboundGatewayMTLS || strings.TrimSpace(request.NetworkIsolation.VPCID) == "" {
		return errors.New("Timeweb workspace request violates isolation policy")
	}
	if !sameStrings(request.NetworkIsolation.EgressGatewayCIDRs, p.egressGatewayCIDRs) || !sameStrings(request.NetworkIsolation.DNSResolverCIDRs, p.dnsResolverCIDRs) {
		return errors.New("Timeweb workspace request has an unapproved egress path")
	}
	return nil
}

type firewallGroup struct {
	ID     opaqueID `json:"id"`
	Name   string   `json:"name"`
	Policy string   `json:"policy"`
}
type groupsEnvelope struct {
	Groups []firewallGroup `json:"groups"`
	Group  firewallGroup   `json:"group"`
}

func (p *Provider) ensureFirewall(ctx context.Context, serverID, workspaceID string) (firewallGroup, error) {
	groupName := nameFor(workspaceID)
	groups, err := p.resourceFirewallGroups(ctx, serverID)
	if err == nil {
		for _, group := range groups {
			if group.Policy == "DROP" && group.Name == groupName {
				if err := p.reconcileFirewallRules(ctx, group.ID.String()); err != nil {
					return firewallGroup{}, err
				}
				return group, nil
			}
		}
	} else if !errors.Is(err, workspace.ErrNotFound) {
		return firewallGroup{}, err
	}
	allGroups, err := p.listFirewallGroups(ctx)
	if err != nil {
		return firewallGroup{}, err
	}
	var matching []firewallGroup
	for _, candidate := range allGroups {
		if candidate.Name == groupName {
			matching = append(matching, candidate)
		}
	}
	if len(matching) > 1 {
		return firewallGroup{}, errors.New("multiple Timeweb firewall groups share workspace identity")
	}
	var group firewallGroup
	if len(matching) == 1 {
		group = matching[0]
		if group.Policy != "DROP" {
			return firewallGroup{}, errors.New("workspace firewall group is not deny-by-default")
		}
	} else {
		query := url.Values{"policy": []string{"DROP"}}
		var created groupsEnvelope
		if err := p.doJSON(ctx, http.MethodPost, "/firewall/groups", query, map[string]any{"name": groupName, "description": "Deny-by-default disposable AI-native workspace"}, &created, http.StatusOK, http.StatusCreated); err != nil {
			return firewallGroup{}, err
		}
		group = created.Group
		if group.ID.String() == "" {
			return firewallGroup{}, errors.New("Timeweb firewall response has no group ID")
		}
	}
	if err := p.reconcileFirewallRules(ctx, group.ID.String()); err != nil {
		return firewallGroup{}, err
	}
	linkQuery := url.Values{"resource_type": []string{"server"}}
	if err := p.doJSON(ctx, http.MethodPost, "/firewall/groups/"+pathSegment(group.ID.String())+"/resources/"+pathSegment(serverID), linkQuery, nil, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent); err != nil {
		return firewallGroup{}, err
	}
	group.Policy = "DROP"
	return group, nil
}

type firewallRule struct {
	ID        opaqueID `json:"id"`
	Direction string   `json:"direction"`
	Protocol  string   `json:"protocol"`
	CIDR      string   `json:"cidr"`
	Port      string   `json:"port"`
}

type rulesEnvelope struct {
	Rules []firewallRule `json:"rules"`
}

func (p *Provider) reconcileFirewallRules(ctx context.Context, groupID string) error {
	desired := make(map[string]firewallRule)
	for _, cidr := range p.egressGatewayCIDRs {
		rule := firewallRule{Direction: "egress", Protocol: "tcp", CIDR: cidr, Port: "1-65535"}
		desired[firewallRuleKey(rule)] = rule
	}
	for _, cidr := range p.dnsResolverCIDRs {
		for _, protocol := range []string{"udp", "tcp"} {
			rule := firewallRule{Direction: "egress", Protocol: protocol, CIDR: cidr, Port: "53"}
			desired[firewallRuleKey(rule)] = rule
		}
	}
	var existing rulesEnvelope
	query := url.Values{"limit": []string{"100"}, "offset": []string{"0"}}
	if err := p.doJSON(ctx, http.MethodGet, "/firewall/groups/"+pathSegment(groupID)+"/rules", query, nil, &existing, http.StatusOK); err != nil {
		return err
	}
	for _, rule := range existing.Rules {
		key := firewallRuleKey(rule)
		if _, ok := desired[key]; ok {
			delete(desired, key)
			continue
		}
		if rule.ID.String() == "" {
			return errors.New("Timeweb firewall returned an unidentified extra rule")
		}
		if err := p.doJSON(ctx, http.MethodDelete, "/firewall/groups/"+pathSegment(groupID)+"/rules/"+pathSegment(rule.ID.String()), nil, nil, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rule := desired[key]
		description := "Workspace DNS resolver"
		if rule.Port != "53" {
			description = "Governed mTLS egress gateway"
		}
		if err := p.createFirewallRule(ctx, groupID, rule.Protocol, rule.CIDR, rule.Port, description); err != nil {
			return err
		}
	}
	return nil
}

func firewallRuleKey(rule firewallRule) string {
	return strings.Join([]string{rule.Direction, rule.Protocol, rule.CIDR, rule.Port}, "\x00")
}

func (p *Provider) listFirewallGroups(ctx context.Context) ([]firewallGroup, error) {
	var response groupsEnvelope
	query := url.Values{"limit": []string{"500"}, "offset": []string{"0"}}
	if err := p.doJSON(ctx, http.MethodGet, "/firewall/groups", query, nil, &response, http.StatusOK); err != nil {
		return nil, err
	}
	return response.Groups, nil
}

func (p *Provider) createFirewallRule(ctx context.Context, groupID, protocol, cidr, port, description string) error {
	payload := map[string]any{"direction": "egress", "protocol": protocol, "cidr": cidr, "port": port, "description": description}
	return p.doJSON(ctx, http.MethodPost, "/firewall/groups/"+pathSegment(groupID)+"/rules", nil, payload, nil, http.StatusOK, http.StatusCreated)
}

func (p *Provider) resourceFirewallGroups(ctx context.Context, serverID string) ([]firewallGroup, error) {
	var response groupsEnvelope
	query := url.Values{"limit": []string{"100"}, "offset": []string{"0"}}
	if err := p.doJSON(ctx, http.MethodGet, "/firewall/service/server/"+pathSegment(serverID), query, nil, &response, http.StatusOK); err != nil {
		return nil, err
	}
	return response.Groups, nil
}

func (p *Provider) getServer(ctx context.Context, serverID string) (serverJSON, error) {
	var response serverEnvelope
	if err := p.doJSON(ctx, http.MethodGet, "/servers/"+pathSegment(serverID), nil, nil, &response, http.StatusOK); err != nil {
		return serverJSON{}, err
	}
	return response.Server, nil
}

func (p *Provider) deleteServer(ctx context.Context, serverID string) error {
	return p.doJSON(ctx, http.MethodDelete, "/servers/"+pathSegment(serverID), nil, nil, nil, http.StatusOK, http.StatusAccepted, http.StatusNoContent)
}

func (p *Provider) doJSON(ctx context.Context, method, endpoint string, query url.Values, requestBody, responseBody any, expected ...int) error {
	var body io.Reader
	if requestBody != nil {
		raw, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	target := *p.baseURL
	target.Path = strings.TrimRight(p.baseURL.Path, "/") + endpoint
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ai-native-paas-workspace-manager/v0.1")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("Timeweb API request failed: %w", err)
	}
	defer response.Body.Close()
	allowed := false
	for _, status := range expected {
		if response.StatusCode == status {
			allowed = true
			break
		}
	}
	if response.StatusCode == http.StatusNotFound && !containsInt(expected, http.StatusNotFound) {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return workspace.ErrNotFound
	}
	if !allowed {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("Timeweb API %s %s returned HTTP %d", method, endpoint, response.StatusCode)
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	decoder.UseNumber()
	if err := decoder.Decode(responseBody); err != nil {
		return errors.New("Timeweb API returned malformed JSON")
	}
	return nil
}

func providerVM(server serverJSON, metadata workspaceMetadata, firewallGroupIDs []string) workspace.ProviderVM {
	disks := make([]string, 0, len(server.Disks))
	for _, disk := range server.Disks {
		if disk.ID.String() != "" {
			disks = append(disks, disk.ID.String())
		}
	}
	sort.Strings(disks)
	return workspace.ProviderVM{
		VMID: server.ID.String(), DiskIDs: disks, FirewallGroupIDs: uniqueStrings(firewallGroupIDs), CorrelationID: metadata.CorrelationID, ImageDigest: metadata.ImageDigest,
		NetworkProfile: metadata.NetworkProfile, PrivateAddressOnly: !hasPublicIP(server), DenyAllInbound: len(firewallGroupIDs) > 0, OutboundAgentReady: len(firewallGroupIDs) > 0,
	}
}

func encodeMetadata(metadata workspaceMetadata) (string, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return "ai-native-paas-workspace:" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeMetadata(comment string) (workspaceMetadata, error) {
	const prefix = "ai-native-paas-workspace:"
	if !strings.HasPrefix(comment, prefix) {
		return workspaceMetadata{}, errors.New("not a managed workspace")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(comment, prefix))
	if err != nil {
		return workspaceMetadata{}, err
	}
	var metadata workspaceMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.WorkspaceID == "" || metadata.CorrelationID == "" || !validDigest(metadata.ImageDigest) || metadata.NetworkProfile != "isolated-governed" {
		return workspaceMetadata{}, errors.New("invalid workspace provider metadata")
	}
	return metadata, nil
}

func hasPublicIP(server serverJSON) bool {
	for _, network := range server.Networks {
		if network.Type == "public" && len(network.IPs) > 0 {
			return true
		}
	}
	return false
}

type opaqueID string

func (id *opaqueID) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*id = ""
		return nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value != "" && !providerIDPattern.MatchString(value) {
			return errors.New("invalid provider resource ID")
		}
		*id = opaqueID(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	if !providerIDPattern.MatchString(number.String()) {
		return errors.New("invalid provider resource ID")
	}
	*id = opaqueID(number.String())
	return nil
}

func (id opaqueID) String() string { return string(id) }

func nameFor(workspaceID string) string {
	var builder strings.Builder
	builder.WriteString("paas-ws-")
	for _, character := range strings.ToLower(workspaceID) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	value := strings.Trim(builder.String(), "-")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}

func pathSegment(value string) string { return path.Clean("/" + url.PathEscape(value))[1:] }
func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := strconv.ParseUint(value[7:23], 16, 64)
	if err != nil {
		return false
	}
	for _, character := range value[7:] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
func sameStrings(left, right []string) bool {
	return strings.Join(sortedCopy(left), "\x00") == strings.Join(sortedCopy(right), "\x00")
}
func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
func cloneMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func singleHostCIDRs(values []string) bool {
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return false
		}
		ones, bits := network.Mask.Size()
		if ones != bits {
			return false
		}
	}
	return len(values) > 0
}
