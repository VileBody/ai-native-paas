package openbao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
)

var ErrUnavailable = errors.New("openbao unavailable")

type Meta struct {
	Version string
	Exists  bool
}
type Backend interface {
	Put(context.Context, string, []byte, time.Time) (string, error)
	Metadata(context.Context, string) (Meta, error)
	Delete(context.Context, string) error
}

// PrefixBackend is implemented by backends that can revoke every credential
// material below one binding path atomically. Production backends should
// implement this interface; falling back to deleting a literal "*" would
// silently leave live credentials behind.
type PrefixBackend interface {
	DeletePrefix(context.Context, string) error
}
type Adapter struct{ Backend Backend }

func path(t, p, n string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if t == "" || !strings.HasPrefix(p+"/", "tenants/"+t+"/") || strings.Contains(p, "..") || strings.ContainsAny(n, "/\\\x00\r\n") {
		return "", domain.NewError(domain.CodeForbidden, "secret path outside tenant")
	}
	return p + "/" + n, nil
}
func mapErr(e error) error {
	if errors.Is(e, ErrUnavailable) {
		return &application.ProviderError{Code: "OPENBAO_UNAVAILABLE", Retryable: true, Cause: e}
	}
	return &application.ProviderError{Code: "OPENBAO_FAILED", Cause: e}
}
func (a Adapter) Write(c context.Context, t, p, n string, v []byte, x time.Time) (application.SecretWriteResult, error) {
	full, e := path(t, p, n)
	if e != nil {
		return application.SecretWriteResult{}, e
	}
	ver, e := a.Backend.Put(c, full, append([]byte(nil), v...), x)
	if e != nil {
		return application.SecretWriteResult{}, mapErr(e)
	}
	return application.SecretWriteResult{Ref: full, Version: ver}, nil
}
func (a Adapter) Metadata(c context.Context, t, p, n string) (application.SecretProviderMetadata, error) {
	full, e := path(t, p, n)
	if e != nil {
		return application.SecretProviderMetadata{}, e
	}
	m, e := a.Backend.Metadata(c, full)
	if e != nil {
		return application.SecretProviderMetadata{}, mapErr(e)
	}
	return application.SecretProviderMetadata{Ref: full, Version: m.Version, Exists: m.Exists}, nil
}
func (a Adapter) Delete(c context.Context, t, p, n string) error {
	if a.Backend == nil {
		return domain.NewError(domain.CodeUnavailable, "secret backend unavailable")
	}
	if n == "*" {
		prefix, e := path(t, p, "credential")
		if e != nil {
			return e
		}
		prefix = strings.TrimSuffix(prefix, "/credential") + "/"
		backend, ok := a.Backend.(PrefixBackend)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "secret backend does not support prefix deletion")
		}
		if e = backend.DeletePrefix(c, prefix); e != nil {
			return mapErr(e)
		}
		return nil
	}
	full, e := path(t, p, n)
	if e != nil {
		return e
	}
	if e = a.Backend.Delete(c, full); e != nil {
		return mapErr(e)
	}
	return nil
}
func Path(t, a, e string) string { return fmt.Sprintf("tenants/%s/apps/%s/%s", t, a, e) }

type entry struct {
	v []byte
	n int
}
type MemoryBackend struct {
	mu     sync.Mutex
	Values map[string]entry
	Err    error
}

func NewMemoryBackend() *MemoryBackend { return &MemoryBackend{Values: map[string]entry{}} }
func (b *MemoryBackend) Put(_ context.Context, p string, v []byte, _ time.Time) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return "", b.Err
	}
	e := b.Values[p]
	e.n++
	e.v = append([]byte(nil), v...)
	b.Values[p] = e
	return fmt.Sprint(e.n), nil
}
func (b *MemoryBackend) Metadata(_ context.Context, p string) (Meta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return Meta{}, b.Err
	}
	e, ok := b.Values[p]
	return Meta{Version: fmt.Sprint(e.n), Exists: ok}, nil
}
func (b *MemoryBackend) Delete(_ context.Context, p string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return b.Err
	}
	delete(b.Values, p)
	return nil
}
func (b *MemoryBackend) DeletePrefix(_ context.Context, prefix string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return b.Err
	}
	for p := range b.Values {
		if strings.HasPrefix(p, prefix) {
			delete(b.Values, p)
		}
	}
	return nil
}
