package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"gopkg.in/yaml.v3"
)

const bootstrapKeySentinel = "private-key-sentinel-must-only-appear-encoded"

type issuerFake struct{ identity Identity }

func (i *issuerFake) Issue(_ context.Context, identity Identity) (CertificateBundle, error) {
	i.identity = identity
	return CertificateBundle{
		Certificate: []byte("certificate-pem"), PrivateKey: []byte(bootstrapKeySentinel), CAChain: []byte("ca-chain-pem"),
		IdentityURI: "spiffe://workspace.platform.example.com/test", Serial: "01", NotAfter: time.Now().Add(15 * time.Minute),
	}, nil
}

func bootstrapRequest() workspace.ProviderCreateRequest {
	return workspace.ProviderCreateRequest{
		TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1",
		CorrelationID: "correlation-1", ExpiresAt: time.Now().Add(15 * time.Minute),
	}
}

func TestCloudInitRenderer_EmbedsOnlyEncodedShortLivedIdentityAndNoShell(t *testing.T) {
	issuer := &issuerFake{}
	renderer := CloudInitRenderer{Issuer: issuer, ControlPlaneURL: "https://workspace-gateway.example.com"}
	rendered, err := renderer.Render(context.Background(), bootstrapRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, "#cloud-config\n") || strings.Contains(rendered, bootstrapKeySentinel) || strings.Contains(rendered, "curl ") || strings.Contains(rendered, "sh -c") {
		t.Fatalf("unsafe cloud-init rendered:\n%s", rendered)
	}
	if issuer.identity != (Identity{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1"}) {
		t.Fatalf("issuer identity=%#v", issuer.identity)
	}
	var document cloudInitDocument
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(rendered, "#cloud-config\n")), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Users) != 0 || document.SSHPassword || !document.DisableRoot || len(document.RunCommands) != 1 || strings.Join(document.RunCommands[0], " ") != "systemctl enable --now "+agentUnit {
		t.Fatalf("cloud-init login/command policy=%#v", document)
	}
	files := make(map[string]cloudInitFile, len(document.WriteFiles))
	for _, file := range document.WriteFiles {
		files[file.Path] = file
		if file.Owner != "root:root" || file.Permissions != "0400" || file.Encoding != "b64" {
			t.Fatalf("insecure bootstrap file: %#v", file)
		}
	}
	decodedKey, err := base64.StdEncoding.DecodeString(files[agentKeyPath].Content)
	if err != nil || string(decodedKey) != bootstrapKeySentinel {
		t.Fatalf("private key encoding err=%v", err)
	}
	configRaw, err := base64.StdEncoding.DecodeString(files[agentConfigPath].Content)
	if err != nil {
		t.Fatal(err)
	}
	var config agentConfig
	if err := json.Unmarshal(configRaw, &config); err != nil || config.CorrelationID != "correlation-1" || config.ControlPlaneURL != "https://workspace-gateway.example.com" || config.PrivateKeyFile != agentKeyPath {
		t.Fatalf("agent config=%#v err=%v", config, err)
	}
}

func TestCloudInitRenderer_RejectsNonTLSControlPlaneAndSizeOverflow(t *testing.T) {
	request := bootstrapRequest()
	if _, err := (CloudInitRenderer{Issuer: &issuerFake{}, ControlPlaneURL: "http://workspace-gateway.example.com"}).Render(context.Background(), request); err == nil {
		t.Fatal("non-TLS workspace control plane accepted")
	}
	if _, err := (CloudInitRenderer{Issuer: &issuerFake{}, ControlPlaneURL: "https://workspace-gateway.example.com", MaximumByteCount: 64}).Render(context.Background(), request); err == nil {
		t.Fatal("oversized cloud-init accepted")
	}
}
