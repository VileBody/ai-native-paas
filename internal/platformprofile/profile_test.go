package platformprofile

import "testing"

func TestProductionProfileRejectsDevelopmentAdapters(t *testing.T) {
	if _, err := Validate("production", "kernel-api", Dev("memory-store")); err == nil {
		t.Fatal("production accepted a memory adapter")
	}
	if profile, err := Validate("production", "runtime-operator", Prod("kubernetes-api")); err != nil || profile != Production {
		t.Fatalf("production adapter rejected: profile=%s err=%v", profile, err)
	}
	if profile, err := Validate("", "kernel-api", Dev("memory-store")); err != nil || profile != Development {
		t.Fatalf("development default rejected: profile=%s err=%v", profile, err)
	}
}
