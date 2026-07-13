package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

type Scope string

const (
	ScopeProject     Scope = "project"
	ScopeTrustedBase Scope = "trusted-base"
)

type Entry struct {
	TenantID, ProjectID, Key, Digest string
	Scope                            Scope
	Payload                          []byte
	Metadata                         map[string]string
}
type Cache struct {
	mu      sync.Mutex
	entries map[string]Entry
}

func New() *Cache                      { return &Cache{entries: map[string]Entry{}} }
func (c *Cache) Put(entry Entry) error { return c.PutWithSecrets(entry, nil) }

func (c *Cache) PutWithSecrets(entry Entry, secretValues []string) error {
	if !buildv1.ValidDigest(entry.Digest) {
		return domain.NewError(domain.CodeInvalidArgument, "invalid cache digest")
	}
	if digest(entry.Payload) != entry.Digest {
		return domain.NewError(domain.CodeConflict, "cache payload digest mismatch")
	}
	if entry.Scope == ScopeProject && (entry.TenantID == "" || entry.ProjectID == "") {
		return domain.NewError(domain.CodeInvalidArgument, "project cache requires tenant and project")
	}
	for key, value := range entry.Metadata {
		if sensitive(key) || containsAny(value, secretValues) {
			return domain.NewError(domain.CodeInvalidArgument, "secret material cannot be cached")
		}
	}
	if containsAny(string(entry.Payload), secretValues) {
		return domain.NewError(domain.CodeInvalidArgument, "secret material cannot be cached")
	}
	entry.Payload = append([]byte(nil), entry.Payload...)
	entry.Metadata = clone(entry.Metadata)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cacheKey(entry)] = entry
	return nil
}
func (c *Cache) Get(tenant, project, key string, scope Scope) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	probe := Entry{TenantID: tenant, ProjectID: project, Key: key, Scope: scope}
	if scope == ScopeTrustedBase {
		probe.TenantID = ""
		probe.ProjectID = ""
	}
	entry, ok := c.entries[cacheKey(probe)]
	if !ok {
		return Entry{}, false, nil
	}
	if digest(entry.Payload) != entry.Digest {
		return Entry{}, false, domain.NewError(domain.CodeConflict, "poisoned cache entry")
	}
	entry.Payload = append([]byte(nil), entry.Payload...)
	entry.Metadata = clone(entry.Metadata)
	return entry, true, nil
}
func (c *Cache) UnsafeCorruptForTest(entry Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cacheKey(entry)] = entry
}
func cacheKey(e Entry) string {
	if e.Scope == ScopeTrustedBase {
		return string(e.Scope) + "\x00" + e.Key
	}
	return string(e.Scope) + "\x00" + e.TenantID + "\x00" + e.ProjectID + "\x00" + e.Key
}
func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func sensitive(key string) bool {
	for _, x := range []string{"secret", "token", "password", "credential"} {
		if containsFold(key, x) {
			return true
		}
	}
	return false
}
func containsFold(v, fragment string) bool {
	if len(fragment) > len(v) {
		return false
	}
	for i := 0; i+len(fragment) <= len(v); i++ {
		match := true
		for j := range fragment {
			a := v[i+j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if a != fragment[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
func containsAny(value string, forbidden []string) bool {
	for _, item := range forbidden {
		if item != "" && strings.Contains(value, item) {
			return true
		}
	}
	return false
}

func clone(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
