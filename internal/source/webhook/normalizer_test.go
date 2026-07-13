package webhook_test

import (
	"testing"
	"time"

	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func TestWebhookNormalizer_Push(t *testing.T) {
	raw := []byte(`{"object_kind":"push","before":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","after":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","ref":"refs/heads/main","project":{"id":42},"event_created_at":"2026-07-12T12:00:00Z"}`)
	n, err := (sourcehook.Normalizer{}).Normalize("e1", raw, time.Now())
	if err != nil || n.Push == nil || n.Push.Branch != "main" || n.Push.ProviderProjectID != 42 {
		t.Fatalf("n=%+v err=%v", n, err)
	}
}
func TestWebhookNormalizer_MergeRequest(t *testing.T) {
	raw := []byte(`{"object_kind":"merge_request","project":{"id":42},"object_attributes":{"iid":7,"action":"update","state":"opened","source_branch":"f","target_branch":"main","last_commit":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`)
	n, err := (sourcehook.Normalizer{}).Normalize("e1", raw, time.Now())
	if err != nil || n.MergeRequest == nil || n.MergeRequest.IID != 7 {
		t.Fatalf("n=%+v err=%v", n, err)
	}
}
func TestWebhookNormalizer_RejectsUnsupportedEvent(t *testing.T) {
	if _, err := (sourcehook.Normalizer{}).Normalize("e", []byte(`{"object_kind":"job"}`), time.Now()); err == nil {
		t.Fatal("expected error")
	}
}
