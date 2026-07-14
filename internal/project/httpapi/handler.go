// Package httpapi exposes the controlled-beta project management API.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

type Handler struct {
	Projects       *projectapp.Service
	Infrastructure *infraapp.Service
	MaxBodyBytes   int64
}

type approvalGrantRequest struct {
	ExpiresInSeconds int64 `json:"expires_in_seconds,omitempty"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects" {
		h.create(w, r)
		return
	}
	if projectID, planID, action, ok := infrastructureRoute(r.URL.Path); ok {
		switch {
		case action == "plan" && r.Method == http.MethodGet:
			h.getInfrastructurePlan(w, r, projectID, planID)
		case action == "approval" && r.Method == http.MethodPost:
			h.grantInfrastructureApproval(w, r, projectID, planID)
		default:
			writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		}
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
}

func (h Handler) getInfrastructurePlan(w http.ResponseWriter, r *http.Request, projectID, planID string) {
	identity, ok := verifiedUser(r)
	if !ok || h.Infrastructure == nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "verified user identity is required")
		return
	}
	plan, err := h.Infrastructure.GetPlan(r.Context(), identity.TenantID, projectID, planID)
	if err != nil {
		writeInfrastructureError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": plan.Summary, "estimate": plan.Estimate, "reservation": plan.Reservation,
		"target": plan.Target, "artifact_digest": plan.ArtifactDigest, "apply_started_at": plan.ApplyStartedAt,
	})
}

func (h Handler) grantInfrastructureApproval(w http.ResponseWriter, r *http.Request, projectID, planID string) {
	identity, ok := verifiedUser(r)
	if !ok || h.Infrastructure == nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "verified user identity is required")
		return
	}
	var body approvalGrantRequest
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid approval grant request")
		return
	}
	ttl := body.ExpiresInSeconds
	if ttl == 0 {
		ttl = 600
	}
	if ttl < 60 || ttl > 600 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "approval lifetime must be between 60 and 600 seconds")
		return
	}
	grant, err := h.Infrastructure.GrantApproval(r.Context(), infraapp.GrantApprovalCommand{
		TenantID: identity.TenantID, ProjectID: projectID, PlanID: planID,
		ApproverUserID: identity.UserID, ExpiresAt: time.Now().UTC().Add(time.Duration(ttl) * time.Second),
	})
	if err != nil {
		writeInfrastructureError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"approval_grant_id": grant.GrantID, "plan_id": grant.PlanID, "plan_hash": grant.PlanHash,
		"estimate_version": grant.EstimateVersion, "reservation_id": grant.ReservationID,
		"target": grant.Target, "actor_id": grant.ActorID, "approver_user_id": grant.ApproverUserID,
		"expires_at": grant.ExpiresAt,
	})
}

func verifiedUser(r *http.Request) (httpauth.Identity, bool) {
	identity, ok := httpauth.IdentityFromContext(r.Context())
	return identity, ok && identity.KindClaim == "USER" && strings.TrimSpace(identity.TenantID) != "" && strings.TrimSpace(identity.UserID) != ""
}

func infrastructureRoute(path string) (string, string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 7 || len(parts) > 8 || parts[0] != "api" || parts[1] != "v2" || parts[2] != "projects" || parts[4] != "infrastructure" || parts[5] != "plans" || parts[3] == "" || parts[6] == "" {
		return "", "", "", false
	}
	if len(parts) == 7 {
		return parts[3], parts[6], "plan", true
	}
	if parts[7] != "approval" {
		return "", "", "", false
	}
	return parts[3], parts[6], "approval", true
}

func writeInfrastructureError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, infraapp.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "infrastructure plan not found")
	case errors.Is(err, infraapp.ErrConflict):
		writeError(w, http.StatusConflict, "CONFLICT", "infrastructure approval conflicts with current state")
	case errors.Is(err, infraapp.ErrPermissionDenied), errors.Is(err, infraapp.ErrApprovalRequired):
		writeError(w, http.StatusForbidden, "FORBIDDEN", "infrastructure approval denied")
	default:
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "infrastructure service is temporarily unavailable")
	}
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	if h.Projects == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project service unavailable")
		return
	}
	identity, ok := verifiedUser(r)
	if !ok {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "verified user identity is required")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	var request projectv2.CreateProjectRequest
	if err := decode(r, h.limit(), &request); err != nil || request.Validate() != nil || idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid project creation request")
		return
	}
	response, err := h.Projects.Create(r.Context(), projectapp.CreateCommand{
		TenantID: identity.TenantID, UserID: identity.UserID, Name: request.Name, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
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
		return errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	var sourceError *domain.Error
	if errors.As(err, &sourceError) {
		switch sourceError.Code {
		case domain.CodeInvalidArgument:
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", sourceError.Message)
		case domain.CodeConflict, domain.CodeStaleVersion:
			writeError(w, http.StatusConflict, "CONFLICT", sourceError.Message)
		default:
			writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project creation is temporarily unavailable")
		}
		return
	}
	writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project creation is temporarily unavailable")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
