package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

var ErrSourceApprovalRequired = errors.New("exact source change approval is required")

type SourceChangePlan struct {
	PlanID                   string               `json:"plan_id"`
	TenantID                 string               `json:"-"`
	ProjectID                string               `json:"project_id"`
	WorkspaceID              string               `json:"workspace_id"`
	RepositoryID             string               `json:"repository_id"`
	BaseSHA                  string               `json:"base_sha"`
	TargetBranch             string               `json:"target_branch"`
	ActorID                  string               `json:"actor_id"`
	TaskID                   string               `json:"task_id"`
	Files                    []sourcev2.PatchFile `json:"files"`
	PlanHash                 string               `json:"plan_hash"`
	RequiresApproval         bool                 `json:"requires_approval"`
	IdempotencyKey           string               `json:"-"`
	RequestFingerprint       string               `json:"-"`
	AuthorizedIdempotencyKey string               `json:"-"`
	AuthorizationFingerprint string               `json:"-"`
	AuthorizedAt             time.Time            `json:"authorized_at,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	ExpiresAt                time.Time            `json:"expires_at"`
}

type SourceApprovalGrant struct {
	GrantID        string    `json:"approval_grant_id"`
	TenantID       string    `json:"-"`
	ProjectID      string    `json:"project_id"`
	PlanID         string    `json:"plan_id"`
	PlanHash       string    `json:"plan_hash"`
	WorkspaceID    string    `json:"workspace_id"`
	RepositoryID   string    `json:"repository_id"`
	TargetBranch   string    `json:"target_branch"`
	ActorID        string    `json:"actor_id"`
	ApproverUserID string    `json:"approver_user_id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	ConsumedAt     time.Time `json:"consumed_at,omitempty"`
}

type SourceChangePlanRequest struct {
	Scope          Scope
	WorkspaceID    string
	RepositoryID   string
	BaseSHA        string
	TargetBranch   string
	TaskID         string
	Files          []sourcev2.PatchMutation
	IdempotencyKey string
}

type GrantSourceApprovalRequest struct {
	TenantID       string
	ProjectID      string
	PlanID         string
	ApproverUserID string
	ExpiresAt      time.Time
}

type AuthorizeSourceCommitRequest struct {
	Scope           Scope
	WorkspaceID     string
	RepositoryID    string
	BaseSHA         string
	TargetBranch    string
	TaskID          string
	PlanID          string
	PlanHash        string
	ApprovalGrantID string
	IdempotencyKey  string
}

