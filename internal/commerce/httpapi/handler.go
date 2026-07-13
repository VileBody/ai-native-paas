package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

const platformAdminRole = "platform-admin"

type createPlanDefinitionBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (b createPlanDefinitionBody) command() application.CreatePlanDefinitionCommand {
	return application.CreatePlanDefinitionCommand{ID: b.ID, Name: b.Name}
}

type createPlanVersionBody struct {
	ID            string              `json:"id"`
	DefinitionID  string              `json:"definition_id"`
	PolicyVersion string              `json:"policy_version"`
	Number        int64               `json:"number"`
	Spec          commercev1.PlanSpec `json:"spec"`
	EffectiveFrom time.Time           `json:"effective_from"`
}

func (b createPlanVersionBody) command() application.CreatePlanVersionCommand {
	return application.CreatePlanVersionCommand{
		ID: b.ID, DefinitionID: b.DefinitionID, PolicyVersion: b.PolicyVersion,
		Number: b.Number, Spec: b.Spec, EffectiveFrom: b.EffectiveFrom,
	}
}

type startSubscriptionBody struct {
	ID            string                   `json:"id"`
	TenantID      string                   `json:"tenant_id"`
	PlanVersionID string                   `json:"plan_version_id"`
	PeriodID      string                   `json:"period_id"`
	State         domain.SubscriptionState `json:"state"`
	TrialEndsAt   time.Time                `json:"trial_ends_at,omitempty"`
	PeriodStart   time.Time                `json:"period_start"`
	PeriodEnd     time.Time                `json:"period_end"`
}

func (b startSubscriptionBody) command() application.StartSubscriptionCommand {
	return application.StartSubscriptionCommand{
		ID: b.ID, TenantID: b.TenantID, PlanVersionID: b.PlanVersionID,
		PeriodID: b.PeriodID, State: b.State, TrialEndsAt: b.TrialEndsAt,
		PeriodStart: b.PeriodStart, PeriodEnd: b.PeriodEnd,
	}
}

type reconcileUsageBody struct {
	PeriodID         string           `json:"period_id"`
	ResourceType     string           `json:"resource_type"`
	ResourceID       string           `json:"resource_id"`
	Meter            commercev1.Meter `json:"meter"`
	WindowStart      time.Time        `json:"window_start"`
	WindowEnd        time.Time        `json:"window_end"`
	ObservedQuantity int64            `json:"observed_quantity"`
}

func (b reconcileUsageBody) command() application.ReconcileUsageCommand {
	return application.ReconcileUsageCommand{
		PeriodID: b.PeriodID, ResourceType: b.ResourceType, ResourceID: b.ResourceID,
		Meter: b.Meter, WindowStart: b.WindowStart, WindowEnd: b.WindowEnd,
		ObservedQuantity: b.ObservedQuantity,
	}
}

type Handler struct {
	Commerce     *application.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if h.Commerce == nil {
		writeError(w, domain.NewError(domain.CodeUnavailable, "commerce service unavailable"))
		return
	}
	parts := splitPath(r.URL.Path)
	if len(parts) >= 2 && parts[0] == "v1" && parts[1] == "admin" {
		if err := authorizeAdmin(r); err != nil {
			writeError(w, err)
			return
		}
		h.serveAdmin(w, r, parts)
		return
	}
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "organizations" {
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
		return
	}
	tenantID := parts[2]
	if _, err := authorizeTenant(r, tenantID); err != nil {
		writeError(w, err)
		return
	}
	h.serveTenant(w, r, tenantID, parts)
}

func (h Handler) serveAdmin(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 3 && parts[2] == "plan-definitions" && r.Method == http.MethodPost:
		var body createPlanDefinitionBody
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		value, err := h.Commerce.CreatePlanDefinition(r.Context(), body.command())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, value)
	case len(parts) == 3 && parts[2] == "plan-versions" && r.Method == http.MethodPost:
		var body createPlanVersionBody
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		value, err := h.Commerce.CreatePlanVersion(r.Context(), body.command())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, value)
	case len(parts) == 5 && parts[2] == "plan-versions" && parts[4] == "activate" && r.Method == http.MethodPost:
		value, err := h.Commerce.ActivatePlanVersion(r.Context(), parts[3])
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case len(parts) == 5 && parts[2] == "plan-versions" && parts[4] == "disable" && r.Method == http.MethodPost:
		value, err := h.Commerce.DisablePlanVersion(r.Context(), parts[3])
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case len(parts) == 3 && parts[2] == "subscriptions" && r.Method == http.MethodPost:
		var body startSubscriptionBody
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		sub, period, err := h.Commerce.StartSubscription(r.Context(), body.command())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"subscription": sub, "billing_period": period})
	default:
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

