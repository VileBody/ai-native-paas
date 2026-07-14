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
