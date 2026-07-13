package kernelpostgres

import (
	"errors"
	"strings"
	"testing"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	domain "github.com/keir-research/ai-native-paas/internal/kernel"
)

func TestPostgres_MapDatabaseErrorToStableDomainCodes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{name: "duplicate", err: errors.New("duplicate key value violates unique constraint"), code: kernelv1.CodeConflict},
		{name: "deadlock", err: errors.New("deadlock detected"), code: kernelv1.CodeOptimisticLock, retryable: true},
		{name: "serialization", err: errors.New("serialization failure"), code: kernelv1.CodeOptimisticLock, retryable: true},
		{name: "internal", err: errors.New("password=secret host=internal-db"), code: kernelv1.CodeInternal, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapDatabaseError("test operation", test.err)
			if domain.ErrorCode(mapped) != test.code {
				t.Fatalf("code = %s, want %s", domain.ErrorCode(mapped), test.code)
			}
			public := domain.ToPublicError(mapped, "")
			if public.Retryable != test.retryable {
				t.Fatalf("retryable = %v, want %v", public.Retryable, test.retryable)
			}
			encoded := public.Error() + public.Message
			if strings.Contains(encoded, "secret") || strings.Contains(encoded, "internal-db") {
				t.Fatalf("database cause leaked: %+v", public)
			}
		})
	}
}

func TestPostgres_MapDatabaseErrorNil(t *testing.T) {
	if mapDatabaseError("noop", nil) != nil {
		t.Fatal("nil database error was not preserved")
	}
}
