package cache_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/cache"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func TestBuildCache_ProjectScopePreventsCrossTenantRead(t *testing.T) {
	c := cache.New()
	raw := []byte("layer")
	entry := cache.Entry{TenantID: "t1", ProjectID: "p1", Key: "deps", Scope: cache.ScopeProject, Digest: digest(raw), Payload: raw}
	if err := c.Put(entry); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get("t2", "p1", "deps", cache.ScopeProject); ok {
		t.Fatal("cross-tenant cache read succeeded")
	}
}
func TestBuildCache_TrustedBaseLayersMayBeShared(t *testing.T) {
	c := cache.New()
	raw := []byte("trusted")
	if err := c.Put(cache.Entry{Key: "go-run-image", Scope: cache.ScopeTrustedBase, Digest: digest(raw), Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Get("any", "project", "go-run-image", cache.ScopeTrustedBase); err != nil || !ok {
		t.Fatal(ok, err)
	}
}
func TestBuildCache_SecretMaterialIsNeverCached(t *testing.T) {
	c := cache.New()
	raw := []byte("x")
	err := c.Put(cache.Entry{TenantID: "t", ProjectID: "p", Key: "k", Scope: cache.ScopeProject, Digest: digest(raw), Payload: raw, Metadata: map[string]string{"npm_token": "secret"}})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
func TestBuildCache_PoisonedEntryIsRejectedByDigest(t *testing.T) {
	c := cache.New()
	raw := []byte("good")
	entry := cache.Entry{TenantID: "t", ProjectID: "p", Key: "k", Scope: cache.ScopeProject, Digest: digest(raw), Payload: []byte("poison")}
	c.UnsafeCorruptForTest(entry)
	_, _, err := c.Get("t", "p", "k", cache.ScopeProject)
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuild_BuildSecretIsAbsentFromCacheMetadata(t *testing.T) {
	c := cache.New()
	raw := []byte("safe-layer")
	err := c.PutWithSecrets(cache.Entry{TenantID: "t", ProjectID: "p", Key: "deps", Scope: cache.ScopeProject, Digest: digest(raw), Payload: raw, Metadata: map[string]string{"registry_auth": "Bearer build-secret"}}, []string{"build-secret"})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
	if _, ok, getErr := c.Get("t", "p", "deps", cache.ScopeProject); getErr != nil || ok {
		t.Fatalf("secret-bearing entry stored: ok=%v err=%v", ok, getErr)
	}
}

func TestBuild_CacheIsProjectScopedForUntrustedLayers(t *testing.T) {
	buildCache := cache.New()
	marker := []byte("untrusted-project-a-layer-marker")
	if err := buildCache.Put(cache.Entry{TenantID: "tenant-1", ProjectID: "project-a", Key: "dependency-layer", Scope: cache.ScopeProject, Digest: digest(marker), Payload: marker}); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ tenant, project string }{{"tenant-1", "project-b"}, {"tenant-2", "project-a"}} {
		if _, found, err := buildCache.Get(probe.tenant, probe.project, "dependency-layer", cache.ScopeProject); err != nil || found {
			t.Fatalf("probe=%+v found=%v err=%v", probe, found, err)
		}
	}
	trusted := []byte("verified-global-base-layer")
	if err := buildCache.Put(cache.Entry{Key: "go-runtime@sha256:trusted", Scope: cache.ScopeTrustedBase, Digest: digest(trusted), Payload: trusted}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"project-a", "project-b"} {
		entry, found, err := buildCache.Get("tenant-1", project, "go-runtime@sha256:trusted", cache.ScopeTrustedBase)
		if err != nil || !found || string(entry.Payload) != string(trusted) {
			t.Fatalf("trusted base project=%s found=%v err=%v entry=%+v", project, found, err, entry)
		}
	}
}
