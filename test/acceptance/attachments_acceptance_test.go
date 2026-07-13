package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func TestApplicationAttachments_FullLifecycleDoesNotDiscloseSecretsOrDeleteManagedData(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	store := memory.New()
	vault := testkit.NewVault()
	provider := &testkit.Provider{
		EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-pg-1", Endpoint: "pg.service", Ready: true},
		GetResult:    application.ProviderInstanceResult{ProviderID: "provider-pg-1", Endpoint: "pg.service", Ready: true},
	}
	dns := &testkit.DNS{Values: map[string][]string{}}
	certificates := &testkit.Certificates{EnsureResult: application.CertificateResult{CertificateID: "cert-1", Status: application.CertificateReady}}
	runtime := testkit.NewRuntime()
	logger := &testkit.Logger{}
	service := &application.Service{
		Store: store,
		Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{
			"env-a": {TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Name: "production", Ready: true},
		}},
		Secrets: vault, Provider: provider, DNS: dns, Certificates: certificates,
		Approvals: &testkit.Approvals{Grants: map[string][3]string{}}, Runtime: runtime,
		Commerce: &testkit.Commerce{Allowed: true}, Usage: &testkit.Usage{}, Logger: logger,
		Clock: clock, IDs: &application.SequentialIDs{}, DefaultDomain: "apps.example.test",
		DNSObservationDelay: time.Minute, DomainQuarantine: 24 * time.Hour,
	}
	plan, err := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "mapping-v1", false, []string{"connect", "read", "write"}, true, true, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterServicePlan(ctx, plan, "system"); err != nil {
		t.Fatal(err)
	}

	const appSecret = "top-secret-app-value"
	secret, firstSnapshot, err := service.SetSecret(ctx, application.SetSecretRequest{
		TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Name: "API_TOKEN",
		Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime,
		Value: []byte(appSecret), ActorID: "user-1", IdempotencyKey: "secret-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != "API_TOKEN" || firstSnapshot.SnapshotID == "" || !vault.Contains(appSecret) {
		t.Fatalf("secret=%+v snapshot=%+v", secret, firstSnapshot)
	}

	instance, err := service.ProvisionService(ctx, application.ProvisionServiceRequest{TenantID: "tenant-a", Name: "main-db", PlanID: "pg-small", ActorID: "user-1", IdempotencyKey: "provision-1"})
	if err != nil || instance.State != attachmentsv1.ServiceReady {
		t.Fatalf("instance=%+v err=%v", instance, err)
	}
	binding, secondSnapshot, err := service.BindService(ctx, application.BindServiceRequest{
		TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", InstanceID: instance.ID,
		Capabilities: []string{"connect"}, ActorID: "user-1", IdempotencyKey: "bind-1",
	})
	if err != nil || binding.State != attachmentsv1.BindingActive || secondSnapshot.Version <= firstSnapshot.Version {
		t.Fatalf("binding=%+v snapshot=%+v err=%v", binding, secondSnapshot, err)
	}

	claim, err := service.ClaimCustomDomain(ctx, application.ClaimDomainRequest{TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Hostname: "booking.example.com", ActorID: "user-1", IdempotencyKey: "domain-1"})
	if err != nil || claim.State != attachmentsv1.DomainAwaitingVerification {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if _, routeErr := service.RouteForEnvironment(ctx, "tenant-a", "env-a"); routeErr == nil {
		t.Fatal("route activated before DNS ownership proof")
	}
	dns.Values[claim.ChallengeName] = []string{claim.ChallengeValue}
	if _, err := service.VerifyDomain(ctx, application.VerifyDomainRequest{TenantID: "tenant-a", ClaimID: claim.ID, ActorID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	claim, err = service.VerifyDomain(ctx, application.VerifyDomainRequest{TenantID: "tenant-a", ClaimID: claim.ID, ActorID: "user-1"})
	if err != nil || claim.State != attachmentsv1.DomainActive {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	route, err := service.RouteForEnvironment(ctx, "tenant-a", "env-a")
	if err != nil || route != "booking.example.com" {
		t.Fatalf("route=%q err=%v", route, err)
	}

	resolved, err := service.Resolve(ctx, "tenant-a", "env-a")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(resolved)
	for _, forbidden := range []string{appSecret, "postgres://secret", "DATABASE_URL"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, encoded)
		}
	}
	if len(resolved.ServiceBindings) != 1 || len(resolved.ActiveDomains) != 1 {
		t.Fatalf("snapshot=%+v", resolved)
	}

	rotated, rotatedSnapshot, err := service.RotateBinding(ctx, application.RotateBindingRequest{TenantID: "tenant-a", BindingID: binding.ID, ActorID: "user-1", IdempotencyKey: "rotate-1"})
	if err != nil || rotated.State != attachmentsv1.BindingActive || rotatedSnapshot.Version <= resolved.Version {
		t.Fatalf("rotated=%+v snapshot=%+v err=%v", rotated, rotatedSnapshot, err)
	}
	if provider.RevokeCalls != 1 {
		t.Fatalf("old credential revoke calls=%d", provider.RevokeCalls)
	}

	if err := service.DeleteApplicationAttachments(ctx, "tenant-a", "app-a", "user-1"); err != nil {
		t.Fatal(err)
	}
	revoked, err := service.GetBinding(ctx, "tenant-a", binding.ID)
	if err != nil || revoked.State != attachmentsv1.BindingRevoked {
		t.Fatalf("binding after app delete=%+v err=%v", revoked, err)
	}
	retained, err := service.GetServiceInstance(ctx, "tenant-a", instance.ID)
	if err != nil || retained.State != attachmentsv1.ServiceReady || provider.DeleteCalls != 0 {
		t.Fatalf("service after app delete=%+v deleteCalls=%d err=%v", retained, provider.DeleteCalls, err)
	}

	for _, event := range store.Audit() {
		if strings.Contains(string(event.Data), appSecret) || strings.Contains(string(event.Data), "postgres://secret") {
			t.Fatalf("audit leaked credential: %s", event.Data)
		}
	}
	for _, event := range store.Outbox() {
		if strings.Contains(string(event.Payload), appSecret) || strings.Contains(string(event.Payload), "postgres://secret") {
			t.Fatalf("outbox leaked credential: %s", event.Payload)
		}
	}
	if logger.Contains(appSecret) || logger.Contains("postgres://secret") {
		t.Fatal("structured logs leaked credential")
	}
}
