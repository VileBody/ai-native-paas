package postgres

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

func TestSerializableRetry_DoesNotRetryDomainConflicts(t *testing.T) {
	if retryableDB(domain.NewError(domain.CodeStaleVersion, "runtime cell version is stale")) {
		t.Fatal("explicit optimistic stale-version result must remain visible to the caller")
	}
	if retryableDB(domain.NewError(domain.CodeConflict, "release identity conflict")) {
		t.Fatal("business conflict must not retry")
	}
}
