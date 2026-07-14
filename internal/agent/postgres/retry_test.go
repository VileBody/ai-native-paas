package postgres

import (
	"errors"
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

func TestTransactionError_DatabaseReadFailureOverridesDerivedDomainDenial(t *testing.T) {
	databaseErr := errors.New("could not serialize access due to concurrent update (SQLSTATE 40001)")
	derived := domain.NewError(domain.CodePermissionDenied, "agent task denied")
	selected := transactionError(derived, databaseErr)
	if !errors.Is(selected, databaseErr) || !retryableDB(selected) {
		t.Fatalf("selected=%v must preserve retryable database cause", selected)
	}
}
