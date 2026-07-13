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
