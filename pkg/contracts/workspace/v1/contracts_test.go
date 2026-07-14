package v1

import "testing"

func TestWorkspaceContracts_EnvironmentContainsReferencesNotPlaintext(t *testing.T) {
	valid := CommandSpec{
		Argv: []string{"tofu", "plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 60, OutputLimitBytes: 4096,
		EnvironmentRefs: map[string]string{"TWC_TOKEN": "credential://lease-123", "STATE_TOKEN": "state://project/staging"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []struct{ name, value string }{
		{name: "TOKEN", value: "super-secret-value"},
		{name: "bad name", value: "credential://lease-123"},
		{name: "TOKEN", value: "credential://lease;curl-evil"},
	} {
		candidate := valid
		candidate.EnvironmentRefs = map[string]string{unsafe.name: unsafe.value}
		if candidate.Validate() == nil {
			t.Fatalf("unsafe environment reference accepted: %q=%q", unsafe.name, unsafe.value)
		}
	}
}

func TestWorkspaceOutputChunk_RejectsTamperedAndNonFinalTerminalMetadata(t *testing.T) {
	valid := AgentOutputChunk{
		SessionID: "session-1", CommandID: "command-1", Stream: AgentOutputStdout, Data: []byte("safe"),
		ChunkSHA256: "sha256:8b3369944dd2a3fab39e32d1aeb1f763946a458ae3e6368a46432adc8f3a0860", Final: true,
		TotalSHA256: "sha256:8b3369944dd2a3fab39e32d1aeb1f763946a458ae3e6368a46432adc8f3a0860",
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tampered := valid
	tampered.Data = []byte("evil")
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered output chunk accepted")
	}
	nonFinal := valid
	nonFinal.Final = false
	if err := nonFinal.Validate(); err == nil {
		t.Fatal("non-final output chunk carried stream digest")
	}
}
