// Package httpapi exposes the authenticated HTTP boundary for Application Attachments.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

type Handler struct {
	Attachments  *application.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "organizations" {
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
		return
	}
	tenantID := parts[2]
	actorID, err := authorize(r, tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.Attachments == nil {
		writeError(w, domain.NewError(domain.CodeUnavailable, "attachments service unavailable"))
		return
	}

	switch {
	case len(parts) == 4 && parts[3] == "services" && r.Method == http.MethodPost:
		h.provision(w, r, tenantID, actorID)
	case len(parts) == 5 && parts[3] == "services" && r.Method == http.MethodGet:
		h.getService(w, r, tenantID, parts[4])
	case len(parts) == 6 && parts[3] == "services" && parts[5] == "reconcile" && r.Method == http.MethodPost:
		h.reconcileService(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "services" && parts[5] == "purge" && r.Method == http.MethodPost:
		h.purgeService(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "bindings" && parts[5] == "rotate" && r.Method == http.MethodPost:
		h.rotateBinding(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "bindings" && parts[5] == "revoke" && r.Method == http.MethodPost:
		h.revokeBinding(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "domains" && parts[5] == "verify" && r.Method == http.MethodPost:
		h.verifyDomain(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "domains" && parts[5] == "certificate" && r.Method == http.MethodPost:
		h.reconcileCertificate(w, r, tenantID, actorID, parts[4])
	case len(parts) == 5 && parts[3] == "domains" && r.Method == http.MethodDelete:
		h.deleteDomain(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "domains" && parts[5] == "release" && r.Method == http.MethodPost:
		h.releaseDomain(w, r, tenantID, actorID, parts[4])
	case isEnvironmentPath(parts, "secrets") && r.Method == http.MethodPost:
		h.setSecret(w, r, tenantID, actorID, parts[4], parts[6])
	case isEnvironmentPath(parts, "secrets") && r.Method == http.MethodGet:
		h.listSecrets(w, r, tenantID, parts[4], parts[6])
	case isEnvironmentPath(parts, "bindings") && r.Method == http.MethodPost:
		h.bind(w, r, tenantID, actorID, parts[4], parts[6])
	case isEnvironmentPath(parts, "domains") && r.Method == http.MethodPost:
		h.addDomain(w, r, tenantID, actorID, parts[4], parts[6])
	case isEnvironmentPath(parts, "attachment-snapshot") && r.Method == http.MethodGet:
		h.snapshot(w, r, tenantID, parts[4], parts[6])
	default:
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

func isEnvironmentPath(parts []string, leaf string) bool {
	return len(parts) == 8 && parts[3] == "applications" && parts[5] == "environments" && parts[7] == leaf
}

type setSecretBody struct {
	Name      string                    `json:"name"`
	Scope     attachmentsv1.SecretScope `json:"scope"`
	Phase     attachmentsv1.SecretPhase `json:"phase"`
	Value     string                    `json:"value"`
	ExpiresAt time.Time                 `json:"expires_at,omitempty"`
}

func (h Handler) setSecret(w http.ResponseWriter, r *http.Request, tenant, actor, app, env string) {
	var body setSecretBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	value := []byte(body.Value)
	defer zero(value)
	meta, snapshot, err := h.Attachments.SetSecret(r.Context(), application.SetSecretRequest{
		TenantID: tenant, ApplicationID: app, EnvironmentID: env, Name: body.Name,
		Scope: body.Scope, Phase: body.Phase, Value: value, ExpiresAt: body.ExpiresAt,
		ActorID: actor, IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"secret": meta, "attachment_snapshot": snapshot})
}
func (h Handler) listSecrets(w http.ResponseWriter, r *http.Request, tenant, app, env string) {
	if err := h.verifyEnvironment(r, tenant, app, env); err != nil {
		writeError(w, err)
		return
	}
	values, err := h.Attachments.ListSecretMetadata(r.Context(), tenant, env)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": values})
}
func (h Handler) verifyEnvironment(r *http.Request, tenant, app, env string) error {
	if h.Attachments.Environments == nil {
		return domain.NewError(domain.CodeUnavailable, "environment directory unavailable")
	}
	ref, err := h.Attachments.Environments.ResolveEnvironment(r.Context(), tenant, env)
	if err != nil {
		return err
	}
	if ref.TenantID != tenant || ref.ApplicationID != app {
		return domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	return nil
}

type provisionBody struct {
	Name        string `json:"name"`
	PlanID      string `json:"plan_id"`
	PlanVersion int64  `json:"plan_version,omitempty"`
}

func (h Handler) provision(w http.ResponseWriter, r *http.Request, tenant, actor string) {
	var body provisionBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	instance, err := h.Attachments.ProvisionService(r.Context(), application.ProvisionServiceRequest{TenantID: tenant, Name: body.Name, PlanID: body.PlanID, PlanVersion: body.PlanVersion, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, instance)
}
func (h Handler) getService(w http.ResponseWriter, r *http.Request, tenant, id string) {
	v, err := h.Attachments.GetServiceInstance(r.Context(), tenant, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) reconcileService(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, err := h.Attachments.ReconcileService(r.Context(), tenant, id, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type purgeBody struct {
	ApprovalRef string `json:"approval_ref"`
}

func (h Handler) purgeService(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	var body purgeBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	v, err := h.Attachments.PurgeService(r.Context(), application.PurgeServiceRequest{TenantID: tenant, InstanceID: id, ActorID: actor, ApprovalRef: body.ApprovalRef, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

type bindBody struct {
	InstanceID   string   `json:"instance_id"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func (h Handler) bind(w http.ResponseWriter, r *http.Request, tenant, actor, app, env string) {
	var body bindBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	v, snap, err := h.Attachments.BindService(r.Context(), application.BindServiceRequest{TenantID: tenant, ApplicationID: app, EnvironmentID: env, InstanceID: body.InstanceID, Capabilities: body.Capabilities, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"binding": v, "attachment_snapshot": snap})
}
func (h Handler) rotateBinding(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, snap, err := h.Attachments.RotateBinding(r.Context(), application.RotateBindingRequest{TenantID: tenant, BindingID: id, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"binding": v, "attachment_snapshot": snap})
}
func (h Handler) revokeBinding(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, snap, err := h.Attachments.RevokeBinding(r.Context(), application.RevokeBindingRequest{TenantID: tenant, BindingID: id, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"binding": v, "attachment_snapshot": snap})
}

type domainBody struct {
	Hostname      string `json:"hostname,omitempty"`
	PreferredName string `json:"preferred_name,omitempty"`
}

func (h Handler) addDomain(w http.ResponseWriter, r *http.Request, tenant, actor, app, env string) {
	var body domainBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	var claim domain.DomainClaim
	var err error
	if strings.TrimSpace(body.Hostname) == "" {
		claim, err = h.Attachments.CreateGeneratedDomain(r.Context(), application.GeneratedDomainRequest{TenantID: tenant, ApplicationID: app, EnvironmentID: env, PreferredName: body.PreferredName, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	} else {
		claim, err = h.Attachments.ClaimCustomDomain(r.Context(), application.ClaimDomainRequest{TenantID: tenant, ApplicationID: app, EnvironmentID: env, Hostname: body.Hostname, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, claim)
}
func (h Handler) verifyDomain(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, err := h.Attachments.VerifyDomain(r.Context(), application.VerifyDomainRequest{TenantID: tenant, ClaimID: id, ActorID: actor})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) reconcileCertificate(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, err := h.Attachments.ReconcileCertificate(r.Context(), tenant, id, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) deleteDomain(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, err := h.Attachments.DeleteDomain(r.Context(), application.DeleteDomainRequest{TenantID: tenant, ClaimID: id, ActorID: actor, IdempotencyKey: idempotencyKey(r)})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
func (h Handler) releaseDomain(w http.ResponseWriter, r *http.Request, tenant, actor, id string) {
	v, err := h.Attachments.ReleaseDomain(r.Context(), tenant, id, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) snapshot(w http.ResponseWriter, r *http.Request, tenant, app, env string) {
	if err := h.verifyEnvironment(r, tenant, app, env); err != nil {
		writeError(w, err)
		return
	}
	v, err := h.Attachments.Resolve(r.Context(), tenant, env)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 1 << 20
	}
	return h.MaxBodyBytes
}
func idempotencyKey(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}
func authorize(r *http.Request, pathTenant string) (string, error) {
	actor := strings.TrimSpace(r.Header.Get("X-Principal-ID"))
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if actor == "" || tenant == "" {
		return "", domain.NewError(domain.CodeForbidden, "principal headers required")
	}
	if tenant != pathTenant {
		return "", domain.NewError(domain.CodeForbidden, "tenant path does not match principal")
	}
	return actor, nil
}
func decode(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple json values")
		}
		return err
	}
	return nil
}
func splitPath(v string) []string {
	raw := strings.Split(strings.Trim(v, "/"), "/")
	out := raw[:0]
	for _, x := range raw {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
func zero(v []byte) {
	for i := range v {
		v[i] = 0
	}
}

type publicError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := domain.CodeInternal
	message := "request failed"
	var typed *domain.Error
	if errors.As(err, &typed) {
		code, message = typed.Code, typed.Message
		switch typed.Code {
		case domain.CodeInvalidArgument:
			status = http.StatusBadRequest
		case domain.CodeForbidden, domain.CodeEntitlementDenied:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeStaleVersion, domain.CodeApprovalRequired:
			status = http.StatusConflict
		case domain.CodeRetryable, domain.CodeUnavailable, domain.CodeInternal:
			status = http.StatusServiceUnavailable
		}
	}
	var p publicError
	p.Error.Code = string(code)
	p.Error.Message = message
	writeJSON(w, status, p)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
