package webhook_test

import (
	"testing"
	"time"

	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func TestWebhookSignature_ValidStandardSignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"x":1}`)
	ts, sig := sourcehook.Sign([]byte("secret"), "evt-1", now, body)
	v := sourcehook.Verifier{Secret: []byte("secret"), ReplayWindow: time.Minute}
	id, err := v.Verify(map[string][]string{"webhook-id": {"evt-1"}, "webhook-timestamp": {ts}, "webhook-signature": {sig}}, body, now)
	if err != nil || id != "evt-1" {
		t.Fatal(id, err)
	}
}
func TestWebhookSignature_RejectsBodyMutation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts, sig := sourcehook.Sign([]byte("secret"), "evt-1", now, []byte("one"))
	v := sourcehook.Verifier{Secret: []byte("secret")}
	if _, err := v.Verify(map[string][]string{"webhook-id": {"evt-1"}, "webhook-timestamp": {ts}, "webhook-signature": {sig}}, []byte("two"), now); err == nil {
		t.Fatal("expected signature failure")
	}
}
func TestWebhookSignature_RejectsReplayOutsideWindow(t *testing.T) {
	then := time.Unix(1_800_000_000, 0)
	ts, sig := sourcehook.Sign([]byte("secret"), "evt-1", then, []byte("x"))
	v := sourcehook.Verifier{Secret: []byte("secret"), ReplayWindow: time.Minute}
	if _, err := v.Verify(map[string][]string{"webhook-id": {"evt-1"}, "webhook-timestamp": {ts}, "webhook-signature": {sig}}, []byte("x"), then.Add(2*time.Minute)); err == nil {
		t.Fatal("expected replay rejection")
	}
}
func TestWebhookSignature_LegacyDisabledByDefault(t *testing.T) {
	v := sourcehook.Verifier{LegacyToken: "legacy"}
	if _, err := v.Verify(map[string][]string{"X-Gitlab-Token": {"legacy"}}, []byte("x"), time.Now()); err == nil {
		t.Fatal("legacy accepted")
	}
}
func TestWebhookSignature_LegacyExplicitCompatibility(t *testing.T) {
	v := sourcehook.Verifier{AllowLegacy: true, LegacyToken: "legacy"}
	id, err := v.Verify(map[string][]string{"X-Gitlab-Token": {"legacy"}, "X-Gitlab-Event-UUID": {"evt"}}, []byte("x"), time.Now())
	if err != nil || id != "evt" {
		t.Fatal(id, err)
	}
}
