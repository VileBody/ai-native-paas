package remotestate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryBlobs struct {
	mu   sync.Mutex
	data map[string]Blob
}

func (m *memoryBlobs) Get(_ context.Context, namespace string) (Blob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blob, ok := m.data[namespace]
	if !ok {
		return Blob{}, ErrNotFound
	}
	blob.Data = append([]byte(nil), blob.Data...)
	return blob, nil
}

func (m *memoryBlobs) Put(_ context.Context, namespace string, data []byte) (Blob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blob := Blob{Data: append([]byte(nil), data...), VersionID: "version-1", ETag: Digest(data)}
	m.data[namespace] = blob
	return blob, nil
}

func (m *memoryBlobs) Delete(_ context.Context, namespace string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[namespace]; !ok {
		return ErrNotFound
	}
	delete(m.data, namespace)
	return nil
}

type memoryRepository struct {
	mu    sync.Mutex
	locks map[string]Lock
}

func (m *memoryRepository) Acquire(_ context.Context, namespace string, lock Lock, _ string) (*Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.locks[namespace]; ok && existing.ID != lock.ID {
		copy := existing
		return &copy, ErrLocked
	}
	m.locks[namespace] = lock
	return nil, nil
}

func (m *memoryRepository) Verify(_ context.Context, namespace, lockID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.locks[namespace]; !ok || existing.ID != lockID {
		return ErrLockMismatch
	}
	return nil
}

func (m *memoryRepository) Release(_ context.Context, namespace, lockID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.locks[namespace]; !ok || existing.ID != lockID {
		return ErrLockMismatch
	}
	delete(m.locks, namespace)
	return nil
}

func (m *memoryRepository) Recover(_ context.Context, namespace, lockID, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.locks[namespace]; !ok || existing.ID != lockID {
		return ErrLockMismatch
	}
	delete(m.locks, namespace)
	return nil
}

func (m *memoryRepository) RecordState(context.Context, string, Blob, string) error { return nil }

