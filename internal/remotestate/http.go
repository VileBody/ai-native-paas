package remotestate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Claims struct {
	Username        string
	Namespace       string
	TenantID        string
	ProjectID       string
	Actor           string
	ExpiresAt       time.Time
	CanRecoverStale bool
}

type CredentialVerifier interface {
	Verify(context.Context, string, string) (Claims, error)
}

type StaticCredential struct {
	claims       Claims
	usernameHash [sha256.Size]byte
	passwordHash [sha256.Size]byte
}

type CredentialSet struct {
	credentials []*StaticCredential
}

func NewCredentialSet(credentials ...*StaticCredential) (*CredentialSet, error) {
	if len(credentials) == 0 {
		return nil, errors.New("at least one state credential is required")
	}
	for _, credential := range credentials {
		if credential == nil {
			return nil, errors.New("state credential set contains nil credential")
		}
	}
	return &CredentialSet{credentials: append([]*StaticCredential(nil), credentials...)}, nil
}

func (s *CredentialSet) Verify(ctx context.Context, username, password string) (Claims, error) {
	for _, credential := range s.credentials {
		claims, err := credential.Verify(ctx, username, password)
		if err == nil {
			return claims, nil
		}
	}
	return Claims{}, errors.New("invalid or expired state credential")
}

func NewStaticCredential(username, password string, claims Claims) (*StaticCredential, error) {
	if username == "" || password == "" || claims.Namespace == "" || claims.Actor == "" || claims.ExpiresAt.IsZero() {
		return nil, errors.New("complete expiring state credential is required")
	}
	if err := ValidateNamespace(claims.Namespace); err != nil {
		return nil, err
	}
	claims.Username = username
	return &StaticCredential{
		claims:       claims,
		usernameHash: sha256.Sum256([]byte(username)),
		passwordHash: sha256.Sum256([]byte(password)),
	}, nil
}

func (v *StaticCredential) Verify(_ context.Context, username, password string) (Claims, error) {
	usernameHash := sha256.Sum256([]byte(username))
	passwordHash := sha256.Sum256([]byte(password))
	valid := subtle.ConstantTimeCompare(usernameHash[:], v.usernameHash[:]) & subtle.ConstantTimeCompare(passwordHash[:], v.passwordHash[:])
	if valid != 1 || !time.Now().UTC().Before(v.claims.ExpiresAt) {
		return Claims{}, errors.New("invalid or expired state credential")
	}
	return v.claims, nil
}

type Handler struct {
	service  *Service
	verifier CredentialVerifier
}

func NewHandler(service *Service, verifier CredentialVerifier) (*Handler, error) {
	if service == nil || verifier == nil {
		return nil, errors.New("state HTTP handler requires service and credential verifier")
	}
	return &Handler{service: service, verifier: verifier}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/healthz" {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	const prefix = "/api/v1/state/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(response, request)
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(remainder, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] != "lock") {
		http.NotFound(response, request)
		return
	}
	namespace := parts[0]
	if err := ValidateNamespace(namespace); err != nil {
		http.Error(response, "invalid state namespace", http.StatusBadRequest)
		return
	}
	username, password, ok := request.BasicAuth()
	if !ok {
		response.Header().Set("WWW-Authenticate", `Basic realm="opentofu-state"`)
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return
	}
	claims, err := h.verifier.Verify(request.Context(), username, password)
	if err != nil {
		http.Error(response, "invalid or expired credential", http.StatusUnauthorized)
		return
	}
	if claims.Namespace != namespace {
		http.Error(response, "credential scope mismatch", http.StatusForbidden)
		return
	}
	if len(parts) == 2 {
		h.handleLock(response, request, claims)
		return
	}
	h.handleState(response, request, claims)
}

func (h *Handler) handleState(response http.ResponseWriter, request *http.Request, claims Claims) {
	switch request.Method {
	case http.MethodGet:
		blob, err := h.service.Get(request.Context(), claims.Namespace)
		if errors.Is(err, ErrNotFound) {
			http.NotFound(response, request)
			return
		}
		if err != nil {
			http.Error(response, "state read failed", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("ETag", blob.ETag)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(blob.Data)
	case http.MethodPost:
		data, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 64<<20))
		if err != nil {
			http.Error(response, "state payload is too large", http.StatusRequestEntityTooLarge)
			return
		}
		_, err = h.service.Put(request.Context(), claims.Namespace, request.URL.Query().Get("ID"), claims.Actor, data)
		h.writeMutationResult(response, err)
	case http.MethodDelete:
		h.writeMutationResult(response, h.service.Delete(request.Context(), claims.Namespace, request.URL.Query().Get("ID"), claims.Actor))
	default:
		response.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleLock(response http.ResponseWriter, request *http.Request, claims Claims) {
	data, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1<<20))
	if err != nil {
		http.Error(response, "lock payload is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var payload struct {
		Lock
		Reason string `json:"Reason,omitempty"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		http.Error(response, "invalid lock payload", http.StatusBadRequest)
		return
	}
	switch request.Method {
	case "LOCK":
		existing, err := h.service.Acquire(request.Context(), claims.Namespace, claims.Actor, payload.Lock)
		if errors.Is(err, ErrLocked) || errors.Is(err, ErrStaleLock) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusLocked)
			_ = json.NewEncoder(response).Encode(existing)
			return
		}
		h.writeMutationResult(response, err)
	case "UNLOCK":
		h.writeMutationResult(response, h.service.Release(request.Context(), claims.Namespace, payload.ID))
	case "RECOVER":
		h.writeMutationResult(response, h.service.RecoverStale(request.Context(), claims.Namespace, payload.ID, RecoveryAuthorization{
			Allowed: claims.CanRecoverStale,
			Actor:   claims.Actor,
			Reason:  payload.Reason,
		}))
	default:
		response.Header().Set("Allow", "LOCK, UNLOCK, RECOVER")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) writeMutationResult(response http.ResponseWriter, err error) {
	switch {
	case err == nil:
		response.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrLocked), errors.Is(err, ErrStaleLock), errors.Is(err, ErrLockMismatch):
		http.Error(response, "state lock conflict", http.StatusLocked)
	case errors.Is(err, ErrRecoveryForbidden):
		http.Error(response, "stale lock recovery is forbidden", http.StatusForbidden)
	case strings.Contains(err.Error(), "payload"), strings.Contains(err.Error(), "plaintext"), strings.Contains(err.Error(), "lock ID"):
		http.Error(response, err.Error(), http.StatusBadRequest)
	default:
		http.Error(response, "state operation failed", http.StatusInternalServerError)
	}
}
