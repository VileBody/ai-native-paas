package architecture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEveryEntrypointDeclaresProductionAdapterInventory(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	entrypoints, err := filepath.Glob(filepath.Join(root, "cmd", "*", "main.go"))
	if err != nil || len(entrypoints) == 0 {
		t.Fatalf("discover entrypoints: files=%d err=%v", len(entrypoints), err)
	}
	for _, entrypoint := range entrypoints {
		raw, err := os.ReadFile(entrypoint)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		legacyInventory := strings.Contains(source, "platformprofile.Validate(os.Getenv(\"PLATFORM_PROFILE\")")
		dynamicInventory := strings.Contains(source, "platformprofile.Parse(os.Getenv(\"PLATFORM_PROFILE\"))") &&
			strings.Contains(source, "platformprofile.Validate(string(profile)")
		if !legacyInventory && !dynamicInventory {
			t.Errorf("%s has no fail-closed production adapter inventory", filepath.Base(filepath.Dir(entrypoint)))
		}
	}
}

func TestProductionHumanAPIEntrypointsBootstrapPostgresOIDC(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	for _, api := range []string{"commerce-api", "kernel-api", "project-api", "source-api"} {
		raw, err := os.ReadFile(filepath.Join(root, "cmd", api, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, required := range []string{
			"oidcverify.NewPostgresVerifier(",
			"OIDC:",
			"oidcVerifier",
			"oidc-jwks-verifier",
			"postgres-membership-resolver",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("%s does not wire production identity component %q", api, required)
			}
		}
	}
}

func TestProductionExecutionAPIsPersistAndVerifyIdentity(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	for _, api := range []string{"agent-api", "attachments-api", "build-api", "runtime-api"} {
		raw, err := os.ReadFile(filepath.Join(root, "cmd", api, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, required := range []string{
			"postgresbootstrap.Open(",
			"postgresbootstrap.WithMigrationLock(",
			"oidcverify.NewPostgresVerifier(",
			"httpauth.Middleware{",
			"verified-identity-middleware",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("%s does not wire production persistence/identity component %q", api, required)
			}
		}
	}
}

func TestProjectAPIMCPWritesAgentTaskEvidence(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "cmd", "project-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"agentpostgres.NewStore(",
		"postgresbootstrap.WithMigrationLock(bootstrapCtx, db, \"agent\", agentStore.Migrate)",
		"platformprofile.Prod(\"agent-postgres-store\")",
		"platformprofile.Prod(\"project-mcp-agent-task-evidence-recorder\")",
		"agentAuditService := &agentapp.Service{Store: agentStore",
		"Audit: agentAuditService",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("project-api does not wire Project MCP task evidence component %q", required)
		}
	}
}

func TestAgentAPIProductionUsesCommerceGatewayWhenConfigured(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "cmd", "agent-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"productiongate.NewHTTPCommerce(os.Getenv(\"COMMERCE_API_URL\")",
		"AGENT_API_SERVICE_PRINCIPAL",
		"commerce-http-gateway-or-fail-closed",
		"commerceGateway =",
		"usageGateway =",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("agent-api does not wire production commerce gateway component %q", required)
		}
	}
}

func TestAgentAPIProductionUsesKernelOperationGatewayWhenConfigured(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "cmd", "agent-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"productiongate.NewHTTPOperations(os.Getenv(\"KERNEL_API_URL\")",
		"AGENT_API_SERVICE_PRINCIPAL",
		"kernel-http-operation-gateway-or-fail-closed",
		"operationGateway =",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("agent-api does not wire production kernel operation gateway component %q", required)
		}
	}
}

func TestAgentAPIProductionUsesBuildGatewayWhenConfigured(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "cmd", "agent-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"productiongate.NewHTTPBuilds(",
		"BUILD_API_URL",
		"AGENT_BUILD_BUILDER_DIGEST",
		"AGENT_BUILD_RUN_IMAGE_DIGEST",
		"AGENT_BUILD_PLATFORM_VERSION",
		"build-http-gateway-or-fail-closed",
		"buildGateway =",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("agent-api does not wire production build gateway component %q", required)
		}
	}
}

func TestProductionInternalServiceGatewaysUseVerifiedMTLS(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	agentRaw, err := os.ReadFile(filepath.Join(root, "cmd", "agent-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	agent := string(agentRaw)
	for _, required := range []string{
		"servicemtls.NewClient(", "INTERNAL_MTLS_CA_FILE", "INTERNAL_MTLS_CLIENT_CERT_FILE", "INTERNAL_MTLS_CLIENT_KEY_FILE",
		"requireHTTPS(serviceURLs...)", "productiongate.NewHTTPRuntime(", "RUNTIME_API_URL", "productiongate.NewHTTPAttachments(", "ATTACHMENTS_API_URL",
		"internal-spiffe-mtls-client",
	} {
		if !strings.Contains(agent, required) {
			t.Errorf("agent-api internal mTLS wiring missing %q", required)
		}
	}
	for _, api := range []string{"build-api", "runtime-api", "attachments-api", "commerce-api", "kernel-api"} {
		raw, err := os.ReadFile(filepath.Join(root, "cmd", api, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, required := range []string{"servicemtls.NewServer(", "servicemtls.ServerConfigFromEnv(", "ListenAndServeTLS(\"\", \"\")", "internal-spiffe-mtls-server"} {
			if !strings.Contains(source, required) {
				t.Errorf("%s internal mTLS server wiring missing %q", api, required)
			}
		}
	}
	for _, name := range []string{"gateways.go", "runtime_gateway.go", "attachments_gateway.go"} {
		raw, err := os.ReadFile(filepath.Join(root, "internal", "agent", "productiongate", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"Header.Set(\"X-Principal-ID\"", "Header.Set(\"X-Principal-Kind\"", "Header.Set(\"X-Scopes\""} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("%s still sends forgeable identity header %q", name, forbidden)
			}
		}
	}
}

func TestProductionKernelRequiresJetStreamOutboxPublisher(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "cmd", "kernel-api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"kernelnats.Connect(",
		"NATS_URL",
		"NATS_AUTH_TOKEN",
		"nats-jetstream-outbox-publisher",
		"kernel.OutboxDispatcher",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("kernel-api does not wire production event component %q", required)
		}
	}
}
