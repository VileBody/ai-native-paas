//go:build postgres_integration

package integration_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

func TestPostgres_MigrationAdvisoryLockChoosesOneMigratorAtATime(t *testing.T) {
	db := openPostgres(t)
	var active atomic.Int32
	var maximum atomic.Int32
	start := make(chan struct{})
	errors := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)

	for range 2 {
		go func() {
			ready.Done()
			<-start
			errors <- postgresbootstrap.WithMigrationLock(context.Background(), db, "five-replica-sentinel", func(context.Context) error {
				current := active.Add(1)
				for {
					old := maximum.Load()
					if current <= old || maximum.CompareAndSwap(old, current) {
						break
					}
				}
				time.Sleep(75 * time.Millisecond)
				active.Add(-1)
				return nil
			})
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("migration callbacks overlapped: max=%d", maximum.Load())
	}
}
