package openbao

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
)

func TestOpenBaoAdapter_WriteReadMetadataDelete(t *testing.T) {
	backend := NewMemoryBackend()
	adapter := Adapter{Backend: backend}
	path := Path("tenant-1", "app-1", "env-1")
	written, err := adapter.Write(context.Background(), "tenant-1", path, "API_TOKEN", []byte("secret"), time.Now().Add(time.Hour))
	if err != nil || written.Ref == "" {
		t.Fatalf("written=%+v err=%v", written, err)
	}
	metadata, err := adapter.Metadata(context.Background(), "tenant-1", path, "API_TOKEN")
	if err != nil || !metadata.Exists || metadata.Ref == "" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	if err := adapter.Delete(context.Background(), "tenant-1", path, "API_TOKEN"); err != nil {
		t.Fatal(err)
	}
	metadata, err = adapter.Metadata(context.Background(), "tenant-1", path, "API_TOKEN")
	if err != nil || metadata.Exists {
		t.Fatalf("metadata after delete=%+v err=%v", metadata, err)
	}
}

func TestOpenBaoAdapter_UsesTenantScopedPath(t *testing.T) {
	backend := NewMemoryBackend()
	adapter := Adapter{Backend: backend}
	path := Path("tenant-1", "app-1", "env-1")
	if _, err := adapter.Write(context.Background(), "tenant-1", path, "API_TOKEN", []byte("secret"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Metadata(context.Background(), "tenants/tenant-1/apps/app-1/env-1/API_TOKEN"); err != nil {
		t.Fatalf("tenant-scoped backend path missing: %v", err)
	}
}

func TestOpenBaoAdapter_DeniesCrossTenantPath(t *testing.T) {
	adapter := Adapter{Backend: NewMemoryBackend()}
	if _, err := adapter.Write(context.Background(), "tenant-2", Path("tenant-1", "app-1", "env-1"), "API_TOKEN", []byte("secret"), time.Time{}); err == nil {
		t.Fatal("cross-tenant path accepted")
	}
}

type unavailableBackend struct{}

func (unavailableBackend) Put(context.Context, string, []byte, time.Time) (string, error) {
	return "", ErrUnavailable
}
func (unavailableBackend) Metadata(context.Context, string) (Meta, error) {
	return Meta{}, ErrUnavailable
}
func (unavailableBackend) Delete(context.Context, string) error { return ErrUnavailable }

func TestOpenBaoAdapter_MapsUnavailableToRetryableError(t *testing.T) {
	_, err := (Adapter{Backend: unavailableBackend{}}).Write(context.Background(), "tenant-1", Path("tenant-1", "app-1", "env-1"), "API_TOKEN", []byte("secret"), time.Time{})
	var provider *application.ProviderError
	if !errors.As(err, &provider) || !provider.Retryable {
		t.Fatalf("err=%v", err)
	}
}
