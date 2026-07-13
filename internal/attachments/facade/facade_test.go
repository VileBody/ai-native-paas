package facade_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/facade"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func facadeFixture(t *testing.T) (*facade.Service, *testkit.Vault, *testkit.Provider) {
	t.Helper()
	clock := testkit.NewClock()
	vault := testkit.NewVault()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-1", Ready: true}}
	core := &application.Service{
		Store: memory.New(),
		Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{
			"env-1": {TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Ready: true},
		}},
		Secrets: vault, Provider: provider, Approvals: &testkit.Approvals{Grants: map[string][3]string{}},
		Commerce: &testkit.Commerce{Allowed: true}, Usage: &testkit.Usage{}, Runtime: testkit.NewRuntime(),
		Clock: clock, IDs: &application.SequentialIDs{},
	}
	plan, err := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "mapping-v1", false, []string{"read"}, true, true, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := core.RegisterServicePlan(context.Background(), plan, "system"); err != nil {
		t.Fatal(err)
	}
	return &facade.Service{Core: core}, vault, provider
}

func TestAttachments_AgentFacadeSetSecretIsWriteOnly(t *testing.T) {
	service, vault, _ := facadeFixture(t)
	const secret = "AGENT_WRITE_ONLY_SECRET"
	metadata, err := service.SetSecret(context.Background(), attachmentsv1.SetSecretRequest{
		TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Name: "API_TOKEN", Value: secret, ActorID: "agent-1", IdempotencyKey: "set-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(metadata)
	if strings.Contains(string(raw), secret) || !vault.Contains(secret) {
		t.Fatalf("metadata=%s vault_contains=%t", raw, vault.Contains(secret))
	}
}

func TestAttachments_AgentFacadePurgeRequiresApproval(t *testing.T) {
	service, _, provider := facadeFixture(t)
	instance, err := service.Provision(context.Background(), attachmentsv1.ServiceRequest{
		TenantID: "tenant-1", ServiceType: "postgresql", Plan: "pg-small", Name: "primary",
		ActorID: "agent-1", IdempotencyKey: "provision-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Purge(context.Background(), facade.PurgeRequest{
		TenantID: "tenant-1", ServiceInstanceID: instance.InstanceID, ActorID: "agent-1", IdempotencyKey: "purge-1",
	})
	if err == nil || provider.DeleteCalls != 0 {
		t.Fatalf("err=%v provider deletes=%d", err, provider.DeleteCalls)
	}
}
