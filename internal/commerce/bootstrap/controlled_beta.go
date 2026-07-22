// Package bootstrap contains explicit, operator-run Commerce bootstrap flows.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

const (
	ControlledBetaPlanDefinitionID = "controlled-beta"
	ControlledBetaPlanVersionID    = "controlled-beta-v1"
	ControlledBetaPolicyVersion    = "controlled-beta-2026-07-22"
)

type ControlledBetaConfig struct {
	TenantID         string
	SubscriptionID   string
	BillingPeriodID  string
	WorkspaceSeconds int64
	Now              time.Time
}

func (c ControlledBetaConfig) validate() error {
	c.TenantID = strings.TrimSpace(c.TenantID)
	c.SubscriptionID = strings.TrimSpace(c.SubscriptionID)
	c.BillingPeriodID = strings.TrimSpace(c.BillingPeriodID)
	if c.TenantID == "" || c.SubscriptionID == "" || c.BillingPeriodID == "" || c.WorkspaceSeconds < 1 || c.Now.IsZero() {
		return errors.New("controlled beta commerce seed configuration is invalid")
	}
	return nil
}

func controlledBetaSpec(workspaceSeconds int64) commercev1.PlanSpec {
	return commercev1.PlanSpec{
		Currency: "RUB",
		Features: map[string]bool{"deploy": true, "workspace": true},
		Quotas:   map[string]int64{"workspace.command_seconds": workspaceSeconds},
		Prices:   map[commercev1.Meter]commercev1.Price{},
		Included: map[commercev1.Meter]int64{},
	}
}

// EnsureControlledBeta creates one immutable controlled-beta plan and one
// active tenant subscription. Re-running it verifies the exact durable state
// instead of mutating an active plan or creating duplicate billing records.
func EnsureControlledBeta(ctx context.Context, service *application.Service, store application.Store, config ControlledBetaConfig) error {
	if service == nil || store == nil {
		return errors.New("commerce service and store are required")
	}
	if err := config.validate(); err != nil {
		return err
	}
	config.TenantID = strings.TrimSpace(config.TenantID)
	config.SubscriptionID = strings.TrimSpace(config.SubscriptionID)
	config.BillingPeriodID = strings.TrimSpace(config.BillingPeriodID)
	config.Now = config.Now.UTC()
	spec := controlledBetaSpec(config.WorkspaceSeconds)

	definitionExists := false
	if err := store.Transact(ctx, func(tx application.Tx) error {
		value, ok := tx.GetPlanDefinition(ControlledBetaPlanDefinitionID)
		definitionExists = ok
		if ok && value.Name != "Controlled Beta" {
			return errors.New("controlled beta plan definition differs from the release contract")
		}
		return nil
	}); err != nil {
		return err
	}
	if !definitionExists {
		if _, err := service.CreatePlanDefinition(ctx, application.CreatePlanDefinitionCommand{ID: ControlledBetaPlanDefinitionID, Name: "Controlled Beta"}); err != nil {
			return fmt.Errorf("create controlled beta plan definition: %w", err)
		}
	}

	versionState := domain.PlanState("")
	versionExists := false
	if err := store.Transact(ctx, func(tx application.Tx) error {
		value, ok := tx.GetPlanVersion(ControlledBetaPlanVersionID)
		versionExists = ok
		if !ok {
			return nil
		}
		versionState = value.State
		if value.DefinitionID != ControlledBetaPlanDefinitionID || value.PolicyVersion != ControlledBetaPolicyVersion || value.Number != 1 || !reflect.DeepEqual(value.Spec, spec) {
			return errors.New("controlled beta plan version differs from the release contract")
		}
		return nil
	}); err != nil {
		return err
	}
	if !versionExists {
		if _, err := service.CreatePlanVersion(ctx, application.CreatePlanVersionCommand{
			ID: ControlledBetaPlanVersionID, DefinitionID: ControlledBetaPlanDefinitionID,
			PolicyVersion: ControlledBetaPolicyVersion, Number: 1, Spec: spec,
			EffectiveFrom: config.Now.Add(-time.Minute),
		}); err != nil {
			return fmt.Errorf("create controlled beta plan version: %w", err)
		}
		versionState = domain.PlanDraft
	}
	if versionState == domain.PlanDraft {
		if _, err := service.ActivatePlanVersion(ctx, ControlledBetaPlanVersionID); err != nil {
			return fmt.Errorf("activate controlled beta plan version: %w", err)
		}
	} else if versionState != domain.PlanActive {
		return errors.New("controlled beta plan version is not active")
	}

	subscriptionExists := false
	if err := store.Transact(ctx, func(tx application.Tx) error {
		subscription, ok := tx.FindSubscriptionByTenant(config.TenantID)
		subscriptionExists = ok
		if !ok {
			return nil
		}
		if subscription.ID != config.SubscriptionID || subscription.PlanVersionID != ControlledBetaPlanVersionID || subscription.State != domain.SubscriptionActive {
			return errors.New("controlled beta subscription differs from the release contract")
		}
		period, ok := tx.GetBillingPeriod(config.BillingPeriodID)
		if !ok || period.SubscriptionID != subscription.ID || period.PlanVersionID != ControlledBetaPlanVersionID || period.State != domain.BillingPeriodOpen || !period.Contains(config.Now) {
			return errors.New("controlled beta billing period is missing or inactive")
		}
		account, ok := tx.GetCommercialAccount(config.TenantID)
		if !ok || account.State != commercev1.CommercialActive {
			return errors.New("controlled beta commercial account is missing or inactive")
		}
		return nil
	}); err != nil {
		return err
	}
	if subscriptionExists {
		return nil
	}
	periodStart := config.Now.Truncate(24 * time.Hour)
	_, _, err := service.StartSubscription(ctx, application.StartSubscriptionCommand{
		ID: config.SubscriptionID, TenantID: config.TenantID, PlanVersionID: ControlledBetaPlanVersionID,
		PeriodID: config.BillingPeriodID, State: domain.SubscriptionActive,
		PeriodStart: periodStart, PeriodEnd: periodStart.Add(31 * 24 * time.Hour),
	})
	if err != nil {
		return fmt.Errorf("start controlled beta subscription: %w", err)
	}
	return nil
}
