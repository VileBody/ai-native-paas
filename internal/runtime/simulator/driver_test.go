package simulator_test

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/simulator"
)

func TestRuntimeSimulatorIdentityIsDevelopmentOnly(t *testing.T) {
	if simulator.DriverName != "runtime_sim_k8s" || simulator.EvidenceClass != "dev-product" || simulator.Namespace != "paas-runtime-sim" ||
		simulator.CellID != "runtime-sim-k8s" || simulator.Region != "eu1" || simulator.ArgoProject != "runtime-sim-k8s" ||
		simulator.GitOpsRepository != "https://git.example.invalid/platform/runtime-sim.git" || simulator.IngressDomain != "sim.runtime.internal" ||
		simulator.CapacityUnits != 1000 {
		t.Fatalf("unexpected simulator identity: driver=%s evidence=%s namespace=%s cell=%s", simulator.DriverName, simulator.EvidenceClass, simulator.Namespace, simulator.CellID)
	}
	for _, kind := range []simulator.ManagedResourceKind{simulator.ManagedPostgres, simulator.ManagedRedis, simulator.ManagedS3} {
		if kind == "" {
			t.Fatal("simulator managed resource kind must be explicit")
		}
	}
}
