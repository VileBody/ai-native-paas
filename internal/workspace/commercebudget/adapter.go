// Package commercebudget binds workspace command runtime to Commerce quota reservations.
package commercebudget

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

const Resource = "workspace.command_seconds"

type Quotas interface {
	Reserve(context.Context, commercev1.QuotaRequest) (commercev1.QuotaReservation, error)
	Commit(context.Context, string) error
}

type Adapter struct {
	Quotas Quotas
}

func (a Adapter) ReserveAndCommit(ctx context.Context, request workspace.CommandBudgetRequest) (workspace.CommandBudgetLease, error) {
	if a.Quotas == nil || strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.TaskID) == "" || strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.CommandID) == "" || request.RequestedSeconds < 1 || request.RequestedSeconds > 86400 || request.RequestedAt.IsZero() {
		return workspace.CommandBudgetLease{}, errors.New("workspace command budget request is invalid")
	}
	now := request.RequestedAt.UTC()
	reservation, err := a.Quotas.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: request.TenantID, ProjectID: request.ProjectID, Resource: Resource, Quantity: request.RequestedSeconds,
		IdempotencyKey: "workspace-command:" + request.ProjectID + ":" + request.TaskID + ":" + request.CommandID,
		At:             now, ExpiresAt: now.Add(time.Duration(request.RequestedSeconds)*time.Second + 15*time.Minute),
	})
	if err != nil {
		return workspace.CommandBudgetLease{}, err
	}
	if reservation.ID == "" || reservation.Quantity != request.RequestedSeconds || reservation.CreatedAt.IsZero() {
		return workspace.CommandBudgetLease{}, errors.New("commerce returned an invalid workspace budget reservation")
	}
	if err := a.Quotas.Commit(ctx, reservation.ID); err != nil {
		return workspace.CommandBudgetLease{}, err
	}
	return workspace.CommandBudgetLease{
		ReservationID:  reservation.ID,
		GrantedSeconds: reservation.Quantity,
		NotAfter:       reservation.CreatedAt.UTC().Add(time.Duration(reservation.Quantity) * time.Second),
	}, nil
}
