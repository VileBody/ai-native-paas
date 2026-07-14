package postgres

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

func TestSerializableRetry_OptimisticStaleVersionRetriesWithoutRetryingBusinessConflict(t *testing.T) {
	if !retryableDB(domain.NewError(domain.CodeStaleVersion, "runtime cell version is stale")) {
		t.Fatal("optimistic stale version must retry the complete transaction")
	}
	if retryableDB(domain.NewError(domain.CodeConflict, "release identity conflict")) {
		t.Fatal("business conflict must not retry")
	}
}
