package timeweb

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
)

const liveWorkspaceImageDigest = "sha256:f633a2042c0b7ed693d3496d8d03da76981812c1c8a680b4be5e2530d4dea1d3"

func TestTimewebWorkspace_LivePrivateCreateDiscoverDestroy(t *testing.T) {
	if os.Getenv("TIMEWEB_WORKSPACE_LIVE") != "1" {
		t.Skip("set TIMEWEB_WORKSPACE_LIVE=1 for the destructive provider gate")
	}
	token := strings.TrimSpace(os.Getenv("TIMEWEB_WORKSPACE_TOKEN"))
	if token == "" {
		t.Fatal("TIMEWEB_WORKSPACE_TOKEN is required")
	}
	imageID := liveEnv("TIMEWEB_WORKSPACE_IMAGE_ID", "803212b3-aa74-4c13-9bdf-77b864216994")
	vpcID := liveEnv("TIMEWEB_WORKSPACE_VPC_ID", "network-687cddd36c3147b3bff75c79e9779498")
	edgeCIDR := liveEnv("TIMEWEB_WORKSPACE_EDGE_CIDR", "72.56.246.80/32")
	stamp := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	workspaceID := "live-" + stamp
	correlationID := "live-correlation-" + stamp

	provider, err := New(Config{
		Token: token, ProjectID: 2545534, ConfiguratorID: 31,
		AvailabilityZone: "msk-1", BandwidthMbps: 1000, SystemDiskMiB: 40960,
		ImageIDs:          map[string]string{liveWorkspaceImageDigest: imageID},
		ControlPlaneCIDRs: []string{edgeCIDR}, ControlPlanePort: 32443,
		EgressGatewayCIDRs: []string{edgeCIDR}, EgressGatewayPort: 32444,
		DNSResolverCIDRs: []string{"1.1.1.1/32", "8.8.8.8/32"},
		RenderCloudInit: func(context.Context, workspace.ProviderCreateRequest) (string, error) {
			return "#cloud-config\nusers: []\nssh_pwauth: false\ndisable_root: true\n", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := workspace.ProviderCreateRequest{
		WorkspaceID: workspaceID, TenantID: "live-tenant", ProjectID: "live-project", TaskID: "live-task", AgentID: "live-agent",
		CorrelationID: correlationID, ImageDigest: liveWorkspaceImageDigest, CPUMillis: 1000, MemoryMiB: 1024,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute), NetworkProfile: "isolated-governed",
		NetworkIsolation: workspace.NetworkIsolation{
			VPCID: vpcID, PrivateAddressOnly: true, DenyAllInbound: true, OutboundGatewayMTLS: true,
			EgressGatewayCIDRs: []string{edgeCIDR}, DNSResolverCIDRs: []string{"1.1.1.1/32", "8.8.8.8/32"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	created, err := provider.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	destroyed := false
	t.Cleanup(func() {
		if destroyed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if _, cleanupErr := destroyEventually(cleanupCtx, provider, created); cleanupErr != nil {
			t.Errorf("cleanup live workspace: %v", cleanupErr)
		}
	})
	if created.VMID == "" || len(created.DiskIDs) == 0 || len(created.FirewallGroupIDs) == 0 || !created.PrivateAddressOnly || !created.DenyAllInbound {
		t.Fatalf("provider returned incomplete isolation evidence: %#v", created)
	}
	found, err := provider.FindByCorrelation(ctx, correlationID)
	if err != nil {
		t.Fatal(err)
	}
	if found.VMID != created.VMID || found.CorrelationID != correlationID || !found.PrivateAddressOnly || !found.DenyAllInbound {
		t.Fatalf("lost-response discovery changed provider identity: created=%#v found=%#v", created, found)
	}
	evidence, err := destroyEventually(ctx, provider, found)
	if err != nil {
		t.Fatal(err)
	}
	destroyed = true
	if !evidence.VMAbsent || len(evidence.AbsentDiskIDs) == 0 || len(evidence.AbsentFirewallGroupIDs) == 0 {
		t.Fatalf("incomplete destroy evidence: %#v", evidence)
	}
}

func destroyEventually(ctx context.Context, provider *Provider, target workspace.ProviderVM) (workspace.DestroyEvidence, error) {
	for {
		evidence, err := provider.Destroy(ctx, target)
		if err == nil {
			return evidence, nil
		}
		select {
		case <-ctx.Done():
			return workspace.DestroyEvidence{}, err
		case <-time.After(5 * time.Second):
		}
	}
}

func liveEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
