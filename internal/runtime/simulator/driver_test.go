package simulator_test

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/simulator"
)

func TestRuntimeSimulatorIdentityIsDevelopmentOnly(t *testing.T) {
	if simulator.DriverName != "runtime_sim_k8s" || simulator.EvidenceClass != "dev-product" || simulator.Namespace != "paas-runtime-sim" {
		t.Fatalf("unexpected simulator identity: driver=%s evidence=%s namespace=%s", simulator.DriverName, simulator.EvidenceClass, simulator.Namespace)
	}
	for _, kind := range []simulator.ManagedResourceKind{simulator.ManagedPostgres, simulator.ManagedRedis, simulator.ManagedS3} {
		if kind == "" {
			t.Fatal("simulator managed resource kind must be explicit")
		}
	}
}
