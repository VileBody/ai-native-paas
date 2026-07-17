//go:build postgres_integration

package pivot_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kernelpostgres "github.com/keir-research/ai-native-paas/adapters/postgres/kernel"
	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
	"github.com/keir-research/ai-native-paas/internal/kernel/execution"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

type k14Clock struct{ now time.Time }

func (c k14Clock) Now() time.Time { return c.now }

type k14IDs struct{ next atomic.Int64 }

func (g *k14IDs) New(prefix string) string {
	return fmt.Sprintf("%s-k14-%d", prefix, g.next.Add(1))
}

// TestKernel_OperationCheckpointSurvivesDatabaseFailover proves the worker
// recovery invariant at the PostgreSQL boundary: after the original database
// pool is lost, a new worker/pool observes PLAN_CREATED and must not invoke the
// provider effect again. The managed-PostgreSQL primary failover itself is a
// release-window chaos action; this test is its deterministic executable gate.
func TestKernel_OperationCheckpointSurvivesDatabaseFailover(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; K14 requires live PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	primary, err := postgresbootstrap.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgresbootstrap.WithMigrationLock(ctx, primary, "kernel", func(lockCtx context.Context) error {
		return kernelpostgres.Migrate(lockCtx, primary)
	}); err != nil {
		_ = primary.Close()
		t.Fatal(err)
	}
	primaryStore, err := kernelpostgres.NewExecutionStore(primary)
	if err != nil {
		_ = primary.Close()
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("%d", now.UnixNano())
	graph, err := execution.NewGraph(
		"opg-k14-"+suffix,
		execution.TargetScope{TenantID: "tenant-k14-" + suffix, ProjectID: "project-k14-" + suffix},
		"k14-"+suffix,
		"sha256:"+strings.Repeat("a", 64),
		[]execution.NodeSpec{{NodeID: "infra-plan", Kind: "INFRA_PLAN", Cancelable: true}},
		now,
	)
	if err != nil {
		_ = primary.Close()
		t.Fatal(err)
	}
	if _, claimed, err := primaryStore.ClaimGraph(ctx, graph); err != nil || !claimed {
		_ = primary.Close()
		t.Fatalf("claim graph: claimed=%v err=%v", claimed, err)
	}

	ids := &k14IDs{}
	firstWorker := &execution.Service{Store: primaryStore, Clock: k14Clock{now: now.Add(time.Second)}, IDs: ids}
	var providerEffects atomic.Int32
	checkpointed, err := firstWorker.EnsureCheckpoint(ctx, graph.GraphID, "infra-plan", "PLAN_CREATED", func(context.Context) (json.RawMessage, *kernelv2.CredentialLease, error) {
		providerEffects.Add(1)
		return json.RawMessage(`{"plan":"create-postgresql","target":"staging"}`), nil, nil
	})
	if err != nil {
		_ = primary.Close()
		t.Fatal(err)
	}
	if checkpointed.Nodes[0].Checkpoint == nil || checkpointed.Nodes[0].Checkpoint.Kind != "PLAN_CREATED" {
		_ = primary.Close()
		t.Fatalf("checkpoint was not persisted: %#v", checkpointed.Nodes[0].Checkpoint)
	}

	// Simulate loss of every connection owned by the original process. A new
	// worker then connects through a fresh pool, as it would after primary
	// failover and process rescheduling.
	if err := primary.Close(); err != nil {
		t.Fatal(err)
	}
	recoveredDB, err := postgresbootstrap.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recoveredDB.Close() })
	recoveredStore, err := kernelpostgres.NewExecutionStore(recoveredDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = recoveredDB.ExecContext(context.Background(), `DELETE FROM kernel.operation_graphs WHERE graph_id=$1`, graph.GraphID)
	})
	restartedWorker := &execution.Service{Store: recoveredStore, Clock: k14Clock{now: now.Add(2 * time.Second)}, IDs: ids}
	recovered, err := restartedWorker.EnsureCheckpoint(ctx, graph.GraphID, "infra-plan", "PLAN_CREATED", func(context.Context) (json.RawMessage, *kernelv2.CredentialLease, error) {
		providerEffects.Add(1)
		return json.RawMessage(`{"duplicate":true}`), nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerEffects.Load(); got != 1 {
		t.Fatalf("provider side effect count=%d, want 1", got)
	}
	if recovered.Nodes[0].Checkpoint == nil || recovered.Nodes[0].Checkpoint.CheckpointID != checkpointed.Nodes[0].Checkpoint.CheckpointID ||
		recovered.Nodes[0].Checkpoint.PayloadHash != checkpointed.Nodes[0].Checkpoint.PayloadHash {
		t.Fatalf("checkpoint changed across recovery: before=%#v after=%#v", checkpointed.Nodes[0].Checkpoint, recovered.Nodes[0].Checkpoint)
	}
}
