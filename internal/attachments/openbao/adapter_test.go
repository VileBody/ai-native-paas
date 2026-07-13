package openbao

import (
	"context"
	"testing"
	"time"
)

func TestAdapter_DeleteWildcardRemovesEveryCredentialBelowBinding(t *testing.T) {
	backend := NewMemoryBackend()
	adapter := Adapter{Backend: backend}
	path := "tenants/tenant-a/apps/app-a/env-a/bindings/bind-1"
	for _, name := range []string{"DATABASE_URL", "PGUSER", "PGPASSWORD"} {
		if _, err := adapter.Write(context.Background(), "tenant-a", path, name, []byte("secret"), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.Delete(context.Background(), "tenant-a", path, "*"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DATABASE_URL", "PGUSER", "PGPASSWORD"} {
		metadata, err := adapter.Metadata(context.Background(), "tenant-a", path, name)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Exists {
			t.Fatalf("credential %s still exists", name)
		}
	}
}

type noPrefixBackend struct{ backend *MemoryBackend }

func (b noPrefixBackend) Put(ctx context.Context, path string, value []byte, expires time.Time) (string, error) {
	return b.backend.Put(ctx, path, value, expires)
}
func (b noPrefixBackend) Metadata(ctx context.Context, path string) (Meta, error) {
	return b.backend.Metadata(ctx, path)
}
func (b noPrefixBackend) Delete(ctx context.Context, path string) error {
	return b.backend.Delete(ctx, path)
}

func TestAdapter_DeleteWildcardFailsClosedWithoutPrefixPrimitive(t *testing.T) {
	backend := noPrefixBackend{backend: NewMemoryBackend()}
	adapter := Adapter{Backend: backend}
	path := "tenants/tenant-a/apps/app-a/env-a/bindings/bind-1"
	if _, err := adapter.Write(context.Background(), "tenant-a", path, "DATABASE_URL", []byte("secret"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Delete(context.Background(), "tenant-a", path, "*"); err == nil {
		t.Fatal("expected wildcard deletion to fail closed")
	}
	metadata, err := adapter.Metadata(context.Background(), "tenant-a", path, "DATABASE_URL")
	if err != nil || !metadata.Exists {
		t.Fatalf("credential unexpectedly removed: metadata=%+v err=%v", metadata, err)
	}
}
