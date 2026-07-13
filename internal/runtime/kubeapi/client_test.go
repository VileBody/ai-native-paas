package kubeapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type capturedRequest struct {
	Method, Path, Query, ContentType, Authorization string
	Body                                            []byte
}

type fakeAPIServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	handler  func(http.ResponseWriter, *http.Request, []byte)
}

func newFakeAPI(t *testing.T, handler func(http.ResponseWriter, *http.Request, []byte)) *fakeAPIServer {
	t.Helper()
	f := &fakeAPIServer{handler: handler}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, capturedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type"), Authorization: r.Header.Get("Authorization"), Body: raw})
		f.mu.Unlock()
		if f.handler != nil {
			f.handler(w, r, raw)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakeAPIServer) last(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request captured")
	}
	return f.requests[len(f.requests)-1]
}
func newTestClient(t *testing.T, f *fakeAPIServer) *Client {
	t.Helper()
	c, err := New(Config{Server: f.server.URL, Token: "top-secret-token", HTTPClient: f.server.Client(), RequestTimeout: time.Second, AllowedServerNames: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClient_RejectsServerOutsideAllowlist(t *testing.T) {
	_, err := New(Config{Server: "https://kubernetes.internal", AllowedServerNames: []string{"api.allowed.test"}})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_UpsertDeploymentUsesServerSideApplyAndSecurityFields(t *testing.T) {
	f := newFakeAPI(t, nil)
	c := newTestClient(t, f)
	replicas := 1
	dep := kube.Deployment{
		Metadata: kube.Metadata{Name: "web-a1", Namespace: "app-1", Labels: map[string]string{"platform.example.com/release-id": "rel-1", "platform.example.com/process": "web"}},
		Spec:     kube.DeploymentSpec{Replicas: &replicas, ProgressDeadlineSeconds: 300, Pod: kube.PodSpec{RuntimeClassName: "gvisor", AutomountServiceAccountToken: false, Containers: []kube.Container{{Name: "web", Image: "registry.test/app@sha256:" + strings.Repeat("a", 64), Port: 8080, HealthPath: "/health", StartupTimeout: 60, ReadinessTimeout: 30, SecurityContext: kube.SecurityContext{RunAsNonRoot: true, AllowPrivilegeEscalation: false, ReadOnlyRootFilesystem: true, SeccompProfile: "RuntimeDefault", DropCapabilities: []string{"ALL"}}, Resources: kube.ResourceRequirements{Requests: runtimev1.ResourceQuantity{CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"}, Limits: runtimev1.ResourceQuantity{CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"}}}}, Volumes: []kube.Volume{{Name: "tmp", EmptyDirSizeLimit: "1Gi"}}}},
	}
	if err := c.UpsertDeployment(context.Background(), dep); err != nil {
		t.Fatal(err)
	}
	r := f.last(t)
	if r.Method != http.MethodPatch || r.Path != "/apis/apps/v1/namespaces/app-1/deployments/web-a1" || r.ContentType != "application/apply-patch+yaml" || r.Authorization != "Bearer top-secret-token" {
		t.Fatalf("request=%+v", r)
	}
	q, _ := url.ParseQuery(r.Query)
	if q.Get("fieldManager") != "paas-runtime-operator" || q.Get("force") != "true" {
		t.Fatalf("query=%s", r.Query)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	raw := string(r.Body)
	for _, want := range []string{`"runtimeClassName":"gvisor"`, `"automountServiceAccountToken":false`, `"runAsNonRoot":true`, `"allowPrivilegeEscalation":false`, `"readOnlyRootFilesystem":true`, `"ephemeral-storage":"1Gi"`, `"image":"registry.test/app@sha256:`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(raw, ":latest") {
		t.Fatalf("mutable tag serialized: %s", raw)
	}
}

func TestClient_GetPaaSApp404IsNotFoundWithoutError(t *testing.T) {
	f := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { http.NotFound(w, nil) })
	c := newTestClient(t, f)
	_, found, err := c.GetPaaSApp(context.Background(), application.RuntimeObjectRef{CellID: "cell-a", Namespace: "app-1", Name: "booking"})
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestClient_ErrorRedactsBearerToken(t *testing.T) {
	f := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`denied Bearer top-secret-token top-secret-token`))
	})
	c := newTestClient(t, f)
	_, _, err := c.GetService(context.Background(), "app-1", "web")
	if !domain.HasCode(err, domain.CodePolicyRejected) || strings.Contains(err.Error(), "top-secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_UpdatePaaSAppStatusUsesStatusSubresource(t *testing.T) {
	f := newFakeAPI(t, nil)
	c := newTestClient(t, f)
	status := runtimev1.PaaSAppStatus{ObservedGeneration: 3, Phase: "Ready", ActiveReleaseID: "rel-1", URL: "https://booking.test", ReadyReplicas: 2}
	if err := c.UpdatePaaSAppStatus(context.Background(), "app-1", "booking", status); err != nil {
		t.Fatal(err)
	}
	r := f.last(t)
	if r.Method != http.MethodPatch || r.Path != "/apis/platform.example.com/v1alpha1/namespaces/app-1/paasapps/booking/status" || r.ContentType != "application/merge-patch+json" {
		t.Fatalf("request=%+v", r)
	}
	if !strings.Contains(string(r.Body), `"status"`) || !strings.Contains(string(r.Body), `"activeReleaseID":"rel-1"`) {
		t.Fatalf("body=%s", r.Body)
	}
}

func TestClient_ListDeploymentsSortsAndDecodesStatus(t *testing.T) {
	f := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"z","namespace":"app-1"},"spec":{"replicas":2,"progressDeadlineSeconds":300},"status":{"readyReplicas":1}},{"metadata":{"name":"a","namespace":"app-1"},"spec":{"replicas":1},"status":{"readyReplicas":1,"conditions":[{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded","message":"timeout"}]}}]}`))
	})
	c := newTestClient(t, f)
	values, err := c.ListDeployments(context.Background(), "app-1", map[string]string{"platform.example.com/application-id": "app-1", "platform.example.com/release-id": "rel-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Metadata.Name != "a" || !values[0].Status.Failed || values[0].Status.Message != "timeout" || values[1].Status.ReadyReplicas != 1 {
		t.Fatalf("values=%+v", values)
	}
	r := f.last(t)
	q, _ := url.ParseQuery(r.Query)
	if q.Get("labelSelector") != "platform.example.com/application-id=app-1,platform.example.com/release-id=rel-1" {
		t.Fatalf("query=%s", r.Query)
	}
}

func TestClient_NetworkPolicySerializesGatewayDNSAndExplicitProfile(t *testing.T) {
	f := newFakeAPI(t, nil)
	c := newTestClient(t, f)
	value := kube.NetworkPolicy{Metadata: kube.Metadata{Name: "platform-default-deny", Namespace: "app-1", Labels: map[string]string{"platform.example.com/application-id": "app-1"}}, DefaultDeny: true, AllowedIngress: []string{"platform-gateway"}, AllowedEgress: []string{"deny-all"}}
	if err := c.UpsertNetworkPolicy(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	r := f.last(t)
	raw := string(r.Body)
	for _, want := range []string{`"kind":"NetworkPolicy"`, `"kubernetes.io/metadata.name":"platform-gateway"`, `"kubernetes.io/metadata.name":"kube-system"`, `"port":53`, `"policyTypes":["Ingress","Egress"]`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	// deny-all contains only the DNS egress rule; there is no empty allow-all rule.
	var object map[string]any
	if err := json.Unmarshal(r.Body, &object); err != nil {
		t.Fatal(err)
	}
	spec := object["spec"].(map[string]any)
	egress := spec["egress"].([]any)
	if len(egress) != 1 || len(egress[0].(map[string]any)) == 0 {
		t.Fatalf("unexpected egress policy: %s", raw)
	}
}

func TestClient_RequestTimeoutIsEnforced(t *testing.T) {
	f := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	c, err := New(Config{Server: f.server.URL, HTTPClient: f.server.Client(), RequestTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.GetService(context.Background(), "app-1", "web")
	if !domain.HasCode(err, domain.CodeUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
