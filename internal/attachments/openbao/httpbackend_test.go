package openbao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tokenFile(t *testing.T, value string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(filename, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestKVV2Backend_WriteMetadataAndPermanentlyDeleteAllVersions(t *testing.T) {
	const (
		token  = "workload-token-0123456789"
		secret = "binary-secret-value"
	)
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != token {
			t.Errorf("unexpected workload token header")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/attachments/data/tenants/tenant-1/apps/app-1/env-1/API_TOKEN":
			var payload struct {
				Data struct {
					Value     string `json:"value"`
					Encoding  string `json:"encoding"`
					ExpiresAt string `json:"expires_at"`
				} `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.Data.Value != base64.StdEncoding.EncodeToString([]byte(secret)) || payload.Data.Encoding != "base64" || payload.Data.ExpiresAt == "" {
				t.Errorf("unexpected KV payload metadata: %#v", payload.Data)
			}
			_, _ = w.Write([]byte(`{"data":{"version":2}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/attachments/metadata/tenants/tenant-1/apps/app-1/env-1/API_TOKEN":
			if deleted.Load() {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"current_version":2,"versions":{"1":{"deletion_time":"","destroyed":false},"2":{"deletion_time":"","destroyed":false}}}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/attachments/metadata/tenants/tenant-1/apps/app-1/env-1/API_TOKEN":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected OpenBao request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	backend, err := NewKVV2Backend(KVV2Config{Address: server.URL, TokenFile: tokenFile(t, token), Mount: "attachments", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	secretPath := "tenants/tenant-1/apps/app-1/env-1/API_TOKEN"
	version, err := backend.Put(context.Background(), secretPath, []byte(secret), time.Now().Add(time.Hour))
	if err != nil || version != "2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	metadata, err := backend.Metadata(context.Background(), secretPath)
	if err != nil || !metadata.Exists || metadata.Version != "2" {
		t.Fatalf("metadata=%#v err=%v", metadata, err)
	}
	if err := backend.Delete(context.Background(), secretPath); err != nil {
		t.Fatal(err)
	}
	metadata, err = backend.Metadata(context.Background(), secretPath)
	if err != nil || metadata.Exists {
		t.Fatalf("metadata after permanent delete=%#v err=%v", metadata, err)
	}
}

func TestKVV2Backend_ReloadsRotatedWorkloadTokenAndContainsProviderErrors(t *testing.T) {
	const firstToken = "first-workload-token-012345"
	const secondToken = "second-workload-token-12345"
	filename := tokenFile(t, firstToken)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := requests.Add(1)
		want := firstToken
		if call == 2 {
			want = secondToken
		}
		if r.Header.Get("X-Vault-Token") != want {
			t.Errorf("request %d used stale workload token", call)
		}
		if call == 1 {
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`provider body leaked second-workload-token-12345 binary-secret-value`))
	}))
	defer server.Close()

	backend, err := NewKVV2Backend(KVV2Config{Address: server.URL, TokenFile: filename, Mount: "attachments", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	secretPath := "tenants/tenant-1/apps/app-1/env-1/API_TOKEN"
	if _, err := backend.Put(context.Background(), secretPath, []byte("binary-secret-value"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(secondToken), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Metadata(context.Background(), secretPath)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider outage error=%v", err)
	}
	for _, sentinel := range []string{secondToken, "binary-secret-value", "provider body leaked"} {
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("error leaked %q: %v", sentinel, err)
		}
	}
}

func TestKVV2Backend_RejectsUnsafeTransportPathAndTokenFile(t *testing.T) {
	for _, config := range []KVV2Config{
		{Address: "http://openbao.internal:8200", TokenFile: "/token", Mount: "attachments"},
		{Address: "https://user@openbao.invalid", TokenFile: "/token", Mount: "attachments"},
		{Address: "https://openbao.invalid", TokenFile: "/token", Mount: "../attachments"},
	} {
		if _, err := NewKVV2Backend(config); err == nil {
			t.Fatalf("unsafe configuration accepted: %#v", config)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("request reached OpenBao with insecure token file")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	filename := tokenFile(t, "workload-token-0123456789")
	if err := os.Chmod(filename, 0o644); err != nil {
		t.Fatal(err)
	}
	backend, err := NewKVV2Backend(KVV2Config{Address: server.URL, TokenFile: filename, Mount: "attachments", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Put(context.Background(), "tenants/tenant-1/apps/app-1/env-1/API_TOKEN", []byte("secret"), time.Time{}); err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("insecure token file error=%v", err)
	}
	if _, err := backend.Metadata(context.Background(), "tenants/tenant-1/../API_TOKEN"); err == nil {
		t.Fatal("non-canonical secret path accepted")
	}
}

func TestKVV2Backend_WorkloadTokenSupportsKubernetesFSGroupWithoutWorldAccess(t *testing.T) {
	filename := tokenFile(t, "workload-token-0123456789")
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		token, err := readWorkloadToken(filename)
		if err != nil || string(token) != "workload-token-0123456789" {
			t.Fatalf("secure mode %o rejected: token=%q err=%v", mode, token, err)
		}
		zeroBytes(token)
	}
	for _, mode := range []os.FileMode{0o700, 0o660, 0o644, 0o604} {
		if err := os.Chmod(filename, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readWorkloadToken(filename); err == nil {
			t.Fatalf("unsafe mode %o accepted", mode)
		}
	}
}

func TestCredential_RevokePrefixFailsClosedWhenBackendCannotGuaranteeIt(t *testing.T) {
	backend, err := NewKVV2Backend(KVV2Config{
		Address: "https://openbao.invalid", TokenFile: "/not-read", Mount: "attachments",
		HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(backend).(PrefixBackend); ok {
		t.Fatal("KV v2 backend incorrectly claims atomic prefix deletion")
	}
	adapter := Adapter{Backend: backend}
	if err := adapter.Delete(context.Background(), "tenant-1", "tenants/tenant-1/apps/app-1/env-1/bindings/binding-1", "*"); err == nil {
		t.Fatal("credential prefix revoke did not fail closed")
	}
}
