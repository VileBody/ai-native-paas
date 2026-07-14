package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sequenceIDs struct {
	mu sync.Mutex
	n  int
}

func (i *sequenceIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}

func enrollmentHandler(t *testing.T) (Handler, enrollment.EnrollmentToken) {
	t.Helper()
	clock := fixedClock{now: time.Date(2026, 7, 14, 4, 30, 0, 0, time.UTC)}
	service := &enrollment.Service{
		Store: enrollment.NewMemoryStore(), Clock: clock, IDs: &sequenceIDs{},
		Secrets: enrollment.CryptoSecrets{}, Signer: enrollment.HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")},
	}
	token, err := service.Issue(context.Background(), enrollment.Binding{
		TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1",
		Scopes: []string{"agent.tool:project_get"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return Handler{Enrollment: service}, token
}

func postJSON(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestEnrollmentHTTP_OneTimeExchangeRefreshAndRevoke(t *testing.T) {
	handler, enrollmentToken := enrollmentHandler(t)
	exchange := agentv2.EnrollmentExchange{EnrollmentToken: enrollmentToken.Token, AgentID: "agent-1", PublicKey: "ssh-ed25519 public"}
	response := postJSON(t, handler, "/api/v2/agent/enroll", exchange)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var credential agentv2.EnrollmentCredential
	if err := json.Unmarshal(response.Body.Bytes(), &credential); err != nil || credential.RefreshToken == "" {
		t.Fatalf("credential=%#v err=%v", credential, err)
	}
	if replay := postJSON(t, handler, "/api/v2/agent/enroll", exchange); replay.Code != http.StatusUnauthorized || bytes.Contains(replay.Body.Bytes(), []byte(enrollmentToken.Token)) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	access := postJSON(t, handler, "/api/v2/agent/token", agentv2.RefreshExchange{RefreshToken: credential.RefreshToken})
	var accessCredential agentv2.AccessCredential
	if access.Code != http.StatusOK || json.Unmarshal(access.Body.Bytes(), &accessCredential) != nil || accessCredential.AccessToken == "" || accessCredential.ExpiresIn != 900 {
		t.Fatalf("access status=%d credential=%#v", access.Code, accessCredential)
	}
	if revoke := postJSON(t, handler, "/api/v2/agent/revoke", agentv2.RefreshExchange{RefreshToken: credential.RefreshToken}); revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	if denied := postJSON(t, handler, "/api/v2/agent/token", agentv2.RefreshExchange{RefreshToken: credential.RefreshToken}); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked refresh status=%d", denied.Code)
	}
}

func TestEnrollmentHTTP_StrictBodyAndGenericErrors(t *testing.T) {
	handler, token := enrollmentHandler(t)
	raw := []byte(`{"enrollment_token":"` + token.Token + `","agent_id":"agent-1","public_key":"key","admin":true}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v2/agent/enroll", bytes.NewReader(raw))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte(token.Token)) {
		t.Fatalf("strict response=%d body=%s", response.Code, response.Body.String())
	}
}
