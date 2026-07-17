package harbor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/harbor"
)

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestResolve_VerifiesTenantPathAndDigest(t *testing.T) {
	artifactDigest := digest([]byte("artifact"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/tenants/t1/apps/p1/manifests/"+artifactDigest || r.Method != http.MethodGet {
			t.Fatalf("request %s %s", r.Method, r.URL)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "robot$t1" || password != "secret" {
			t.Fatalf("robot authentication was not sent")
		}
		w.Header().Set("Docker-Content-Digest", artifactDigest)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	registry, err := harbor.New(server.URL, "robot$t1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	registry.HTTPClient = server.Client()
	repository := strings.TrimPrefix(server.URL, "https://") + "/tenants/t1/apps/p1"
	resolved, err := registry.Resolve(context.Background(), "t1", repository+"@"+artifactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Repository != repository || resolved.Digest != artifactDigest {
		t.Fatalf("resolved=%+v", resolved)
	}
	if _, err := registry.Resolve(context.Background(), "t2", repository+"@"+artifactDigest); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("cross-tenant resolve error=%v", err)
	}
}

func TestStoreAttachment_PushesOCIReferrerBoundToImmutableSubject(t *testing.T) {
	subjectDigest := digest([]byte("artifact"))
	attachment := []byte(`{"spdxVersion":"SPDX-2.3"}`)
	attachmentDigest := digest(attachment)
	emptyDigest := digest([]byte("{}"))
	stage := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/v2/tenants/t1/apps/p1/"
		switch stage {
		case 0:
			if r.Method != http.MethodGet || r.URL.Path != base+"manifests/"+subjectDigest {
				t.Fatalf("subject request %s %s", r.Method, r.URL)
			}
			w.Header().Set("Docker-Content-Digest", subjectDigest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.WriteHeader(http.StatusOK)
		case 1, 3:
			if r.Method != http.MethodPost || r.URL.Path != base+"blobs/uploads" {
				t.Fatalf("upload start %d: %s %s", stage, r.Method, r.URL)
			}
			w.Header().Set("Location", "/uploads/"+string(rune('a'+stage)))
			w.WriteHeader(http.StatusAccepted)
		case 2:
			if r.Method != http.MethodPut || r.URL.Path != "/uploads/b" || r.URL.Query().Get("digest") != emptyDigest {
				t.Fatalf("empty upload %s", r.URL)
			}
			w.Header().Set("Docker-Content-Digest", emptyDigest)
			w.WriteHeader(http.StatusCreated)
		case 4:
			if r.Method != http.MethodPut || r.URL.Path != "/uploads/d" || r.URL.Query().Get("digest") != attachmentDigest {
				t.Fatalf("attachment upload %s", r.URL)
			}
			w.Header().Set("Docker-Content-Digest", attachmentDigest)
			w.WriteHeader(http.StatusCreated)
		case 5:
			if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, base+"manifests/paas-att-") {
				t.Fatalf("manifest request %s %s", r.Method, r.URL)
			}
			var manifest struct {
				Subject struct {
					Digest string `json:"digest"`
				} `json:"subject"`
				Layers []struct {
					Digest string `json:"digest"`
				} `json:"layers"`
				ArtifactType string `json:"artifactType"`
			}
			if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Subject.Digest != subjectDigest || len(manifest.Layers) != 1 || manifest.Layers[0].Digest != attachmentDigest || manifest.ArtifactType != "application/spdx+json" {
				t.Fatalf("manifest=%+v", manifest)
			}
			w.Header().Set("Docker-Content-Digest", digest([]byte("manifest")))
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		stage++
	}))
	defer server.Close()
	registry, err := harbor.New(server.URL, "robot$t1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	registry.HTTPClient = server.Client()
	repository := strings.TrimPrefix(server.URL, "https://") + "/tenants/t1/apps/p1"
	result, err := registry.StoreAttachment(context.Background(), "t1", application.PublishedArtifact{Repository: repository, Digest: subjectDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}, "application/spdx+json", attachment)
	if err != nil || result != attachmentDigest || stage != 6 {
		t.Fatalf("result=%s stage=%d err=%v", result, stage, err)
	}
}