func (s *Service) PlanSourceChange(ctx context.Context, request SourceChangePlanRequest) (SourceChangePlan, error) {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil || request.Scope.validate() != nil || strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.RepositoryID) == "" || !validSourceSHA(request.BaseSHA) || !sourcev2.ValidBranch(request.TargetBranch) || strings.TrimSpace(request.TaskID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 || len(request.Files) == 0 || len(request.Files) > 126 {
		return SourceChangePlan{}, errors.New("invalid source change plan request")
	}
	files := make([]sourcev2.PatchFile, 0, len(request.Files))
	seen := make(map[string]struct{}, len(request.Files))
	requiresApproval := false
	for _, mutation := range request.Files {
		if mutation.Validate() != nil {
			return SourceChangePlan{}, errors.New("invalid source change file")
		}
		if _, exists := seen[mutation.Path]; exists {
			return SourceChangePlan{}, errors.New("duplicate source change file")
		}
		seen[mutation.Path] = struct{}{}
		contentHash := "sha256:" + strings.Repeat("0", 64)
		if !mutation.Delete {
			content, _ := base64.StdEncoding.Strict().DecodeString(mutation.ContentBase64)
			digest := sha256.Sum256(content)
			for index := range content {
				content[index] = 0
			}
			contentHash = "sha256:" + hex.EncodeToString(digest[:])
		}
		files = append(files, sourcev2.PatchFile{Path: mutation.Path, ContentHash: contentHash, Delete: mutation.Delete})
		requiresApproval = requiresApproval || protectedProductionPath(mutation.Path)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	changeSet := sourcev2.ChangeSet{RepositoryID: request.RepositoryID, BaseSHA: request.BaseSHA, TargetBranch: request.TargetBranch, Files: files}
	planHash, err := sourceChangeHash(changeSet)
	if err != nil {
		return SourceChangePlan{}, err
	}
	fingerprint, err := sourceChangeHash(struct {
		TenantID       string `json:"tenant_id"`
		ProjectID      string `json:"project_id"`
		WorkspaceID    string `json:"workspace_id"`
		ActorID        string `json:"actor_id"`
		TaskID         string `json:"task_id"`
		IdempotencyKey string `json:"idempotency_key"`
		PlanHash       string `json:"plan_hash"`
	}{request.Scope.TenantID, request.Scope.ProjectID, request.WorkspaceID, request.Scope.ActorID, request.TaskID, request.IdempotencyKey, planHash})
	if err != nil {
		return SourceChangePlan{}, err
	}
	now := s.Clock.Now().UTC()
	return s.Store.CreateSourceChangePlan(ctx, SourceChangePlan{
		PlanID: s.IDs.New("source-plan"), TenantID: request.Scope.TenantID, ProjectID: request.Scope.ProjectID,
		WorkspaceID: request.WorkspaceID, RepositoryID: request.RepositoryID, BaseSHA: request.BaseSHA,
		TargetBranch: request.TargetBranch, ActorID: request.Scope.ActorID, TaskID: request.TaskID,
		Files: files, PlanHash: planHash, RequiresApproval: requiresApproval,
		IdempotencyKey: request.IdempotencyKey, RequestFingerprint: fingerprint, CreatedAt: now, ExpiresAt: now.Add(20 * time.Minute),
	})
}

func (s *Service) GetSourceChangePlan(ctx context.Context, tenantID, projectID, planID string) (SourceChangePlan, error) {
	if s == nil || s.Store == nil || tenantID == "" || projectID == "" || planID == "" {
		return SourceChangePlan{}, ErrNotFound
	}
	return s.Store.GetSourceChangePlan(ctx, tenantID, projectID, planID)
}

func (s *Service) GrantSourceApproval(ctx context.Context, request GrantSourceApprovalRequest) (SourceApprovalGrant, error) {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil || request.TenantID == "" || request.ProjectID == "" || request.PlanID == "" || request.ApproverUserID == "" {
		return SourceApprovalGrant{}, errors.New("invalid source approval request")
	}
	now := s.Clock.Now().UTC()
	plan, err := s.Store.GetSourceChangePlan(ctx, request.TenantID, request.ProjectID, request.PlanID)
	if err != nil {
		return SourceApprovalGrant{}, err
	}
	if !plan.RequiresApproval || !request.ExpiresAt.After(now) || !plan.ExpiresAt.After(now) || !plan.AuthorizedAt.IsZero() {
		return SourceApprovalGrant{}, ErrConflict
	}
	expiresAt := request.ExpiresAt.UTC()
	if plan.ExpiresAt.Before(expiresAt) {
		expiresAt = plan.ExpiresAt
	}
	return s.Store.CreateSourceApproval(ctx, SourceApprovalGrant{
		GrantID: s.IDs.New("source-approval"), TenantID: plan.TenantID, ProjectID: plan.ProjectID,
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, WorkspaceID: plan.WorkspaceID, RepositoryID: plan.RepositoryID,
		TargetBranch: plan.TargetBranch, ActorID: plan.ActorID, ApproverUserID: request.ApproverUserID,
		CreatedAt: now, ExpiresAt: expiresAt,
	})
}

func (s *Service) CheckSourceApproval(ctx context.Context, scope Scope, planID, grantID string) (SourceChangePlan, error) {
	if s == nil || s.Store == nil || s.Clock == nil || scope.validate() != nil || planID == "" {
		return SourceChangePlan{}, ErrNotFound
	}
	plan, err := s.Store.GetSourceChangePlan(ctx, scope.TenantID, scope.ProjectID, planID)
	if err != nil {
		return SourceChangePlan{}, err
	}
	if plan.ActorID != scope.ActorID {
		return SourceChangePlan{}, ErrPolicyDenied
	}
	if !plan.AuthorizedAt.IsZero() {
		return plan, nil
	}
	if !plan.ExpiresAt.After(s.Clock.Now().UTC()) {
		return SourceChangePlan{}, ErrPolicyDenied
	}
	if !plan.RequiresApproval {
		return plan, nil
	}
	if grantID == "" {
		return SourceChangePlan{}, ErrSourceApprovalRequired
	}
	grant, err := s.Store.GetActiveSourceApproval(ctx, scope.TenantID, scope.ProjectID, planID, grantID, scope.ActorID, s.Clock.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return SourceChangePlan{}, ErrPolicyDenied
		}
		return SourceChangePlan{}, err
	}
	if grant.PlanHash != plan.PlanHash || grant.WorkspaceID != plan.WorkspaceID || grant.RepositoryID != plan.RepositoryID || grant.TargetBranch != plan.TargetBranch {
		return SourceChangePlan{}, ErrPolicyDenied
	}
	return plan, nil
}

