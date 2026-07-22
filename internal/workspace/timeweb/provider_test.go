package timeweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
)

const (
	testDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testToken  = "timeweb-master-token-must-never-leak"
)

type apiFixture struct {
	mu                   sync.Mutex
	serverCreated        bool
	serverDeleted        bool
	firewallGroupCreated bool
	firewallGroupDeleted bool
	firewallLinked       bool
	publicIP             bool
	failRuleCreation     bool
	comment              string
	serverPayload        map[string]any
	rules                []map[string]any
}

func (f *apiFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/servers":
		if err := json.NewDecoder(r.Body).Decode(&f.serverPayload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.comment, _ = f.serverPayload["comment"].(string)
		f.serverCreated = true
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"server": f.serverJSON()})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/servers":
		servers := []any{}
		if f.serverCreated && !f.serverDeleted {
			servers = append(servers, f.serverJSON())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": servers})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/firewall/service/server/42":
		groups := []any{}
		if f.firewallLinked {
			groups = append(groups, map[string]any{"id": "group-1", "name": "paas-ws-workspace-1", "policy": "DROP"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/firewall/groups":
		groups := []any{}
		if f.firewallGroupCreated && !f.firewallGroupDeleted {
			groups = append(groups, map[string]any{"id": "group-1", "name": "paas-ws-workspace-1", "policy": "DROP"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/firewall/groups":
		if r.URL.Query().Get("policy") != "DROP" {
			http.Error(w, "firewall must default drop", http.StatusBadRequest)
			return
		}
		f.firewallGroupCreated = true
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"group": map[string]any{"id": "group-1", "name": "paas-ws-workspace-1", "policy": "DROP"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/firewall/groups/group-1/rules":
		rules := make([]map[string]any, len(f.rules))
		for index, rule := range f.rules {
			rules[index] = make(map[string]any, len(rule)+1)
			for key, value := range rule {
				rules[index][key] = value
			}
			rules[index]["id"] = "rule-" + strconv.Itoa(index+1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rules": rules})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/firewall/groups/group-1/rules/rule-"):
		index, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/firewall/groups/group-1/rules/rule-"))
		if err != nil || index < 1 || index > len(f.rules) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		f.rules = append(f.rules[:index-1], f.rules[index:]...)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/firewall/groups/group-1/rules":
		if f.failRuleCreation {
			http.Error(w, "rule provider failure", http.StatusInternalServerError)
			return
		}
		var rule map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "bad rule", http.StatusBadRequest)
			return
		}
		f.rules = append(f.rules, rule)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"rule":{"id":"rule-1"}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/firewall/groups/group-1/resources/42":
		if r.URL.Query().Get("resource_type") != "server" {
			http.Error(w, "wrong resource type", http.StatusBadRequest)
			return
		}
		f.firewallLinked = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/servers/42":
		f.serverDeleted = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/servers/42":
		if f.serverDeleted {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"server": f.serverJSON()})
	case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/firewall/groups/group-1":
		f.firewallGroupDeleted = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/firewall/groups/group-1":
		if f.firewallGroupDeleted || !f.firewallGroupCreated {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"group": map[string]any{"id": "group-1", "name": "paas-ws-workspace-1", "policy": "DROP"}})
	default:
		http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
	}
}

func (f *apiFixture) serverJSON() map[string]any {
	networkType := "local"
	address := "192.168.75.10"
	if f.publicIP {
		networkType = "public"
		address = "203.0.113.10"
	}
	return map[string]any{
		"id": 42, "name": "paas-ws-workspace-1", "comment": f.comment,
		"disks":    []any{map[string]any{"id": 99}},
		"networks": []any{map[string]any{"type": networkType, "ips": []any{map[string]any{"ip": address}}}},
	}
}

func newProvider(t *testing.T, handler http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(Config{
		BaseURL: server.URL + "/api/v1", Token: testToken, ProjectID: 2545534, ConfiguratorID: 123,
		AvailabilityZone: "msk-1", BandwidthMbps: 100, SystemDiskMiB: 40960,
		ImageIDs: map[string]string{testDigest: "image-uuid-1"}, ControlPlaneCIDRs: []string{"192.168.75.5/32"}, ControlPlanePort: 9443,
		EgressGatewayCIDRs: []string{"192.168.75.4/32"},
		EgressGatewayPort:  8443, DNSResolverCIDRs: []string{"192.168.75.1/32"}, HTTPClient: server.Client(),
		RenderCloudInit: func(context.Context, workspace.ProviderCreateRequest) (string, error) {
			return "#cloud-config\nwrite_files: []\n", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider, server
}

func providerRequest() workspace.ProviderCreateRequest {
	return workspace.ProviderCreateRequest{
		WorkspaceID: "workspace-1", TenantID: "tenant-1", ProjectID: "project-1", TaskID: "task-1", AgentID: "agent-1", CorrelationID: "correlation-1",
		ImageDigest: testDigest, CPUMillis: 2000, MemoryMiB: 4096, ExpiresAt: time.Now().Add(15 * time.Minute),
		NetworkProfile: "isolated-governed", NetworkIsolation: workspace.NetworkIsolation{
			VPCID: "vpc-workspace", PrivateAddressOnly: true, DenyAllInbound: true, OutboundGatewayMTLS: true,
			AllowedEgressHosts: []string{"gitlab.com"}, EgressGatewayCIDRs: []string{"192.168.75.4/32"},
			DNSResolverCIDRs: []string{"192.168.75.1/32"}, DeniedCIDRs: []string{"169.254.169.254/32"},
		},
	}
}

func TestTimewebWorkspace_CreateFindDestroyIsPrivateFailClosedAndRecoverable(t *testing.T) {
	api := &apiFixture{}
	provider, _ := newProvider(t, api)
	created, err := provider.Create(context.Background(), providerRequest())
	if err != nil {
		t.Fatal(err)
	}
	if created.VMID != "42" || len(created.DiskIDs) != 1 || created.DiskIDs[0] != "99" || len(created.FirewallGroupIDs) != 1 || created.FirewallGroupIDs[0] != "group-1" || !created.PrivateAddressOnly || !created.DenyAllInbound {
		t.Fatalf("invalid provider VM: %#v", created)
	}
	api.mu.Lock()
	payload := api.serverPayload
	rules := append([]map[string]any(nil), api.rules...)
	api.mu.Unlock()
	network, _ := payload["network"].(map[string]any)
	if network["id"] != "vpc-workspace" || network["floating_ip"] != nil || payload["is_root_password_required"] != false || payload["project_id"] != float64(2545534) && payload["project_id"] != int64(2545534) {
		t.Fatalf("server payload can expose workspace: %#v", payload)
	}
	if len(rules) != 4 {
		t.Fatalf("want control-plane TCP, gateway TCP and resolver TCP/UDP rules, got %#v", rules)
	}
	for _, rule := range rules {
		if rule["direction"] != "egress" || rule["cidr"] != "192.168.75.5/32" && rule["cidr"] != "192.168.75.4/32" && rule["cidr"] != "192.168.75.1/32" {
			t.Fatalf("firewall rule opens an unapproved path: %#v", rule)
		}
		if rule["cidr"] == "192.168.75.4/32" && rule["port"] != "8443" {
			t.Fatalf("gateway firewall opens more than the proxy port: %#v", rule)
		}
		if rule["cidr"] == "192.168.75.5/32" && rule["port"] != "9443" {
			t.Fatalf("control-plane firewall opens more than the manager port: %#v", rule)
		}
	}
	api.mu.Lock()
	api.rules = append(api.rules, map[string]any{"direction": "ingress", "protocol": "tcp", "cidr": "0.0.0.0/0", "port": "22"})
	api.mu.Unlock()
	found, err := provider.FindByCorrelation(context.Background(), "correlation-1")
	if err != nil || found.VMID != created.VMID || found.CorrelationID != created.CorrelationID {
		t.Fatalf("lost-response discovery failed: found=%#v err=%v", found, err)
	}
	api.mu.Lock()
	ruleCountAfterReconcile := len(api.rules)
	api.mu.Unlock()
	if ruleCountAfterReconcile != 4 {
		t.Fatalf("firewall drift was not removed, rules=%d", ruleCountAfterReconcile)
	}
	evidence, err := provider.Destroy(context.Background(), found)
	if err != nil || !evidence.VMAbsent || len(evidence.AbsentDiskIDs) != 1 || evidence.AbsentDiskIDs[0] != "99" || len(evidence.AbsentFirewallGroupIDs) != 1 || evidence.AbsentFirewallGroupIDs[0] != "group-1" {
		t.Fatalf("destroy evidence=%#v err=%v", evidence, err)
	}
}

func TestTimewebWorkspace_RejectsUnapprovedEgressBeforeProviderCall(t *testing.T) {
	api := &apiFixture{}
	provider, _ := newProvider(t, api)
	request := providerRequest()
	request.NetworkIsolation.EgressGatewayCIDRs = []string{"0.0.0.0/0"}
	if _, err := provider.Create(context.Background(), request); err == nil {
		t.Fatal("unapproved egress path was accepted")
	}
	if api.serverCreated {
		t.Fatal("provider side effect happened before policy denial")
	}
}

func TestTimewebWorkspace_ProviderErrorsNeverEchoTokenOrResponseBody(t *testing.T) {
	provider, _ := newProvider(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"` + testToken + `"}`))
	}))
	_, err := provider.Create(context.Background(), providerRequest())
	if err == nil {
		t.Fatal("provider error expected")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("provider secret leaked in error: %v", err)
	}
	if errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("provider 500 mapped to not found: %v", err)
	}
}

func TestTimewebWorkspace_PublicAddressAndPartialFirewallFailureAreCleanedUp(t *testing.T) {
	for _, test := range []struct {
		name         string
		configureAPI func(*apiFixture)
		wantFirewall bool
	}{
		{name: "unexpected public address", configureAPI: func(api *apiFixture) { api.publicIP = true }},
		{name: "firewall rule failure", configureAPI: func(api *apiFixture) { api.failRuleCreation = true }, wantFirewall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &apiFixture{}
			test.configureAPI(api)
			provider, _ := newProvider(t, api)
			if _, err := provider.Create(context.Background(), providerRequest()); err == nil {
				t.Fatal("partial provider failure expected")
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if !api.serverDeleted {
				t.Fatal("partially created workspace server was left behind")
			}
			if test.wantFirewall && !api.firewallGroupDeleted {
				t.Fatal("partially created firewall group was left behind")
			}
		})
	}
}
