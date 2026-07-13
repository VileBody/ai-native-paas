package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

const (
	EventOrganizationCreated = "kernel.organization_created.v1"
	EventMembershipChanged   = "kernel.membership_changed.v1"
	EventOperationChanged    = "kernel.operation_state_changed.v1"
	EventAuditRecorded       = "kernel.audit_recorded.v1"
)

type Service struct {
	store  Store
	clock  Clock
	ids    IDGenerator
	policy AuthorizationPolicy
}

func NewService(store Store, clock Clock, ids IDGenerator) (*Service, error) {
	if store == nil || clock == nil || ids == nil {
		return nil, errors.New("kernel service requires store, clock, and id generator")
	}
	return &Service{store: store, clock: clock, ids: ids}, nil
}

type MembershipSnapshot struct {
	PrincipalID kernelv1.PrincipalID `json:"principal_id"`
	Role        MembershipRole       `json:"role"`
	State       MembershipState      `json:"state"`
	InvitedAt   time.Time            `json:"invited_at"`
	AcceptedAt  *time.Time           `json:"accepted_at,omitempty"`
	UpdatedAt   time.Time            `json:"updated_at"`
	Version     int64                `json:"version"`
}

type OrganizationSnapshot struct {
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
	Name           string               `json:"name"`
	Slug           string               `json:"slug"`
	Memberships    []MembershipSnapshot `json:"memberships"`
	Version        int64                `json:"version"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

type CommandResult struct {
	Operation kernelv1.OperationRef `json:"operation"`
}

type CreateOrganizationResult struct {
	Operation    kernelv1.OperationRef `json:"operation"`
	Organization OrganizationSnapshot  `json:"organization"`
}

type CreateOrganizationCommand struct {
	Meta kernelv1.CommandMeta `json:"meta"`
	Name string               `json:"name"`
}

type InviteMemberCommand struct {
	Meta           kernelv1.CommandMeta `json:"meta"`
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
	PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
	Role           MembershipRole       `json:"role"`
}

type AcceptInvitationCommand struct {
	Meta           kernelv1.CommandMeta `json:"meta"`
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
}

type ChangeMemberRoleCommand struct {
	Meta           kernelv1.CommandMeta `json:"meta"`
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
	PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
	Role           MembershipRole       `json:"role"`
}

type SuspendMembershipCommand struct {
	Meta           kernelv1.CommandMeta `json:"meta"`
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
	PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
}

type RemoveMembershipCommand struct {
	Meta           kernelv1.CommandMeta `json:"meta"`
	OrganizationID kernelv1.TenantID    `json:"organization_id"`
	PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
}

type CancelOperationCommand struct {
	Meta        kernelv1.CommandMeta `json:"meta"`
	OperationID kernelv1.OperationID `json:"operation_id"`
}

type storedCommandResponse struct {
	Operation kernelv1.OperationRef `json:"operation"`
	Payload   json.RawMessage       `json:"payload,omitempty"`
	Error     *kernelv1.PublicError `json:"error,omitempty"`
}

type commandExecution struct {
	scope       string
	fingerprint string
	meta        kernelv1.CommandMeta
	action      string
	resource    kernelv1.ResourceRef
	operationID kernelv1.OperationID
	replay      *storedCommandResponse
}

func (s *Service) CreateOrganization(ctx context.Context, command CreateOrganizationCommand) (CreateOrganizationResult, error) {
	command.Meta.Command = "CreateOrganization"
	if err := validateMeta(command.Meta); err != nil {
		return CreateOrganizationResult{}, err
	}
	payload := struct {
		Name string `json:"name"`
	}{Name: command.Name}
	execution, err := s.prepareCommand(command.Meta, platformScope(command.Meta, command.Meta.Command), payload, ActionOrganizationCreate, kernelv1.ResourceRef{Type: "organization", ID: "new"})
	if err != nil {
		return CreateOrganizationResult{}, err
	}

	var stored storedCommandResponse
	var outcomeErr error
	err = s.store.Transact(ctx, func(tx Tx) error {
		replay, claimed, err := s.claimCommand(ctx, tx, execution)
		if err != nil {
			return err
		}
		if !claimed {
			stored = *replay
			return nil
		}

		if !command.Meta.Principal.HasScope(ActionOrganizationCreate) {
			outcomeErr = forbidden("principal does not have the required scope")
			stored, err = s.recordCommandFailure(ctx, tx, execution, outcomeErr, map[string]any{"name": command.Name})
			return err
		}

		organizationID := kernelv1.TenantID(s.ids.New("org"))
		organization, domainErr := NewOrganization(organizationID, command.Name, command.Meta.Principal.PrincipalID, s.clock.Now())
		if domainErr != nil {
			outcomeErr = domainErr
			stored, err = s.recordCommandFailure(ctx, tx, execution, domainErr, map[string]any{"name": command.Name})
			return err
		}
		if err := tx.InsertOrganization(ctx, organization); err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, map[string]any{"name": command.Name})
			return err
		}

		operation, err := s.recordSuccessfulOperation(ctx, tx, command.Meta, organization.ID, command.Meta.Command, map[string]any{"organization_id": organization.ID})
		if err != nil {
			return err
		}
		execution.operationID = operation.OperationID
		execution.resource = kernelv1.ResourceRef{TenantID: organization.ID, Type: "organization", ID: string(organization.ID)}

		if err := s.appendEvent(ctx, tx, EventOrganizationCreated, organization.ID, string(organization.ID), command.Meta, map[string]any{
			"organization_id": organization.ID,
			"name":            organization.Name,
			"slug":            organization.Slug,
			"creator_id":      command.Meta.Principal.PrincipalID,
		}); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, command.Meta, execution.resource, ActionOrganizationCreate, kernelv1.AuditOutcomeSucceeded, "", map[string]any{"name": organization.Name, "slug": organization.Slug}); err != nil {
			return err
		}

		result := CreateOrganizationResult{Operation: operation, Organization: snapshotOrganization(organization)}
		encoded, err := json.Marshal(result)
		if err != nil {
			return WrapError(kernelv1.CodeInternal, "Could not encode command result", false, err)
		}
		stored = storedCommandResponse{Operation: operation, Payload: encoded}
		return s.completeCommand(ctx, tx, execution, stored)
	})
	if err != nil {
		return CreateOrganizationResult{}, err
	}
	if outcomeErr != nil {
		return CreateOrganizationResult{}, replayError(stored, outcomeErr)
	}
	if stored.Error != nil {
		return CreateOrganizationResult{}, replayError(stored, nil)
	}
	var result CreateOrganizationResult
	if err := json.Unmarshal(stored.Payload, &result); err != nil {
		return CreateOrganizationResult{}, WrapError(kernelv1.CodeInternal, "Stored command response is invalid", false, err)
	}
	return result, nil
}

func (s *Service) InviteMember(ctx context.Context, command InviteMemberCommand) (CommandResult, error) {
	command.Meta.Command = "InviteMember"
	payload := struct {
		OrganizationID kernelv1.TenantID    `json:"organization_id"`
		PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
		Role           MembershipRole       `json:"role"`
	}{command.OrganizationID, command.PrincipalID, command.Role}
	return s.mutateMembership(ctx, command.Meta, command.OrganizationID, ActionMembershipInvite, payload, func(organization *Organization) error {
		return organization.Invite(command.PrincipalID, command.Role, s.clock.Now())
	}, map[string]any{"target_principal_id": command.PrincipalID, "role": command.Role, "state": MembershipInvited})
}

func (s *Service) AcceptInvitation(ctx context.Context, command AcceptInvitationCommand) (CommandResult, error) {
	command.Meta.Command = "AcceptInvitation"
	if err := validateMeta(command.Meta); err != nil {
		return CommandResult{}, err
	}
	payload := struct {
		OrganizationID kernelv1.TenantID `json:"organization_id"`
	}{command.OrganizationID}
	resource := kernelv1.ResourceRef{TenantID: command.OrganizationID, Type: "membership", ID: string(command.Meta.Principal.PrincipalID)}
	execution, err := s.prepareCommand(command.Meta, tenantScope(command.Meta, command.OrganizationID, command.Meta.Command), payload, ActionMembershipAccept, resource)
	if err != nil {
		return CommandResult{}, err
	}

	var stored storedCommandResponse
	var outcomeErr error
	err = s.store.Transact(ctx, func(tx Tx) error {
		replay, claimed, err := s.claimCommand(ctx, tx, execution)
		if err != nil {
			return err
		}
		if !claimed {
			stored = *replay
			return nil
		}
		organization, err := tx.GetOrganization(ctx, command.OrganizationID)
		if err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		principal := deriveTenant(command.Meta.Principal, command.OrganizationID)
		if !principal.HasScope(ActionMembershipAccept) {
			outcomeErr = forbidden("principal does not have the required scope")
			stored, err = s.recordCommandFailure(ctx, tx, execution, outcomeErr, nil)
			return err
		}
		membership, ok := organization.Memberships[principal.PrincipalID]
		if !ok || membership.State != MembershipInvited {
			outcomeErr = forbidden("an invitation for the principal is required")
			stored, err = s.recordCommandFailure(ctx, tx, execution, outcomeErr, nil)
			return err
		}
		expectedVersion := organization.Version
		if err := organization.AcceptInvitation(principal.PrincipalID, s.clock.Now()); err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		if err := tx.SaveOrganization(ctx, organization, expectedVersion); err != nil {
			return err
		}
		operation, err := s.recordSuccessfulOperation(ctx, tx, command.Meta, organization.ID, command.Meta.Command, map[string]any{"principal_id": principal.PrincipalID})
		if err != nil {
			return err
		}
		execution.operationID = operation.OperationID
		if err := s.appendEvent(ctx, tx, EventMembershipChanged, organization.ID, string(principal.PrincipalID), command.Meta, map[string]any{
			"organization_id": organization.ID,
			"principal_id":    principal.PrincipalID,
			"role":            membership.Role,
			"state":           MembershipActive,
		}); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, command.Meta, resource, ActionMembershipAccept, kernelv1.AuditOutcomeSucceeded, "", map[string]any{"role": membership.Role}); err != nil {
			return err
		}
		result := CommandResult{Operation: operation}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		stored = storedCommandResponse{Operation: operation, Payload: encoded}
		return s.completeCommand(ctx, tx, execution, stored)
	})
	if err != nil {
		return CommandResult{}, err
	}
	if outcomeErr != nil || stored.Error != nil {
		return CommandResult{}, replayError(stored, outcomeErr)
	}
	var result CommandResult
	if err := json.Unmarshal(stored.Payload, &result); err != nil {
		return CommandResult{}, WrapError(kernelv1.CodeInternal, "Stored command response is invalid", false, err)
	}
	return result, nil
}

func (s *Service) ChangeMemberRole(ctx context.Context, command ChangeMemberRoleCommand) (CommandResult, error) {
	command.Meta.Command = "ChangeMemberRole"
	payload := struct {
		OrganizationID kernelv1.TenantID    `json:"organization_id"`
		PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
		Role           MembershipRole       `json:"role"`
	}{command.OrganizationID, command.PrincipalID, command.Role}
	return s.mutateMembership(ctx, command.Meta, command.OrganizationID, ActionMembershipManage, payload, func(organization *Organization) error {
		return organization.ChangeMemberRole(command.PrincipalID, command.Role, s.clock.Now())
	}, map[string]any{"target_principal_id": command.PrincipalID, "role": command.Role})
}

func (s *Service) SuspendMembership(ctx context.Context, command SuspendMembershipCommand) (CommandResult, error) {
	command.Meta.Command = "SuspendMembership"
	payload := struct {
		OrganizationID kernelv1.TenantID    `json:"organization_id"`
		PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
	}{command.OrganizationID, command.PrincipalID}
	return s.mutateMembership(ctx, command.Meta, command.OrganizationID, ActionMembershipManage, payload, func(organization *Organization) error {
		return organization.SuspendMember(command.PrincipalID, s.clock.Now())
	}, map[string]any{"target_principal_id": command.PrincipalID, "state": MembershipSuspended})
}

func (s *Service) RemoveMembership(ctx context.Context, command RemoveMembershipCommand) (CommandResult, error) {
	command.Meta.Command = "RemoveMembership"
	payload := struct {
		OrganizationID kernelv1.TenantID    `json:"organization_id"`
		PrincipalID    kernelv1.PrincipalID `json:"principal_id"`
	}{command.OrganizationID, command.PrincipalID}
	return s.mutateMembership(ctx, command.Meta, command.OrganizationID, ActionMembershipManage, payload, func(organization *Organization) error {
		return organization.RemoveMember(command.PrincipalID, s.clock.Now())
	}, map[string]any{"target_principal_id": command.PrincipalID, "state": MembershipRemoved})
}

func (s *Service) mutateMembership(ctx context.Context, meta kernelv1.CommandMeta, organizationID kernelv1.TenantID, action string, payload any, mutate func(*Organization) error, auditMetadata map[string]any) (CommandResult, error) {
	if err := validateMeta(meta); err != nil {
		return CommandResult{}, err
	}
	resourceID := "memberships"
	if target, ok := auditMetadata["target_principal_id"]; ok {
		resourceID = fmt.Sprint(target)
	}
	resource := kernelv1.ResourceRef{TenantID: organizationID, Type: "membership", ID: resourceID}
	execution, err := s.prepareCommand(meta, tenantScope(meta, organizationID, meta.Command), payload, action, resource)
	if err != nil {
		return CommandResult{}, err
	}

	var stored storedCommandResponse
	var outcomeErr error
	err = s.store.Transact(ctx, func(tx Tx) error {
		replay, claimed, err := s.claimCommand(ctx, tx, execution)
		if err != nil {
			return err
		}
		if !claimed {
			stored = *replay
			return nil
		}
		organization, err := tx.GetOrganization(ctx, organizationID)
		if err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, auditMetadata)
			return err
		}
		principal := deriveTenant(meta.Principal, organizationID)
		if err := s.policy.Check(principal, action, resource, organization); err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, auditMetadata)
			return err
		}
		if action == ActionMembershipManage && !canManageTarget(organization, principal.PrincipalID, resourceID, auditMetadata) {
			outcomeErr = forbidden("only an owner can manage owner memberships")
			stored, err = s.recordCommandFailure(ctx, tx, execution, outcomeErr, auditMetadata)
			return err
		}

		expectedVersion := organization.Version
		if err := mutate(organization); err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, auditMetadata)
			return err
		}
		if err := tx.SaveOrganization(ctx, organization, expectedVersion); err != nil {
			return err
		}
		operation, err := s.recordSuccessfulOperation(ctx, tx, meta, organization.ID, meta.Command, auditMetadata)
		if err != nil {
			return err
		}
		execution.operationID = operation.OperationID
		eventPayload := cloneMap(auditMetadata)
		eventPayload["organization_id"] = organization.ID
		if err := s.appendEvent(ctx, tx, EventMembershipChanged, organization.ID, resourceID, meta, eventPayload); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, meta, resource, action, kernelv1.AuditOutcomeSucceeded, "", auditMetadata); err != nil {
			return err
		}
		result := CommandResult{Operation: operation}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		stored = storedCommandResponse{Operation: operation, Payload: encoded}
		return s.completeCommand(ctx, tx, execution, stored)
	})
	if err != nil {
		return CommandResult{}, err
	}
	if outcomeErr != nil || stored.Error != nil {
		return CommandResult{}, replayError(stored, outcomeErr)
	}
	var result CommandResult
	if err := json.Unmarshal(stored.Payload, &result); err != nil {
		return CommandResult{}, WrapError(kernelv1.CodeInternal, "Stored command response is invalid", false, err)
	}
	return result, nil
}

func canManageTarget(organization *Organization, actor kernelv1.PrincipalID, targetID string, metadata map[string]any) bool {
	actorMembership, actorExists := organization.Memberships[actor]
	if !actorExists || actorMembership.Role == RoleOwner {
		return actorExists
	}
	target := organization.Memberships[kernelv1.PrincipalID(targetID)]
	if target != nil && target.Role == RoleOwner {
		return false
	}
	if role, ok := metadata["role"].(MembershipRole); ok && role == RoleOwner {
		return false
	}
	return true
}

func (s *Service) GetOrganization(ctx context.Context, principal kernelv1.PrincipalContext, organizationID kernelv1.TenantID, correlationID kernelv1.CorrelationID) (OrganizationSnapshot, error) {
	resource := kernelv1.ResourceRef{TenantID: organizationID, Type: "organization", ID: string(organizationID)}
	var snapshot OrganizationSnapshot
	var outcomeErr error
	err := s.store.Transact(ctx, func(tx Tx) error {
		organization, err := tx.GetOrganization(ctx, organizationID)
		if err != nil {
			outcomeErr = err
			return nil
		}
		scoped := deriveTenant(principal, organizationID)
		meta := kernelv1.CommandMeta{TenantID: organizationID, Principal: principal, CorrelationID: correlationID, Command: "GetOrganization", IdempotencyKey: "read"}
		if err := s.policy.Check(scoped, ActionOrganizationRead, resource, organization); err != nil {
			outcomeErr = err
			return s.appendAudit(ctx, tx, meta, resource, ActionOrganizationRead, kernelv1.AuditOutcomeDenied, ErrorCode(err), nil)
		}
		snapshot = snapshotOrganization(organization)
		return s.appendAudit(ctx, tx, meta, resource, ActionOrganizationRead, kernelv1.AuditOutcomeSucceeded, "", nil)
	})
	if err != nil {
		return OrganizationSnapshot{}, err
	}
	if outcomeErr != nil {
		return OrganizationSnapshot{}, outcomeErr
	}
	return snapshot, nil
}

func (s *Service) ListAuditEvents(ctx context.Context, principal kernelv1.PrincipalContext, organizationID kernelv1.TenantID, correlationID kernelv1.CorrelationID, limit int) ([]kernelv1.AuditEnvelope, error) {
	resource := kernelv1.ResourceRef{TenantID: organizationID, Type: "audit", ID: "events"}
	var records []kernelv1.AuditEnvelope
	var outcomeErr error
	err := s.store.Transact(ctx, func(tx Tx) error {
		organization, err := tx.GetOrganization(ctx, organizationID)
		if err != nil {
			outcomeErr = err
			return nil
		}
		scoped := deriveTenant(principal, organizationID)
		meta := kernelv1.CommandMeta{TenantID: organizationID, Principal: principal, CorrelationID: correlationID, Command: "ListAuditEvents", IdempotencyKey: "read"}
		if err := s.policy.Check(scoped, ActionAuditRead, resource, organization); err != nil {
			outcomeErr = err
			return s.appendAudit(ctx, tx, meta, resource, ActionAuditRead, kernelv1.AuditOutcomeDenied, ErrorCode(err), nil)
		}
		records, err = tx.ListAudit(ctx, organizationID, limit)
		return err
	})
	if err != nil {
		return nil, err
	}
	if outcomeErr != nil {
		return nil, outcomeErr
	}
	return records, nil
}

func (s *Service) Start(ctx context.Context, meta kernelv1.CommandMeta) (kernelv1.OperationRef, error) {
	if meta.Command == "" {
		meta.Command = "StartOperation"
	}
	if err := validateMeta(meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	payload := struct {
		TenantID kernelv1.TenantID `json:"tenant_id"`
		Kind     string            `json:"kind"`
	}{meta.TenantID, meta.Command}
	execution, err := s.prepareCommand(meta, tenantScope(meta, meta.TenantID, "StartOperation"), payload, ActionOperationStart, kernelv1.ResourceRef{TenantID: meta.TenantID, Type: "operation", ID: "new"})
	if err != nil {
		return kernelv1.OperationRef{}, err
	}
	var stored storedCommandResponse
	var outcomeErr error
	err = s.store.Transact(ctx, func(tx Tx) error {
		replay, claimed, err := s.claimCommand(ctx, tx, execution)
		if err != nil {
			return err
		}
		if !claimed {
			stored = *replay
			return nil
		}
		if meta.TenantID != "" {
			organization, err := tx.GetOrganization(ctx, meta.TenantID)
			if err != nil {
				outcomeErr = err
				stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
				return err
			}
			principal := deriveTenant(meta.Principal, meta.TenantID)
			if err := s.policy.Check(principal, ActionOperationStart, execution.resource, organization); err != nil {
				outcomeErr = err
				stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
				return err
			}
		} else if !meta.Principal.HasScope(ActionOperationStart) {
			outcomeErr = forbidden("principal does not have the required scope")
			stored, err = s.recordCommandFailure(ctx, tx, execution, outcomeErr, nil)
			return err
		}
		operation, err := NewOperation(kernelv1.OperationID(s.ids.New("op")), meta.TenantID, meta.Command, meta.CorrelationID, meta.CausationID, s.clock.Now())
		if err != nil {
			return err
		}
		if err := tx.InsertOperation(ctx, operation); err != nil {
			return err
		}
		ref := kernelv1.OperationRef{OperationID: operation.ID, TenantID: operation.TenantID, State: operation.State}
		execution.operationID = operation.ID
		if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
			return err
		}
		if err := s.appendAudit(ctx, tx, meta, kernelv1.ResourceRef{TenantID: meta.TenantID, Type: "operation", ID: string(operation.ID)}, ActionOperationStart, kernelv1.AuditOutcomeSucceeded, "", nil); err != nil {
			return err
		}
		encoded, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		stored = storedCommandResponse{Operation: ref, Payload: encoded}
		return s.completeCommand(ctx, tx, execution, stored)
	})
	if err != nil {
		return kernelv1.OperationRef{}, err
	}
	if outcomeErr != nil || stored.Error != nil {
		return kernelv1.OperationRef{}, replayError(stored, outcomeErr)
	}
	var ref kernelv1.OperationRef
	if err := json.Unmarshal(stored.Payload, &ref); err != nil {
		return kernelv1.OperationRef{}, err
	}
	return ref, nil
}

func (s *Service) Transition(ctx context.Context, id kernelv1.OperationID, to kernelv1.OperationState, result kernelv1.OperationResult) error {
	return s.store.Transact(ctx, func(tx Tx) error {
		operation, err := tx.GetOperation(ctx, id)
		if err != nil {
			return err
		}
		expectedVersion := operation.Version
		if err := operation.Transition(to, result, s.clock.Now()); err != nil {
			return err
		}
		if operation.Version == expectedVersion {
			return nil
		}
		if err := tx.SaveOperation(ctx, operation, expectedVersion); err != nil {
			return err
		}
		meta := kernelv1.CommandMeta{
			TenantID: operation.TenantID,
			Principal: kernelv1.PrincipalContext{
				PrincipalID: "svc_kernel_operation",
				TenantID:    operation.TenantID,
				Kind:        kernelv1.PrincipalKindService,
				Scopes:      []string{"kernel:*"},
			},
			CorrelationID:  operation.CorrelationID,
			CausationID:    operation.CausationID,
			Command:        "TransitionOperation",
			IdempotencyKey: kernelv1.IdempotencyKey("transition-" + string(id) + "-" + string(to)),
		}
		if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
			return err
		}
		return s.appendAudit(ctx, tx, meta, kernelv1.ResourceRef{TenantID: operation.TenantID, Type: "operation", ID: string(operation.ID)}, "kernel.operation.transition", kernelv1.AuditOutcomeSucceeded, "", map[string]any{"state": to})
	})
}

func (s *Service) GetOperationForPrincipal(ctx context.Context, principal kernelv1.PrincipalContext, id kernelv1.OperationID, correlationID kernelv1.CorrelationID) (kernelv1.OperationSnapshot, error) {
	var snapshot kernelv1.OperationSnapshot
	var outcomeErr error
	err := s.store.Transact(ctx, func(tx Tx) error {
		operation, err := tx.GetOperation(ctx, id)
		if err != nil {
			outcomeErr = err
			return nil
		}
		resource := kernelv1.ResourceRef{TenantID: operation.TenantID, Type: "operation", ID: string(operation.ID)}
		meta := kernelv1.CommandMeta{
			TenantID:       operation.TenantID,
			Principal:      principal,
			CorrelationID:  correlationID,
			Command:        "GetOperation",
			IdempotencyKey: "read",
		}
		if operation.TenantID == "" {
			if !principal.HasScope(ActionOperationRead) {
				outcomeErr = forbidden("principal does not have the required scope")
				return s.appendAudit(ctx, tx, meta, resource, ActionOperationRead, kernelv1.AuditOutcomeDenied, ErrorCode(outcomeErr), nil)
			}
		} else {
			organization, err := tx.GetOrganization(ctx, operation.TenantID)
			if err != nil {
				outcomeErr = err
				return nil
			}
			scoped := deriveTenant(principal, operation.TenantID)
			if err := s.policy.Check(scoped, ActionOperationRead, resource, organization); err != nil {
				outcomeErr = err
				return s.appendAudit(ctx, tx, meta, resource, ActionOperationRead, kernelv1.AuditOutcomeDenied, ErrorCode(err), nil)
			}
		}
		snapshot = operation.Snapshot()
		return s.appendAudit(ctx, tx, meta, resource, ActionOperationRead, kernelv1.AuditOutcomeSucceeded, "", nil)
	})
	if err != nil {
		return kernelv1.OperationSnapshot{}, err
	}
	if outcomeErr != nil {
		return kernelv1.OperationSnapshot{}, outcomeErr
	}
	return snapshot, nil
}

func (s *Service) Get(ctx context.Context, id kernelv1.OperationID) (kernelv1.OperationSnapshot, error) {
	var snapshot kernelv1.OperationSnapshot
	err := s.store.View(ctx, func(reader Reader) error {
		operation, err := reader.GetOperation(ctx, id)
		if err != nil {
			return err
		}
		snapshot = operation.Snapshot()
		return nil
	})
	return snapshot, err
}

func (s *Service) CancelOperation(ctx context.Context, command CancelOperationCommand) (CommandResult, error) {
	command.Meta.Command = "CancelOperation"
	if err := validateMeta(command.Meta); err != nil {
		return CommandResult{}, err
	}
	payload := struct {
		OperationID kernelv1.OperationID `json:"operation_id"`
	}{command.OperationID}
	// Scope is corrected after the operation is loaded; operation ID makes the
	// platform-level scope collision-safe for the initial idempotency claim.
	execution, err := s.prepareCommand(command.Meta, platformScope(command.Meta, command.Meta.Command+":"+string(command.OperationID)), payload, ActionOperationCancel, kernelv1.ResourceRef{Type: "operation", ID: string(command.OperationID)})
	if err != nil {
		return CommandResult{}, err
	}
	var stored storedCommandResponse
	var outcomeErr error
	err = s.store.Transact(ctx, func(tx Tx) error {
		replay, claimed, err := s.claimCommand(ctx, tx, execution)
		if err != nil {
			return err
		}
		if !claimed {
			stored = *replay
			return nil
		}
		operation, err := tx.GetOperation(ctx, command.OperationID)
		if err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		organization, err := tx.GetOrganization(ctx, operation.TenantID)
		if err != nil {
			outcomeErr = err
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		resource := kernelv1.ResourceRef{TenantID: operation.TenantID, Type: "operation", ID: string(operation.ID)}
		principal := deriveTenant(command.Meta.Principal, operation.TenantID)
		if err := s.policy.Check(principal, ActionOperationCancel, resource, organization); err != nil {
			outcomeErr = err
			execution.resource = resource
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		expectedVersion := operation.Version
		if err := operation.Cancel(s.clock.Now()); err != nil {
			outcomeErr = err
			execution.resource = resource
			stored, err = s.recordCommandFailure(ctx, tx, execution, err, nil)
			return err
		}
		if operation.Version != expectedVersion {
			if err := tx.SaveOperation(ctx, operation, expectedVersion); err != nil {
				return err
			}
			if err := s.appendOperationEvent(ctx, tx, operation, command.Meta); err != nil {
				return err
			}
		}
		ref := kernelv1.OperationRef{OperationID: operation.ID, TenantID: operation.TenantID, State: operation.State}
		execution.operationID = operation.ID
		execution.resource = resource
		if err := s.appendAudit(ctx, tx, command.Meta, resource, ActionOperationCancel, kernelv1.AuditOutcomeSucceeded, "", nil); err != nil {
			return err
		}
		result := CommandResult{Operation: ref}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		stored = storedCommandResponse{Operation: ref, Payload: encoded}
		return s.completeCommand(ctx, tx, execution, stored)
	})
	if err != nil {
		return CommandResult{}, err
	}
	if outcomeErr != nil || stored.Error != nil {
		return CommandResult{}, replayError(stored, outcomeErr)
	}
	var result CommandResult
	if err := json.Unmarshal(stored.Payload, &result); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (s *Service) prepareCommand(meta kernelv1.CommandMeta, scope string, payload any, action string, resource kernelv1.ResourceRef) (commandExecution, error) {
	fingerprint, err := CanonicalFingerprint(payload)
	if err != nil {
		return commandExecution{}, WrapError(kernelv1.CodeInvalidArgument, "Command payload cannot be canonicalized", false, err)
	}
	return commandExecution{scope: scope, fingerprint: fingerprint, meta: meta, action: action, resource: resource}, nil
}

func (s *Service) claimCommand(ctx context.Context, tx Tx, execution commandExecution) (*storedCommandResponse, bool, error) {
	now := s.clock.Now()
	existing, claimed, err := tx.ClaimIdempotency(ctx, IdempotencyRecord{
		Scope:       execution.scope,
		Key:         execution.meta.IdempotencyKey,
		Fingerprint: execution.fingerprint,
		Status:      IdempotencyStarted,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		return nil, false, err
	}
	if claimed {
		return nil, true, nil
	}
	if existing.Fingerprint != execution.fingerprint {
		return nil, false, NewError(kernelv1.CodeIdempotencyConflict, "Idempotency key was already used with a different payload")
	}
	if existing.Status != IdempotencyCompleted {
		return nil, false, &DomainError{Code: kernelv1.CodeConflict, Message: "Command with this idempotency key is still in progress", Retryable: true}
	}
	var response storedCommandResponse
	if err := json.Unmarshal(existing.Response, &response); err != nil {
		return nil, false, WrapError(kernelv1.CodeInternal, "Stored idempotency response is invalid", false, err)
	}
	return &response, false, nil
}

func (s *Service) completeCommand(ctx context.Context, tx Tx, execution commandExecution, response storedCommandResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return WrapError(kernelv1.CodeInternal, "Could not encode idempotency response", false, err)
	}
	return tx.CompleteIdempotency(ctx, execution.scope, execution.meta.IdempotencyKey, response.Operation.OperationID, encoded, s.clock.Now())
}

func (s *Service) recordCommandFailure(ctx context.Context, tx Tx, execution commandExecution, commandErr error, metadata map[string]any) (storedCommandResponse, error) {
	tenantID := execution.resource.TenantID
	if tenantID == "" {
		tenantID = execution.meta.TenantID
	}
	operation, err := s.recordFailedOperation(ctx, tx, execution.meta, tenantID, execution.meta.Command, commandErr)
	if err != nil {
		return storedCommandResponse{}, err
	}
	publicErr := ToPublicError(commandErr, operation.OperationID)
	outcome := kernelv1.AuditOutcomeFailed
	if publicErr.Code == kernelv1.CodeForbidden {
		outcome = kernelv1.AuditOutcomeDenied
	}
	if err := s.appendAudit(ctx, tx, execution.meta, execution.resource, execution.action, outcome, publicErr.Code, metadata); err != nil {
		return storedCommandResponse{}, err
	}
	response := storedCommandResponse{Operation: operation, Error: &publicErr}
	if err := s.completeCommand(ctx, tx, execution, response); err != nil {
		return storedCommandResponse{}, err
	}
	return response, nil
}

func (s *Service) recordSuccessfulOperation(ctx context.Context, tx Tx, meta kernelv1.CommandMeta, tenantID kernelv1.TenantID, kind string, data any) (kernelv1.OperationRef, error) {
	operation, err := NewOperation(kernelv1.OperationID(s.ids.New("op")), tenantID, kind, meta.CorrelationID, meta.CausationID, s.clock.Now())
	if err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := tx.InsertOperation(ctx, operation); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	expected := operation.Version
	if err := operation.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{Code: "RUNNING"}, s.clock.Now()); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := tx.SaveOperation(ctx, operation, expected); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return kernelv1.OperationRef{}, err
	}
	expected = operation.Version
	if err := operation.Transition(kernelv1.OperationSucceeded, kernelv1.OperationResult{Code: "SUCCEEDED", Data: encoded}, s.clock.Now()); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := tx.SaveOperation(ctx, operation, expected); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	return kernelv1.OperationRef{OperationID: operation.ID, TenantID: operation.TenantID, State: operation.State}, nil
}

func (s *Service) recordFailedOperation(ctx context.Context, tx Tx, meta kernelv1.CommandMeta, tenantID kernelv1.TenantID, kind string, commandErr error) (kernelv1.OperationRef, error) {
	operation, err := NewOperation(kernelv1.OperationID(s.ids.New("op")), tenantID, kind, meta.CorrelationID, meta.CausationID, s.clock.Now())
	if err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := tx.InsertOperation(ctx, operation); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	publicErr := ToPublicError(commandErr, operation.ID)
	expected := operation.Version
	if err := operation.Transition(kernelv1.OperationFailed, kernelv1.OperationResult{Code: "FAILED", Error: &publicErr}, s.clock.Now()); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := tx.SaveOperation(ctx, operation, expected); err != nil {
		return kernelv1.OperationRef{}, err
	}
	if err := s.appendOperationEvent(ctx, tx, operation, meta); err != nil {
		return kernelv1.OperationRef{}, err
	}
	return kernelv1.OperationRef{OperationID: operation.ID, TenantID: operation.TenantID, State: operation.State}, nil
}

func (s *Service) appendOperationEvent(ctx context.Context, tx Tx, operation *Operation, meta kernelv1.CommandMeta) error {
	return s.appendEvent(ctx, tx, EventOperationChanged, operation.TenantID, string(operation.ID), meta, map[string]any{
		"operation_id": operation.ID,
		"kind":         operation.Kind,
		"state":        operation.State,
		"version":      operation.Version,
		"error_code": func() string {
			if operation.Result.Error != nil {
				return operation.Result.Error.Code
			}
			return ""
		}(),
	})
}

func (s *Service) appendEvent(ctx context.Context, tx Tx, eventType string, tenantID kernelv1.TenantID, aggregateID string, meta kernelv1.CommandMeta, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return WrapError(kernelv1.CodeInternal, "Could not encode domain event", false, err)
	}
	event := kernelv1.DomainEventEnvelope[json.RawMessage]{
		EventID:       s.ids.New("evt"),
		Type:          eventType,
		Version:       1,
		TenantID:      tenantID,
		AggregateID:   aggregateID,
		CorrelationID: meta.CorrelationID,
		CausationID:   meta.CausationID,
		OccurredAt:    s.clock.Now(),
		Payload:       encoded,
	}
	if err := event.Validate(); err != nil {
		return WrapError(kernelv1.CodeInternal, "Generated domain event is invalid", false, err)
	}
	return tx.AppendOutbox(ctx, OutboxRecord{Event: event, State: OutboxPending, CreatedAt: s.clock.Now()})
}

func (s *Service) appendAudit(ctx context.Context, tx Tx, meta kernelv1.CommandMeta, resource kernelv1.ResourceRef, action string, outcome kernelv1.AuditOutcome, errorCode string, metadata map[string]any) error {
	record := kernelv1.AuditEnvelope{
		AuditID:       s.ids.New("aud"),
		TenantID:      resource.TenantID,
		Actor:         meta.Principal,
		Action:        action,
		Resource:      resource,
		CorrelationID: meta.CorrelationID,
		Outcome:       outcome,
		ErrorCode:     errorCode,
		Metadata:      RedactMetadata(metadata),
		OccurredAt:    s.clock.Now(),
	}
	if err := tx.AppendAudit(ctx, record); err != nil {
		return err
	}
	payload := map[string]any{
		"audit_id":   record.AuditID,
		"actor_id":   record.Actor.PrincipalID,
		"action":     record.Action,
		"resource":   record.Resource,
		"outcome":    record.Outcome,
		"error_code": record.ErrorCode,
		"metadata":   record.Metadata,
	}
	aggregateID := record.Resource.ID
	if aggregateID == "" {
		aggregateID = record.AuditID
	}
	return s.appendEvent(ctx, tx, EventAuditRecorded, record.TenantID, aggregateID, meta, payload)
}

func replayError(stored storedCommandResponse, fallback error) error {
	if stored.Error != nil {
		return &DomainError{
			Code:      stored.Error.Code,
			Message:   stored.Error.Message,
			Retryable: stored.Error.Retryable,
			Details:   cloneMap(stored.Error.Details),
		}
	}
	return fallback
}

func validateMeta(meta kernelv1.CommandMeta) error {
	if err := meta.Validate(); err != nil {
		return invalidArgument(err.Error())
	}
	return nil
}

func deriveTenant(principal kernelv1.PrincipalContext, tenantID kernelv1.TenantID) kernelv1.PrincipalContext {
	if principal.TenantID == "" {
		return principal.WithTenant(tenantID)
	}
	return principal
}

func platformScope(meta kernelv1.CommandMeta, command string) string {
	return strings.Join([]string{"platform", string(meta.Principal.PrincipalID), command}, "/")
}

func tenantScope(meta kernelv1.CommandMeta, tenantID kernelv1.TenantID, command string) string {
	return strings.Join([]string{"tenant", string(tenantID), string(meta.Principal.PrincipalID), command}, "/")
}

func snapshotOrganization(organization *Organization) OrganizationSnapshot {
	snapshot := OrganizationSnapshot{
		OrganizationID: organization.ID,
		Name:           organization.Name,
		Slug:           organization.Slug,
		Version:        organization.Version,
		CreatedAt:      organization.CreatedAt,
		UpdatedAt:      organization.UpdatedAt,
		Memberships:    make([]MembershipSnapshot, 0, len(organization.Memberships)),
	}
	for _, membership := range organization.Memberships {
		snapshot.Memberships = append(snapshot.Memberships, MembershipSnapshot{
			PrincipalID: membership.PrincipalID,
			Role:        membership.Role,
			State:       membership.State,
			InvitedAt:   membership.InvitedAt,
			AcceptedAt:  membership.AcceptedAt,
			UpdatedAt:   membership.UpdatedAt,
			Version:     membership.Version,
		})
	}
	// Stable ordering is part of the public response and idempotency record.
	for i := 0; i < len(snapshot.Memberships); i++ {
		for j := i + 1; j < len(snapshot.Memberships); j++ {
			if snapshot.Memberships[j].PrincipalID < snapshot.Memberships[i].PrincipalID {
				snapshot.Memberships[i], snapshot.Memberships[j] = snapshot.Memberships[j], snapshot.Memberships[i]
			}
		}
	}
	return snapshot
}

var _ kernelv1.OperationService = (*Service)(nil)
