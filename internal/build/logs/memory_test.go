package logs_test

import (
	"context"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/logs"
)

func TestBuild_BuildSecretIsAbsentFromLogs(t *testing.T) {
	store := logs.New()
	writer := store.Writer("b1", []string{"super-secret"})
	_, _ = writer.Write([]byte("token=super-secret\n"))
	raw, err := store.Read(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("logs=%s", raw)
	}
}

func TestBuild_BuildSecretSplitAcrossWriterChunksIsRedacted(t *testing.T) {
	store := logs.New()
	writer := store.Writer("b1", []string{"super-secret"})
	_, _ = writer.Write([]byte("token=super-"))
	intermediate, err := store.Read(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(intermediate), "super-") {
		t.Fatalf("partial secret leaked during streaming read: %s", intermediate)
	}
	_, _ = writer.Write([]byte("secret done\n"))
	raw, err := store.Read(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") || strings.Contains(string(raw), "super-") || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("split secret leaked: %s", raw)
	}
}

func TestBuild_LogRedactionHandlesOverlappingSecrets(t *testing.T) {
	store := logs.New()
	writer := store.Writer("b1", []string{"aaaa", "aa"})
	_, _ = writer.Write([]byte("value=aaaaa"))
	raw, err := store.Read(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "aa") {
		t.Fatalf("overlapping secret leaked: %s", raw)
	}
}
