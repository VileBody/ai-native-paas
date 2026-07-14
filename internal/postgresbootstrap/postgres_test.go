package postgresbootstrap

import (
	"context"
	"testing"
)

func TestOpenRequiresDatabaseURL(t *testing.T) {
	if _, err := Open(context.Background(), " \t "); err == nil {
		t.Fatal("empty DATABASE_URL was accepted")
	}
}

func TestWithMigrationLockValidatesArguments(t *testing.T) {
	if err := WithMigrationLock(context.Background(), nil, "kernel", func(context.Context) error { return nil }); err == nil {
		t.Fatal("nil database was accepted")
	}
}

func TestAdvisoryLockIDIsStableAndNamespaced(t *testing.T) {
	first := advisoryLockID("ai-native-paas:migrations:kernel")
	if first != advisoryLockID("ai-native-paas:migrations:kernel") {
		t.Fatal("migration lock ID is unstable")
	}
	if first == advisoryLockID("ai-native-paas:migrations:source") {
		t.Fatal("different components share a migration lock ID")
	}
}
