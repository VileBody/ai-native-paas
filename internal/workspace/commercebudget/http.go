package commercebudget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type HTTPConfig struct {
	BaseURL string
	Client  *http.Client
}

type HTTPQuotas struct {
	baseURL string
	client  *http.Client
	tenants sync.Map
}

func NewHTTP(config HTTPConfig) (*HTTPQuotas, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || config.Client == nil {
		return nil, errors.New("commerce budget HTTP configuration is invalid")
	}
	return &HTTPQuotas{baseURL: strings.TrimRight(parsed.String(), "/"), client: config.Client}, nil
}

func (h *HTTPQuotas) Reserve(ctx context.Context, request commercev1.QuotaRequest) (commercev1.QuotaReservation, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return commercev1.QuotaReservation{}, err
	}
	target := h.baseURL + "/v1/organizations/" + url.PathEscape(request.TenantID) + "/quota-reservations"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return commercev1.QuotaReservation{}, errors.New("create commerce budget request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.IdempotencyKey)
	response, err := h.client.Do(httpRequest)
	if err != nil {
		return commercev1.QuotaReservation{}, errors.New("commerce budget reservation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return commercev1.QuotaReservation{}, errors.New("commerce denied workspace command budget")
	}
	var reservation commercev1.QuotaReservation
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reservation); err != nil {
		return commercev1.QuotaReservation{}, errors.New("commerce returned invalid budget reservation")
	}
	h.tenants.Store(reservation.ID, request.TenantID)
	return reservation, nil
}

func (h *HTTPQuotas) Commit(ctx context.Context, reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return errors.New("commerce budget reservation identity is missing")
	}
	tenantValue, ok := h.tenants.Load(reservationID)
	tenant, tenantOK := tenantValue.(string)
	if !ok || !tenantOK || strings.TrimSpace(tenant) == "" {
		return errors.New("commerce budget reservation tenant binding is missing")
	}
	target := h.baseURL + "/v1/organizations/" + url.PathEscape(tenant) + "/quota-reservations/" + url.PathEscape(reservationID) + "/commit"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target, http.NoBody)
	if err != nil {
		return errors.New("create commerce budget commit request")
	}
	response, err := h.client.Do(httpRequest)
	if err != nil {
		return errors.New("commerce budget commit unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return errors.New("commerce rejected workspace budget commit")
	}
	h.tenants.Delete(reservationID)
	return nil
}
