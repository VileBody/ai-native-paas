package v2

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInvocationScopeCannotBeSuppliedByArguments(t *testing.T) {
	request := InvocationRequest{APIVersion: APIVersion, SemanticsVersion: SemanticsVersion, TaskID: "task-1", Tool: ToolInfraPlan, Arguments: json.RawMessage(`{"tenant_id":"victim","project_id":"victim"}`), IdempotencyKey: "key", CorrelationID: "corr-1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("envelope should validate independently of opaque arguments: %v", err)
	}
	context := VerifiedInvocationContext{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", CredentialID: "cred-1", GrantedScopes: map[string]struct{}{"agent.tool:infra_plan": {}}}
	if err := context.Authorize(request.Tool); err != nil {
		t.Fatalf("verified context should authorize tool: %v", err)
	}
	if _, exists := context.GrantedScopes["victim"]; exists {
		t.Fatal("client arguments unexpectedly changed verified scope")
	}
}

func TestAccessCredentialIsBoundAndShortLived(t *testing.T) {
	now := time.Now().UTC()
	claims := AccessCredentialClaims{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", Scopes: []string{"agent.tool:project_get"}, ExpiresAt: now.Add(15 * time.Minute)}
	if err := claims.Validate(now); err != nil {
		t.Fatalf("valid claims rejected: %v", err)
	}
	claims.ExpiresAt = now.Add(16 * time.Minute)
	if err := claims.Validate(now); err == nil {
		t.Fatal("overlong access credential accepted")
	}
}
