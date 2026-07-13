// Package execution implements the project-scoped v2 operation kernel.
package execution

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
)

type ErrorCode string

const (
	CodePermissionDenied    ErrorCode = "PERMISSION_DENIED"
	CodeCredentialExpired   ErrorCode = "CREDENTIAL_EXPIRED"
	CodeIdempotencyConflict ErrorCode = "IDEMPOTENCY_CONFLICT"
	CodeInvalidArgument     ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound            ErrorCode = "NOT_FOUND"
	CodeConflict            ErrorCode = "CONFLICT"
)

type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string { return e.Message }

func IsCode(err error, code ErrorCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

func fail(code ErrorCode, message string) error { return &Error{Code: code, Message: message} }

type VerifiedPrincipal struct {
	Identity kernelv2.Principal
	Scopes   map[string]struct{}
	Lease    kernelv2.CredentialLease
}

func (p VerifiedPrincipal) Authorize(target TargetScope, requiredScope string, now time.Time) error {
	if err := p.Identity.Validate(); err != nil {
		return fail(CodePermissionDenied, "verified principal is invalid")
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if p.Identity.TenantID != target.TenantID || p.Identity.ProjectID != target.ProjectID {
		return fail(CodePermissionDenied, "principal cannot cross tenant or project scope")
	}
	if p.Identity.Kind == kernelv2.PrincipalWorkspace {
		if target.WorkspaceID == "" || p.Identity.WorkspaceID != target.WorkspaceID {
			return fail(CodePermissionDenied, "workspace principal cannot escape workspace scope")
		}
	}
	if strings.Contains(target.ProjectID, "*") || strings.Contains(target.WorkspaceID, "*") {
		return fail(CodePermissionDenied, "wildcard resource scope is forbidden")
	}
	if _, allowed := p.Scopes[requiredScope]; !allowed {
		return fail(CodePermissionDenied, "principal scope is not granted")
	}
	if err := p.Lease.Validate(now); err != nil {
		return fail(CodeCredentialExpired, "credential lease is expired or invalid")
	}
	return nil
}

func (p VerifiedPrincipal) CanApproveHumanAction(requiredScope string, now time.Time) error {
	if p.Identity.Kind != kernelv2.PrincipalUser {
		return fail(CodePermissionDenied, "approval requires a verified human principal")
	}
	return p.Authorize(TargetScope{TenantID: p.Identity.TenantID, ProjectID: p.Identity.ProjectID}, requiredScope, now)
}

type TargetScope struct {
	TenantID    string `json:"tenant_id"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Environment string `json:"environment,omitempty"`
}

func (t TargetScope) Validate() error {
	if strings.TrimSpace(t.TenantID) == "" || strings.TrimSpace(t.ProjectID) == "" {
		return fail(CodeInvalidArgument, "target tenant and project are required")
	}
	return nil
}

type NodeSpec struct {
	NodeID     string
	Kind       string
	DependsOn  []string
	Cancelable bool
}

type Node struct {
	NodeID              string                  `json:"node_id"`
	Kind                string                  `json:"kind"`
	DependsOn           []string                `json:"depends_on,omitempty"`
	Cancelable          bool                    `json:"cancelable"`
	CancellationPending bool                    `json:"cancellation_pending"`
	State               kernelv2.OperationState `json:"state"`
	Checkpoint          *Checkpoint             `json:"checkpoint,omitempty"`
}

type Checkpoint struct {
	CheckpointID string                    `json:"checkpoint_id"`
	Kind         string                    `json:"kind"`
	Payload      json.RawMessage           `json:"payload"`
	PayloadHash  string                    `json:"payload_hash"`
	Lease        *kernelv2.CredentialLease `json:"credential_lease,omitempty"`
	RecordedAt   time.Time                 `json:"recorded_at"`
}

type Graph struct {
	GraphID               string                  `json:"graph_id"`
	TenantID              string                  `json:"tenant_id"`
	ProjectID             string                  `json:"project_id"`
	WorkspaceID           string                  `json:"workspace_id,omitempty"`
	ParentState           kernelv2.OperationState `json:"parent_state"`
	Nodes                 []Node                  `json:"nodes"`
	IdempotencyKey        string                  `json:"idempotency_key"`
	CommandFingerprint    string                  `json:"command_fingerprint"`
	CancellationRequested bool                    `json:"cancellation_requested"`
	Version               int64                   `json:"version"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
}

func NewGraph(id string, target TargetScope, idempotencyKey, fingerprint string, specs []NodeSpec, now time.Time) (*Graph, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(fingerprint) == "" || len(specs) == 0 || len(specs) > 256 {
		return nil, fail(CodeInvalidArgument, "invalid operation graph identity")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	nodes := make([]Node, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for index, spec := range specs {
		if strings.TrimSpace(spec.NodeID) == "" || strings.TrimSpace(spec.Kind) == "" {
			return nil, fail(CodeInvalidArgument, "operation node identity is required")
		}
		if _, exists := seen[spec.NodeID]; exists {
			return nil, fail(CodeInvalidArgument, "duplicate operation node")
		}
		seen[spec.NodeID] = struct{}{}
		nodes[index] = Node{NodeID: spec.NodeID, Kind: spec.Kind, DependsOn: append([]string(nil), spec.DependsOn...), Cancelable: spec.Cancelable, State: kernelv2.OperationPending}
	}
	for _, node := range nodes {
		for _, dependency := range node.DependsOn {
			if dependency == node.NodeID {
				return nil, fail(CodeInvalidArgument, "operation node cannot depend on itself")
			}
			if _, exists := seen[dependency]; !exists {
				return nil, fail(CodeInvalidArgument, "operation dependency does not exist")
			}
		}
	}
	if hasDependencyCycle(nodes) {
		return nil, fail(CodeInvalidArgument, "operation graph contains a dependency cycle")
	}
	now = now.UTC()
	return &Graph{GraphID: id, TenantID: target.TenantID, ProjectID: target.ProjectID, WorkspaceID: target.WorkspaceID, ParentState: kernelv2.OperationPending, Nodes: nodes, IdempotencyKey: idempotencyKey, CommandFingerprint: fingerprint, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func hasDependencyCycle(nodes []Node) bool {
	edges := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		edges[node.NodeID] = node.DependsOn
	}
	visiting := make(map[string]bool, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var visit func(string) bool
	visit = func(node string) bool {
		if visiting[node] {
			return true
		}
		if visited[node] {
			return false
		}
		visiting[node] = true
		for _, dependency := range edges[node] {
			if visit(dependency) {
				return true
			}
		}
		visiting[node] = false
		visited[node] = true
		return false
	}
	for node := range edges {
		if visit(node) {
			return true
		}
	}
	return false
}

func (g *Graph) Clone() *Graph {
	if g == nil {
		return nil
	}
	copy := *g
	copy.Nodes = make([]Node, len(g.Nodes))
	for index := range g.Nodes {
		copy.Nodes[index] = g.Nodes[index]
		copy.Nodes[index].DependsOn = append([]string(nil), g.Nodes[index].DependsOn...)
		if g.Nodes[index].Checkpoint != nil {
			checkpoint := *g.Nodes[index].Checkpoint
			checkpoint.Payload = append(json.RawMessage(nil), g.Nodes[index].Checkpoint.Payload...)
			if g.Nodes[index].Checkpoint.Lease != nil {
				lease := *g.Nodes[index].Checkpoint.Lease
				checkpoint.Lease = &lease
			}
			copy.Nodes[index].Checkpoint = &checkpoint
		}
	}
	return &copy
}

func (g *Graph) node(nodeID string) (*Node, error) {
	for index := range g.Nodes {
		if g.Nodes[index].NodeID == nodeID {
			return &g.Nodes[index], nil
		}
	}
	return nil, fail(CodeNotFound, "operation node not found")
}

func (g *Graph) PutCheckpoint(nodeID, checkpointID, kind string, payload json.RawMessage, lease *kernelv2.CredentialLease, now time.Time) (bool, error) {
	node, err := g.node(nodeID)
	if err != nil {
		return false, err
	}
	if node.Checkpoint != nil {
		return false, nil
	}
	if checkpointID == "" || kind == "" || len(payload) == 0 || !json.Valid(payload) {
		return false, fail(CodeInvalidArgument, "invalid checkpoint")
	}
	sum := sha256.Sum256(payload)
	node.Checkpoint = &Checkpoint{CheckpointID: checkpointID, Kind: kind, Payload: append(json.RawMessage(nil), payload...), PayloadHash: fmt.Sprintf("sha256:%x", sum), Lease: lease, RecordedAt: now.UTC()}
	node.State = kernelv2.OperationWaitingApproval
	g.ParentState = kernelv2.OperationWaitingApproval
	g.Version++
	g.UpdatedAt = now.UTC()
	return true, nil
}

func (g *Graph) ResumeApproved(nodeID string, binding kernelv2.ApprovalBinding, now time.Time) error {
	node, err := g.node(nodeID)
	if err != nil {
		return err
	}
	if node.Checkpoint == nil || node.State != kernelv2.OperationWaitingApproval {
		return fail(CodeConflict, "operation node is not waiting for approval")
	}
	if err := binding.Validate(now); err != nil || binding.PlanHash != node.Checkpoint.PayloadHash {
		return fail(CodePermissionDenied, "approval does not match checkpoint plan")
	}
	node.State = kernelv2.OperationRunning
	g.ParentState = kernelv2.OperationRunning
	g.Version++
	g.UpdatedAt = now.UTC()
	return nil
}

func (g *Graph) CompleteNode(nodeID string, now time.Time) error {
	node, err := g.node(nodeID)
	if err != nil {
		return err
	}
	if node.State.Terminal() {
		return nil
	}
	node.State = kernelv2.OperationSucceeded
	g.recalculateParent()
	g.Version++
	g.UpdatedAt = now.UTC()
	return nil
}

func (g *Graph) RequestCancellation(now time.Time) []string {
	if g.ParentState.Terminal() {
		return nil
	}
	g.CancellationRequested = true
	cancel := make([]string, 0, len(g.Nodes))
	for index := range g.Nodes {
		node := &g.Nodes[index]
		if node.State.Terminal() || !node.Cancelable || node.CancellationPending {
			continue
		}
		node.CancellationPending = true
		cancel = append(cancel, node.NodeID)
	}
	if len(cancel) == 0 {
		g.recalculateParent()
	} else {
		g.ParentState = kernelv2.OperationWaitingDependency
	}
	sort.Strings(cancel)
	g.Version++
	g.UpdatedAt = now.UTC()
	return cancel
}

func (g *Graph) AcknowledgeCancellation(nodeID string, now time.Time) error {
	node, err := g.node(nodeID)
	if err != nil {
		return err
	}
	if !node.CancellationPending {
		return fail(CodeConflict, "node cancellation was not requested")
	}
	node.CancellationPending = false
	node.State = kernelv2.OperationCanceled
	g.recalculateParent()
	g.Version++
	g.UpdatedAt = now.UTC()
	return nil
}

func (g *Graph) recalculateParent() {
	allTerminal := true
	allSucceeded := true
	for _, node := range g.Nodes {
		allTerminal = allTerminal && node.State.Terminal()
		allSucceeded = allSucceeded && node.State == kernelv2.OperationSucceeded
	}
	if !allTerminal {
		if g.CancellationRequested {
			g.ParentState = kernelv2.OperationWaitingDependency
		}
		return
	}
	if g.CancellationRequested {
		g.ParentState = kernelv2.OperationCanceled
	} else if allSucceeded {
		g.ParentState = kernelv2.OperationSucceeded
	} else {
		g.ParentState = kernelv2.OperationFailed
	}
}

func CanonicalFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fail(CodeInvalidArgument, "command payload cannot be encoded")
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", fail(CodeInvalidArgument, "command payload is invalid")
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func RejectScopeOverrides(arguments json.RawMessage) error {
	if len(arguments) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(arguments, &value); err != nil {
		return fail(CodeInvalidArgument, "tool arguments are invalid")
	}
	if containsReservedScope(value) {
		return fail(CodePermissionDenied, "tool arguments cannot override verified scope")
	}
	return nil
}

func containsReservedScope(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			switch strings.ToLower(key) {
			case "tenant_id", "project_id", "user_id", "agent_id", "workspace_id":
				return true
			}
			if containsReservedScope(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsReservedScope(nested) {
				return true
			}
		}
	}
	return false
}