func (h Handler) serveTenant(w http.ResponseWriter, r *http.Request, tenantID string, parts []string) {
	switch {
	case len(parts) == 5 && parts[3] == "entitlements" && parts[4] == "check" && r.Method == http.MethodPost:
		var body commercev1.EntitlementRequest
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		body.TenantID = tenantID
		value, err := h.Commerce.CheckAs(r.Context(), tenantID, body)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case len(parts) == 4 && parts[3] == "quota-reservations" && r.Method == http.MethodPost:
		var body commercev1.QuotaRequest
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		body.TenantID = tenantID
		if body.IdempotencyKey == "" {
			body.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		}
		value, err := h.Commerce.Reserve(r.Context(), body)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, value)
	case len(parts) == 6 && parts[3] == "quota-reservations" && parts[5] == "commit" && r.Method == http.MethodPost:
		if err := h.Commerce.Commit(r.Context(), parts[4]); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "committed"})
	case len(parts) == 6 && parts[3] == "quota-reservations" && parts[5] == "release" && r.Method == http.MethodPost:
		if err := h.Commerce.Release(r.Context(), parts[4]); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
	case len(parts) == 4 && parts[3] == "usage" && r.Method == http.MethodPost:
		var body commercev1.UsageEvent
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		body.TenantID = tenantID
		if body.IdempotencyKey == "" {
			body.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		}
		if err := h.Commerce.Append(r.Context(), body); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
	case len(parts) == 6 && parts[3] == "billing-periods" && parts[5] == "invoice-preview" && r.Method == http.MethodGet:
		value, err := h.Commerce.PreviewInvoice(r.Context(), tenantID, parts[4])
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case len(parts) == 5 && parts[3] == "commercial-state" && r.Method == http.MethodPost:
		var value commercev1.RuntimeIntent
		var err error
		switch parts[4] {
		case "grace":
			value, err = h.Commerce.SetGrace(r.Context(), tenantID)
		case "suspend":
			value, err = h.Commerce.Suspend(r.Context(), tenantID)
		case "resume":
			value, err = h.Commerce.Resume(r.Context(), tenantID)
		default:
			err = domain.NewError(domain.CodeNotFound, "route not found")
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, value)
	case len(parts) == 5 && parts[3] == "usage" && parts[4] == "reconcile" && r.Method == http.MethodPost:
		var body reconcileUsageBody
		if !h.decodeOrWrite(w, r, &body) {
			return
		}
		command := body.command()
		command.TenantID = tenantID
		drift, err := h.Commerce.ReconcileUsage(r.Context(), command)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"drift": drift})
	default:
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

func (h Handler) decodeOrWrite(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := decode(r, h.limit(), out); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return false
	}
	return true
}
func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 2 << 20
	}
	return h.MaxBodyBytes
}

func authorizeAdmin(r *http.Request) error {
	if strings.TrimSpace(r.Header.Get("X-Principal-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Principal-Role")) != platformAdminRole {
		return domain.NewError(domain.CodePermissionDenied, "platform-admin principal required")
	}
	return nil
}
func authorizeTenant(r *http.Request, pathTenant string) (string, error) {
	actor := strings.TrimSpace(r.Header.Get("X-Principal-ID"))
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if actor == "" || tenant == "" {
		return "", domain.NewError(domain.CodePermissionDenied, "principal headers required")
	}
	if tenant != pathTenant {
		return "", domain.NewError(domain.CodePermissionDenied, "tenant path does not match principal")
	}
	return actor, nil
}
func decode(r *http.Request, limit int64, out any) error {
	if limit <= 0 {
		return errors.New("invalid body limit")
	}
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
func splitPath(value string) []string {
	raw := strings.Split(strings.Trim(value, "/"), "/")
	out := raw[:0]
	for _, item := range raw {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

type publicError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, domain.CodeInternal, "request failed"
	var typed *domain.Error
	if errors.As(err, &typed) {
		code, message = typed.Code, typed.Message
		switch typed.Code {
		case domain.CodeInvalidArgument:
			status = http.StatusBadRequest
		case domain.CodePermissionDenied:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeStaleVersion, domain.CodeQuotaExceeded:
			status = http.StatusConflict
		case domain.CodeOverflow:
			status = http.StatusUnprocessableEntity
		case domain.CodeUnavailable, domain.CodeInternal:
			status = http.StatusServiceUnavailable
		}
	}
	var payload publicError
	payload.Error.Code, payload.Error.Message = string(code), message
	writeJSON(w, status, payload)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
