package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ New(string) string }

type Store interface {
	ClaimGraph(context.Context, *Graph) (*Graph, bool, error)
	GetGraph(context.Context, string) (*Graph, error)
	UpdateGraph(context.Context, string, int64, func(*Graph) error) (*Graph, error)
	AppendAudit(context.Context, AuditRecord) error
}

type AuditRecord struct {
	AuditID    string          `json:"audit_id"`
	TenantID   string          `json:"tenant_id"`
	ProjectID  string          `json:"project_id"`
	Principal  string          `json:"principal"`
	Action     string          `json:"action"`
	Outcome    string          `json:"outcome"`
	ErrorCode  ErrorCode       `json:"error_code,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type GraphFactory interface {
	Build(context.Context) ([]NodeSpec, error)
}

type GraphFactoryFunc func(context.Context) ([]NodeSpec, error)

func (f GraphFactoryFunc) Build(ctx context.Context) ([]NodeSpec, error) { return f(ctx) }

type CreateWorkflowCommand struct {
	Principal      VerifiedPrincipal
	Target         TargetScope
	RequiredScope  string
	IdempotencyKey string
	Arguments      json.RawMessage
	Payload        any
	Factory        GraphFactory
}

type Service struct {
	Store Store
	Clock Clock
	IDs   IDGenerator
}

func (s *Service) CreateWorkflow(ctx context.Context, command CreateWorkflowCommand) (*Graph, error) {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil || command.Factory == nil {
		return nil, fail(CodeInvalidArgument, "execution kernel dependencies are unavailable")
	}
	now := s.Clock.Now().UTC()
	if err := command.Principal.Authorize(command.Target, command.RequiredScope, now); err != nil {
		s.auditDenied(ctx, command, err, now)
		return nil, err
	}
	if err := RejectScopeOverrides(command.Arguments); err != nil {
		s.auditDenied(ctx, command, err, now)
		return nil, err
	}
	fingerprint, err := CanonicalFingerprint(command.Payload)
	if err != nil {
		return nil, err
	}
	// Credential validation intentionally happens before the idempotency lookup,
	// so replay cannot extend or resurrect an expired lease.
	specs, err := command.Factory.Build(ctx)
	if err != nil {
		return nil, err
	}
	graph, err := NewGraph(s.IDs.New("opg"), command.Target, command.IdempotencyKey, fingerprint, specs, now)
	if err != nil {
		return nil, err
	}
	stored, claimed, err := s.Store.ClaimGraph(ctx, graph)
	if err != nil {
		return nil, err
	}
	if !claimed && stored.CommandFingerprint != fingerprint {
		return nil, fail(CodeIdempotencyConflict, "idempotency key was used with a different canonical payload")
	}
	return stored, nil
}

func (s *Service) EnsureCheckpoint(ctx context.Context, graphID, nodeID, kind string, effect func(context.Context) (json.RawMessage, *kernelv2.CredentialLease, error)) (*Graph, error) {
	graph, err := s.Store.GetGraph(ctx, graphID)
	if err != nil {
		return nil, err
	}
	node, err := graph.node(nodeID)
	if err != nil {
		return nil, err
	}
	if node.Checkpoint != nil {
		return graph, nil
	}
	payload, lease, err := effect(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.UpdateGraph(ctx, graphID, graph.Version, func(candidate *Graph) error {
		_, err := candidate.PutCheckpoint(nodeID, s.IDs.New("checkpoint"), kind, payload, lease, s.Clock.Now())
		return err
	})
}

func (s *Service) ResumeApproved(ctx context.Context, graphID, nodeID string, binding kernelv2.ApprovalBinding) (*Graph, error) {
	graph, err := s.Store.GetGraph(ctx, graphID)
	if err != nil {
		return nil, err
	}
	return s.Store.UpdateGraph(ctx, graphID, graph.Version, func(candidate *Graph) error {
		return candidate.ResumeApproved(nodeID, binding, s.Clock.Now())
	})
}

func (s *Service) Cancel(ctx context.Context, graphID string) (*Graph, []string, error) {
	graph, err := s.Store.GetGraph(ctx, graphID)
	if err != nil {
		return nil, nil, err
	}
	var canceled []string
	updated, err := s.Store.UpdateGraph(ctx, graphID, graph.Version, func(candidate *Graph) error {
		canceled = candidate.RequestCancellation(s.Clock.Now())
		return nil
	})
	return updated, canceled, err
}

func (s *Service) AcknowledgeCancellation(ctx context.Context, graphID, nodeID string) (*Graph, error) {
	graph, err := s.Store.GetGraph(ctx, graphID)
	if err != nil {
		return nil, err
	}
	return s.Store.UpdateGraph(ctx, graphID, graph.Version, func(candidate *Graph) error {
		return candidate.AcknowledgeCancellation(nodeID, s.Clock.Now())
	})
}

func (s *Service) GrantApproval(principal VerifiedPrincipal, requiredScope string) error {
	return principal.CanApproveHumanAction(requiredScope, s.Clock.Now())
}

func (s *Service) auditDenied(ctx context.Context, command CreateWorkflowCommand, cause error, now time.Time) {
	code := CodePermissionDenied
	var typed *Error
	if errors.As(cause, &typed) {
		code = typed.Code
	}
	_ = s.Store.AppendAudit(ctx, AuditRecord{AuditID: s.IDs.New("audit"), TenantID: command.Principal.Identity.TenantID, ProjectID: command.Principal.Identity.ProjectID, Principal: command.Principal.Identity.SubjectID, Action: command.RequiredScope, Outcome: "DENIED", ErrorCode: code, OccurredAt: now})
}