func (s *Service) AuthorizeSourceCommit(ctx context.Context, request AuthorizeSourceCommitRequest) (SourceChangePlan, error) {
	if s == nil || s.Store == nil || s.Clock == nil || request.Scope.validate() != nil || request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 {
		return SourceChangePlan{}, errors.New("invalid source commit authorization")
	}
	plan, err := s.CheckSourceApproval(ctx, request.Scope, request.PlanID, request.ApprovalGrantID)
	if err != nil {
		return SourceChangePlan{}, err
	}
	if plan.WorkspaceID != request.WorkspaceID || plan.RepositoryID != request.RepositoryID || plan.BaseSHA != request.BaseSHA || plan.TargetBranch != request.TargetBranch || plan.TaskID != request.TaskID || plan.PlanHash != request.PlanHash {
		return SourceChangePlan{}, ErrPolicyDenied
	}
	fingerprint, err := sourceChangeHash(struct {
		TenantID       string `json:"tenant_id"`
		ProjectID      string `json:"project_id"`
		ActorID        string `json:"actor_id"`
		PlanID         string `json:"plan_id"`
		PlanHash       string `json:"plan_hash"`
		GrantID        string `json:"grant_id,omitempty"`
		IdempotencyKey string `json:"idempotency_key"`
	}{request.Scope.TenantID, request.Scope.ProjectID, request.Scope.ActorID, plan.PlanID, plan.PlanHash, request.ApprovalGrantID, request.IdempotencyKey})
	if err != nil {
		return SourceChangePlan{}, err
	}
	return s.Store.AuthorizeSourceCommit(ctx, SourceCommitAuthorization{
		TenantID: request.Scope.TenantID, ProjectID: request.Scope.ProjectID, PlanID: plan.PlanID,
		PlanHash: plan.PlanHash, ActorID: request.Scope.ActorID, ApprovalGrantID: request.ApprovalGrantID,
		IdempotencyKey: request.IdempotencyKey, Fingerprint: fingerprint, Now: s.Clock.Now().UTC(), ApprovalRequired: plan.RequiresApproval,
	})
}

type SourceCommitAuthorization struct {
	TenantID, ProjectID, PlanID, PlanHash, ActorID string
	ApprovalGrantID, IdempotencyKey, Fingerprint   string
	Now                                            time.Time
	ApprovalRequired                               bool
}

func sourceChangeHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validSourceSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func protectedProductionPath(value string) bool {
	clean := strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	return clean == "deploy/environments/production" || strings.HasPrefix(clean, "deploy/environments/production/")
}
