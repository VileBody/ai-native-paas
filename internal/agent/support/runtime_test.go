package support

import (
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	"sync"
	"testing"
)

func TestIDs_AreConcurrentUniqueAndContractSafe(t *testing.T) {
	ids := &IDs{}
	seen := sync.Map{}
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := ids.NewID("Invocation")
			if !agentv1.ValidID(id) {
				t.Errorf("invalid id %q", id)
			}
			if _, loaded := seen.LoadOrStore(id, true); loaded {
				t.Errorf("duplicate %q", id)
			}
		}()
	}
	wg.Wait()
}
