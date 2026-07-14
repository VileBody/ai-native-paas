// Package httpapi exposes the credential-authenticated agent enrollment flow.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

type Handler struct {
	Enrollment   *enrollment.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/agent/enroll":
		h.enroll(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/agent/token":
		h.access(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/agent/revoke":
		h.revoke(w, r)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h Handler) enroll(w http.ResponseWriter, r *http.Request) {
	if h.Enrollment == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "agent enrollment unavailable")
		return
	}
	var request agentv2.EnrollmentExchange
	if err := decode(r, h.limit(), &request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid enrollment exchange")
		return
	}
	credential, err := h.Enrollment.Exchange(r.Context(), request.EnrollmentToken, request.AgentID, request.PublicKey)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "enrollment credential is invalid or expired")
		return
	}
	writeJSON(w, http.StatusCreated, agentv2.EnrollmentCredential{
		CredentialID: credential.CredentialID,
		RefreshToken: credential.RefreshToken,
		ExpiresAt:    credential.ExpiresAt,
	})
}

func (h Handler) access(w http.ResponseWriter, r *http.Request) {
	if h.Enrollment == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "agent enrollment unavailable")
		return
	}
	var request agentv2.RefreshExchange
	if err := decode(r, h.limit(), &request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid refresh exchange")
		return
	}
	token, claims, err := h.Enrollment.Access(r.Context(), request.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "refresh credential is invalid, expired, or revoked")
		return
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	writeJSON(w, http.StatusOK, agentv2.AccessCredential{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   claims.ExpiresAt - claims.IssuedAt,
		Claims: agentv2.AccessCredentialClaims{
			TenantID: claims.TenantID, ProjectID: claims.ProjectID, UserID: claims.UserID,
			AgentID: claims.AgentID, Scopes: append([]string(nil), claims.Scopes...), ExpiresAt: expiresAt,
		},
	})
}

func (h Handler) revoke(w http.ResponseWriter, r *http.Request) {
	if h.Enrollment == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "agent enrollment unavailable")
		return
	}
	var request agentv2.RefreshExchange
	if err := decode(r, h.limit(), &request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid refresh credential")
		return
	}
	if err := h.Enrollment.Revoke(r.Context(), request.RefreshToken); err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "refresh credential is invalid")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 || h.MaxBodyBytes > 1<<20 {
		return 64 << 10
	}
	return h.MaxBodyBytes
}

func decode(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func BearerToken(header string) string {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}
