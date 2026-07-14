package openbao_test

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
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	openbao "github.com/keir-research/ai-native-paas/internal/attachments/openbao"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func TestSecret_ProjectAndEnvironmentScopesAreIsolated(t *testing.T) {
	const token = "project-workload-token-012345"

	type openBaoState struct {
		sync.Mutex
		values map[string][]byte
	}
	state := &openBaoState{values: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, "/v1/attachments/data/") {
			http.NotFound(w, request)
			return
		}
		if request.Header.Get("X-Vault-Token") != token {
			t.Errorf("OpenBao request did not use the scoped workload token")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var payload struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		value, err := base64.StdEncoding.DecodeString(payload.Data.Value)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		path := strings.TrimPrefix(request.URL.Path, "/v1/attachments/data/")
		state.Lock()
		state.values[path] = append([]byte(nil), value...)
		state.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	defer server.Close()

	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, err := openbao.NewKVV2Backend(openbao.KVV2Config{
		Address: server.URL, TokenFile: tokenPath, Mount: "attachments", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock()
	service := &application.Service{
		Store: memory.New(),
		Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{
			"env-alpha": {TenantID: "tenant-1", ApplicationID: "project-alpha", EnvironmentID: "env-alpha", Ready: true},
			"env-beta":  {TenantID: "tenant-1", ApplicationID: "project-beta", EnvironmentID: "env-beta", Ready: true},
		}},
		Secrets: openbao.Adapter{Backend: backend},
		Clock:   clock,
		IDs:     &application.SequentialIDs{},
	}

	for _, secret := range []struct {
		project, environment, value, key string
	}{
		{project: "project-alpha", environment: "env-alpha", value: "alpha-build-token", key: "alpha-set"},
		{project: "project-beta", environment: "env-beta", value: "beta-build-token", key: "beta-set"},
	} {
		_, _, err = service.SetSecret(context.Background(), application.SetSecretRequest{
			TenantID:       "tenant-1",
			ApplicationID:  secret.project,
			EnvironmentID:  secret.environment,
			Name:           "NPM_TOKEN",
			Scope:          attachmentsv1.SecretScopeBuild,
			Phase:          attachmentsv1.SecretPhaseBuild,
			Value:          []byte(secret.value),
			ExpiresAt:      clock.Now().Add(time.Hour),
			ActorID:        "agent-1",
			IdempotencyKey: secret.key,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	alphaRefs, err := service.ResolveProjectBuildSecretRefs(context.Background(), "tenant-1", "project-alpha", "env-alpha", attachmentsv1.SecretPhaseBuild)
	if err != nil {
		t.Fatal(err)
	}
	betaRefs, err := service.ResolveProjectBuildSecretRefs(context.Background(), "tenant-1", "project-beta", "env-beta", attachmentsv1.SecretPhaseBuild)
	if err != nil {
		t.Fatal(err)
	}
	wantAlpha := "tenants/tenant-1/apps/project-alpha/env-alpha/NPM_TOKEN"
	wantBeta := "tenants/tenant-1/apps/project-beta/env-beta/NPM_TOKEN"
	if alphaRefs["NPM_TOKEN"] != wantAlpha || betaRefs["NPM_TOKEN"] != wantBeta || alphaRefs["NPM_TOKEN"] == betaRefs["NPM_TOKEN"] {
		t.Fatalf("project-scoped refs are not isolated: alpha=%q beta=%q", alphaRefs["NPM_TOKEN"], betaRefs["NPM_TOKEN"])
	}

	for _, crossScope := range []struct {
		project, environment string
	}{
		{project: "project-alpha", environment: "env-beta"},
		{project: "project-beta", environment: "env-alpha"},
	} {
		refs, resolveErr := service.ResolveProjectBuildSecretRefs(context.Background(), "tenant-1", crossScope.project, crossScope.environment, attachmentsv1.SecretPhaseBuild)
		var domainErr *domain.Error
		if !errors.As(resolveErr, &domainErr) || domainErr.Code != domain.CodeForbidden || refs != nil {
			t.Fatalf("cross-project environment resolved: project=%s environment=%s refs=%v err=%v", crossScope.project, crossScope.environment, refs, resolveErr)
		}
	}

	state.Lock()
	defer state.Unlock()
	if string(state.values[wantAlpha]) != "alpha-build-token" || string(state.values[wantBeta]) != "beta-build-token" {
		t.Fatalf("OpenBao paths crossed project boundaries: %#v", state.values)
	}
}