func encryptedFixture() []byte {
	return []byte(`{"serial":1,"lineage":"lineage","meta":{"key_provider.pbkdf2.offline_recovery":"opaque"},"encryption_version":"v0","encrypted_data":"opaque"}`)
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	service, err := NewService(&memoryBlobs{data: map[string]Blob{}}, &memoryRepository{locks: map[string]Lock{}})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewStaticCredential("admin", "secret", Claims{
		Namespace: "admin", TenantID: "platform", ProjectID: "admin", Actor: "migration", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, credential)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func request(t *testing.T, handler http.Handler, method, target string, body []byte, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestStateHTTP_RequiresVerifiedNamespaceScopeAndExactLock(t *testing.T) {
	handler := testHandler(t)
	if status := request(t, handler, http.MethodGet, "/api/v1/state/admin", nil, "", "").Code; status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	if status := request(t, handler, http.MethodGet, "/api/v1/state/other", nil, "admin", "secret").Code; status != http.StatusForbidden {
		t.Fatalf("cross-namespace status=%d", status)
	}
	lock := []byte(`{"ID":"lock-1","Operation":"OperationTypeApply","Who":"agent"}`)
	if status := request(t, handler, "LOCK", "/api/v1/state/admin/lock", lock, "admin", "secret").Code; status != http.StatusOK {
		t.Fatalf("lock status=%d", status)
	}
	other := []byte(`{"ID":"lock-2","Who":"other-agent"}`)
	if status := request(t, handler, "LOCK", "/api/v1/state/admin/lock", other, "admin", "secret").Code; status != http.StatusLocked {
		t.Fatalf("contended lock status=%d", status)
	}
	if status := request(t, handler, http.MethodPost, "/api/v1/state/admin?ID=lock-2", encryptedFixture(), "admin", "secret").Code; status != http.StatusLocked {
		t.Fatalf("mismatched write lock status=%d", status)
	}
}

func TestStateHTTP_RejectsPlaintextAndRoundTripsEncryptedState(t *testing.T) {
	handler := testHandler(t)
	lock := []byte(`{"ID":"lock-1"}`)
	if status := request(t, handler, "LOCK", "/api/v1/state/admin/lock", lock, "admin", "secret").Code; status != http.StatusOK {
		t.Fatalf("lock status=%d", status)
	}
	plaintext := []byte(`{"version":4,"resources":[{"password":"sentinel"}]}`)
	if status := request(t, handler, http.MethodPost, "/api/v1/state/admin?ID=lock-1", plaintext, "admin", "secret").Code; status != http.StatusBadRequest {
		t.Fatalf("plaintext status=%d", status)
	}
	fixture := encryptedFixture()
	if status := request(t, handler, http.MethodPost, "/api/v1/state/admin?ID=lock-1", fixture, "admin", "secret").Code; status != http.StatusOK {
		t.Fatalf("encrypted write status=%d", status)
	}
	read := request(t, handler, http.MethodGet, "/api/v1/state/admin", nil, "admin", "secret")
	if read.Code != http.StatusOK || !bytes.Equal(read.Body.Bytes(), fixture) {
		t.Fatalf("state read status=%d body=%q", read.Code, read.Body.String())
	}
	if status := request(t, handler, "UNLOCK", "/api/v1/state/admin/lock", []byte(`{"ID":"wrong"}`), "admin", "secret").Code; status != http.StatusLocked {
		t.Fatalf("wrong unlock status=%d", status)
	}
	if status := request(t, handler, "UNLOCK", "/api/v1/state/admin/lock", lock, "admin", "secret").Code; status != http.StatusOK {
		t.Fatalf("unlock status=%d", status)
	}
}

func TestStateHTTP_StaleRecoveryRequiresAuthorizedCredentialAndReason(t *testing.T) {
	repository := &memoryRepository{locks: map[string]Lock{}}
	service, err := NewService(&memoryBlobs{data: map[string]Blob{}}, repository)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewStaticCredential("worker", "worker-secret", Claims{
		Namespace: "admin", Actor: "workspace-agent", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := NewStaticCredential("operator", "operator-secret", Claims{
		Namespace: "admin", Actor: "operator-1", ExpiresAt: time.Now().Add(time.Hour), CanRecoverStale: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := NewCredentialSet(worker, operator)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, credentials)
	if err != nil {
		t.Fatal(err)
	}
	lock := []byte(`{"ID":"lock-1","Who":"workspace-1"}`)
	if status := request(t, handler, "LOCK", "/api/v1/state/admin/lock", lock, "worker", "worker-secret").Code; status != http.StatusOK {
		t.Fatalf("lock status=%d", status)
	}
	recovery := []byte(`{"ID":"lock-1","Reason":"workspace lease expired after provider timeout"}`)
	if status := request(t, handler, "RECOVER", "/api/v1/state/admin/lock", recovery, "worker", "worker-secret").Code; status != http.StatusForbidden {
		t.Fatalf("unprivileged recovery status=%d", status)
	}
	withoutReason := []byte(`{"ID":"lock-1"}`)
	if status := request(t, handler, "RECOVER", "/api/v1/state/admin/lock", withoutReason, "operator", "operator-secret").Code; status != http.StatusBadRequest {
		t.Fatalf("reasonless recovery status=%d", status)
	}
}

func TestStateService_ConcurrentLockHasExactlyOneWinner(t *testing.T) {
	repository := &memoryRepository{locks: map[string]Lock{}}
	service, err := NewService(&memoryBlobs{data: map[string]Blob{}}, repository)
	if err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Acquire(context.Background(), "admin", "actor", Lock{ID: fmt.Sprintf("lock-%d", i)})
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrLocked):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected acquire error: %v", err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 || conflicts.Load() != 31 {
		t.Fatalf("winners=%d conflicts=%d", winners.Load(), conflicts.Load())
	}
}

func TestStaticCredential_ExpiresAndDoesNotAcceptPrefix(t *testing.T) {
	credential, err := NewStaticCredential("admin", "secret", Claims{Namespace: "admin", Actor: "test", ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential.Verify(context.Background(), "admin", "secret"); err == nil {
		t.Fatal("expired credential was accepted")
	}
	credential, err = NewStaticCredential("admin", "secret", Claims{Namespace: "admin", Actor: "test", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential.Verify(context.Background(), "admin", "secret-extra"); err == nil {
		t.Fatal("credential password prefix was accepted")
	}
}

func TestCredentialSet_VerifiesIndependentNamespaceCredentials(t *testing.T) {
	admin, err := NewStaticCredential("admin", "admin-secret", Claims{Namespace: "admin", Actor: "migration", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cozystack, err := NewStaticCredential("cozystack", "cozy-secret", Claims{Namespace: "cozystack-lab", Actor: "bootstrap", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewCredentialSet(admin, cozystack)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := set.Verify(context.Background(), "cozystack", "cozy-secret")
	if err != nil || claims.Namespace != "cozystack-lab" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if _, err := set.Verify(context.Background(), "admin", "cozy-secret"); err == nil {
		t.Fatal("credential components from different namespaces were combined")
	}
}
