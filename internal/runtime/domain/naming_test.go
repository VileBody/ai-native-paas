package domain

import (
	"testing"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func FuzzRuntimeObjectNamesRemainDNSBounded(f *testing.F) {
	f.Add("app-a", "production")
	f.Add("APP with spaces/and/slashes", "MR-42")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, applicationID, environment string) {
		if len(applicationID) > 4096 || len(environment) > 4096 {
			t.Skip()
		}
		namespace := EnvironmentNamespace(applicationID, environment)
		name := PaaSAppName(applicationID, environment)
		for label, value := range map[string]string{"namespace": namespace, "name": name} {
			if len(value) > 63 || !runtimev1.ValidDNSLabel(value) {
				t.Fatalf("%s is not a bounded DNS label: %q", label, value)
			}
		}
		if EnvironmentNamespace(applicationID, environment) != namespace || PaaSAppName(applicationID, environment) != name {
			t.Fatal("runtime naming is not deterministic")
		}
		hostname, err := GeneratedHostname(applicationID, environment, "apps.eu1.example.invalid")
		if err != nil {
			t.Fatalf("fixed ingress domain rejected: %v", err)
		}
		if !runtimev1.ValidDNSSubdomain(hostname) {
			t.Fatalf("generated hostname is invalid: %q", hostname)
		}
	})
}
