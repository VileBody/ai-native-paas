package v1

import (
	"strings"
	"testing"
	"time"
)

func TestAgentPlanReceipt_RetentionIsBoundedAndImmutableInput(t *testing.T) {
	valid := AgentPlanReceipt{
		SessionID: "session-1", ExecutionSessionID: "session-1", CommandID: "command-1",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PlanJSON: []byte(`{"resource_changes":[]}`),
		RetainedResources: []RetainedResource{{
			Address: "cozystack_postgres.primary", Provider: "cozystack", ResourceType: "cozystack_postgres",
			Policy: "platform.yaml/v2:retain", Reason: "production data retention policy",
		}},
		CapturedAt: time.Now().UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AgentPlanReceipt){
		"missing provider": func(receipt *AgentPlanReceipt) { receipt.RetainedResources[0].Provider = "" },
		"oversized reason": func(receipt *AgentPlanReceipt) { receipt.RetainedResources[0].Reason = strings.Repeat("x", 1025) },
		"nul external id":  func(receipt *AgentPlanReceipt) { receipt.RetainedResources[0].ExternalID = "id\x00suffix" },
		"duplicate address": func(receipt *AgentPlanReceipt) {
			receipt.RetainedResources = append(receipt.RetainedResources, receipt.RetainedResources[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.RetainedResources = append([]RetainedResource(nil), valid.RetainedResources...)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid retained resource was accepted")
			}
		})
	}
}
