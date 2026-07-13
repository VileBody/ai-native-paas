package application_test

import (
	"context"
	"encoding/json"
	"github.com/keir-research/ai-native-paas/internal/agent/application"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	"strings"
	"testing"
)

func FuzzMCPToolArguments_NoPanicNoScopeEscalation(f *testing.F) {
	f.Add([]byte(`{"name":"x"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		fx := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolGetProject)))
		req := fx.request(agentv1.ToolCreateProject, map[string]any{}, "fuzz")
		req.Arguments = json.RawMessage(b)
		_, _ = fx.svc.Invoke(context.Background(), req)
		if fx.source.CreateCalls != 0 {
			t.Fatal("scope escalation")
		}
	})
}
func FuzzRepositoryPatch_NoPathEscape(f *testing.F) {
	f.Add("../etc/passwd")
	f.Add("src/main.go")
	f.Fuzz(func(t *testing.T, path string) {
		fx := newFixture(t, agentv1.BudgetPolicy{})
		req := fx.request(agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{ProjectID: "project-1", BaseCommitSHA: "abcdef0", Branch: "main", Message: "x", Files: []application.PatchFile{{Path: path, Content: "x"}}}, "fuzz")
		_, _ = fx.svc.Invoke(context.Background(), req)
		if strings.Contains(path, "..") && fx.source.PatchCalls != 0 {
			t.Fatal("path escape")
		}
	})
}
func FuzzApprovalPayloadHash_NoConfusedDeputy(f *testing.F) {
	f.Add([]byte(`{"x":1}`), []byte(`{"x":2}`))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		if !json.Valid(a) || !json.Valid(b) {
			return
		}
		ha, _ := agentv1.StableFingerprint(json.RawMessage(a))
		hb, _ := agentv1.StableFingerprint(json.RawMessage(b))
		if string(a) != string(b) && ha == hb {
			var va, vb any
			if json.Unmarshal(a, &va) == nil && json.Unmarshal(b, &vb) == nil {
				ca, _ := json.Marshal(va)
				cb, _ := json.Marshal(vb)
				if string(ca) != string(cb) {
					t.Fatal("distinct canonical payload collision")
				}
			}
		}
	})
}
func FuzzPublicErrors_NoSecretLeak(f *testing.F) {
	f.Add("supersecret")
	f.Fuzz(func(t *testing.T, secret string) {
		if secret == "" {
			return
		}
		fx := newFixture(t, agentv1.BudgetPolicy{})
		fx.attachments.Fail = &application.ProviderError{Message: "provider " + secret}
		r, _ := fx.svc.Invoke(context.Background(), fx.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "TOKEN", Value: secret}, "f"))
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret leaked")
		}
	})
}
