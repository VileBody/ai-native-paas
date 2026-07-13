package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

const maxRequestBody = 1 << 20 // 1 MiB

type Handler struct {
	Service *kernel.Service
	IDs     kernel.IDGenerator
}

func NewHandler(service *kernel.Service, ids kernel.IDGenerator) (http.Handler, error) {
	if service == nil || ids == nil {
		return nil, errors.New("http handler requires kernel service and id generator")
	}
	handler := &Handler{Service: service, IDs: ids}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/organizations", handler.createOrganization)
	mux.HandleFunc("GET /v1/organizations/{organizationID}", handler.getOrganization)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/invitations", handler.inviteMember)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/invitations/accept", handler.acceptInvitation)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/members/{principalID}/role", handler.changeRole)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/members/{principalID}/suspend", handler.suspendMembership)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/members/{principalID}", handler.removeMembership)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/audit", handler.listAudit)
	mux.HandleFunc("GET /v1/operations/{operationID}", handler.getOperation)
	mux.HandleFunc("POST /v1/operations/{operationID}/cancel", handler.cancelOperation)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return recoveryMiddleware(mux), nil
}

func (h *Handler) createOrganization(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.CreateOrganization(r.Context(), kernel.CreateOrganizationCommand{Meta: meta, Name: request.Name})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) getOrganization(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	correlationID := h.correlationID(r)
	result, err := h.Service.GetOrganization(r.Context(), principal, kernelv1.TenantID(r.PathValue("organizationID")), correlationID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) inviteMember(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		PrincipalID kernelv1.PrincipalID  `json:"principal_id"`
		Role        kernel.MembershipRole `json:"role"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.InviteMember(r.Context(), kernel.InviteMemberCommand{
		Meta:           meta,
		OrganizationID: kernelv1.TenantID(r.PathValue("organizationID")),
		PrincipalID:    request.PrincipalID,
		Role:           request.Role,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.AcceptInvitation(r.Context(), kernel.AcceptInvitationCommand{
		Meta:           meta,
		OrganizationID: kernelv1.TenantID(r.PathValue("organizationID")),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var request struct {
		Role kernel.MembershipRole `json:"role"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.ChangeMemberRole(r.Context(), kernel.ChangeMemberRoleCommand{
		Meta:           meta,
		OrganizationID: kernelv1.TenantID(r.PathValue("organizationID")),
		PrincipalID:    kernelv1.PrincipalID(r.PathValue("principalID")),
		Role:           request.Role,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) suspendMembership(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.SuspendMembership(r.Context(), kernel.SuspendMembershipCommand{
		Meta:           meta,
		OrganizationID: kernelv1.TenantID(r.PathValue("organizationID")),
		PrincipalID:    kernelv1.PrincipalID(r.PathValue("principalID")),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) removeMembership(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.RemoveMembership(r.Context(), kernel.RemoveMembershipCommand{
		Meta:           meta,
		OrganizationID: kernelv1.TenantID(r.PathValue("organizationID")),
		PrincipalID:    kernelv1.PrincipalID(r.PathValue("principalID")),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	records, err := h.Service.ListAuditEvents(r.Context(), principal, kernelv1.TenantID(r.PathValue("organizationID")), h.correlationID(r), 250)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": records})
}

func (h *Handler) getOperation(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	snapshot, err := h.Service.GetOperationForPrincipal(r.Context(), principal, kernelv1.OperationID(r.PathValue("operationID")), h.correlationID(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) cancelOperation(w http.ResponseWriter, r *http.Request) {
	principal, err := principalFromHeaders(r)
	if err != nil {
		writeError(w, err)
		return
	}
	meta, err := h.commandMeta(r, principal)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.Service.CancelOperation(r.Context(), kernel.CancelOperationCommand{Meta: meta, OperationID: kernelv1.OperationID(r.PathValue("operationID"))})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) commandMeta(r *http.Request, principal kernelv1.PrincipalContext) (kernelv1.CommandMeta, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return kernelv1.CommandMeta{}, kernel.NewError(kernelv1.CodeInvalidArgument, "Idempotency-Key header is required")
	}
	return kernelv1.CommandMeta{
		Principal:      principal,
		CorrelationID:  h.correlationID(r),
		CausationID:    kernelv1.CausationID(strings.TrimSpace(r.Header.Get("X-Causation-ID"))),
		IdempotencyKey: kernelv1.IdempotencyKey(key),
	}, nil
}

func (h *Handler) correlationID(r *http.Request) kernelv1.CorrelationID {
	value := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if value == "" {
		value = h.IDs.New("cor")
	}
	return kernelv1.CorrelationID(value)
}

func principalFromHeaders(r *http.Request) (kernelv1.PrincipalContext, error) {
	principalID := strings.TrimSpace(r.Header.Get("X-Principal-ID"))
	if principalID == "" {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeForbidden, "Authenticated principal is required")
	}
	kind := kernelv1.PrincipalKind(strings.ToLower(strings.TrimSpace(r.Header.Get("X-Principal-Kind"))))
	if kind == "" {
		kind = kernelv1.PrincipalKindUser
	}
	scopesHeader := strings.NewReplacer(",", " ", ";", " ").Replace(r.Header.Get("X-Scopes"))
	principal := kernelv1.PrincipalContext{
		PrincipalID: kernelv1.PrincipalID(principalID),
		// Tenant context is intentionally not accepted from request headers.
		// The application derives it from the target resource and membership.
		Kind:   kind,
		Scopes: strings.Fields(scopesHeader),
	}
	if err := principal.Validate(); err != nil {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeForbidden, "Authenticated principal is invalid")
	}
	return principal, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "Request body is invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "Request body must contain one JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, err error) {
	publicErr := kernel.ToPublicError(err, "")
	status := http.StatusInternalServerError
	switch publicErr.Code {
	case kernelv1.CodeInvalidArgument:
		status = http.StatusBadRequest
	case kernelv1.CodeForbidden:
		status = http.StatusForbidden
	case kernelv1.CodeNotFound:
		status = http.StatusNotFound
	case kernelv1.CodeConflict, kernelv1.CodeIdempotencyConflict, kernelv1.CodeLastOwner, kernelv1.CodeInvalidTransition, kernelv1.CodeOptimisticLock:
		status = http.StatusConflict
	}
	writeJSON(w, status, publicErr)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeJSON(w, http.StatusInternalServerError, kernelv1.PublicError{Code: kernelv1.CodeInternal, Message: "Internal platform error", Retryable: true})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
