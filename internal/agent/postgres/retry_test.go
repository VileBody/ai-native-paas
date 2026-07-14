package postgres

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/agent/domain"
)

func TestSerializableRetry_OptimisticStaleVersionRetriesWithoutRetryingBusinessConflict(t *testing.T) {
	if !retryableDB(domain.NewError(domain.CodeConflict, "task version is stale")) {
		t.Fatal("optimistic stale version must retry the complete transaction")
	}
	if retryableDB(domain.NewError(domain.CodeConflict, "idempotency key payload conflict")) {
		t.Fatal("business conflict must not retry")
	}
}
