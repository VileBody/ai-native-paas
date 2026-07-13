package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type Service struct {
	Store    Store
	Provider GitProvider
	Clock    Clock
	IDs      IDGenerator
}

type CreateProjectCommand struct {
	TenantID, ActorID, Name, IdempotencyKey string
	ProviderNamespaceID                     int64
}
type CreateProjectResult struct {
	Project    domain.Project
	Repository domain.Repository
}

func (s *Service) CreateProject(ctx context.Context, cmd CreateProjectCommand) (CreateProjectResult, error) {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return CreateProjectResult{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if cmd.TenantID == "" || cmd.ActorID == "" || cmd.Name == "" || cmd.IdempotencyKey == "" || cmd.ProviderNamespaceID <= 0 {
		return CreateProjectResult{}, domain.NewError(domain.CodeInvalidArgument, "missing create-project fields")
	}
	requestHash := hashJSON(struct {
		Name      string
		Namespace int64
	}{cmd.Name, cmd.ProviderNamespaceID})
	var result CreateProjectResult
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if rec, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if rec.Command != "source.create_project.v1" || rec.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if rec.Completed {
				return json.Unmarshal(rec.Result, &result)
			}
			return domain.NewError(domain.CodeConflict, "request with this idempotency key is in progress")
		}
		now := s.Clock.Now()
		p, err := domain.NewProject(s.IDs.NewID("prj"), cmd.TenantID, cmd.Name, now)
		if err != nil {
			return err
		}
		if _, ok := tx.FindProjectBySlug(cmd.TenantID, p.Slug); ok {
			return domain.NewError(domain.CodeConflict, "project slug already exists")
		}
		r, err := domain.NewRepository(s.IDs.NewID("repo"), cmd.TenantID, p.ID, "gitlab", s.IDs.NewID("corr"), cmd.ProviderNamespaceID, now)
		if err != nil {
			return err
		}
		if err = tx.InsertProject(p); err != nil {
			return err
		}
		if err = tx.InsertRepository(r); err != nil {
			return err
		}
		result = CreateProjectResult{Project: p, Repository: r}
		raw, _ := json.Marshal(result)
		if err = tx.PutIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: "source.create_project.v1", RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: now, CompletedAt: now}); err != nil {
			return err
		}
		evt, _ := json.Marshal(map[string]any{"project_id": p.ID, "repository_id": r.ID, "tenant_id": p.TenantID})
		if err = tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.project_created.v1", AggregateID: p.ID, Payload: evt, CreatedAt: now}); err != nil {
			return err
		}
		audit, _ := json.Marshal(map[string]any{"name": p.Name, "slug": p.Slug})
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.project.create", ResourceType: "project", ResourceID: p.ID, Data: audit, CreatedAt: now})
	})
	return result, err
}

type ProvisionRepositoryCommand struct{ TenantID, ActorID, RepositoryID string }

func (s *Service) ProvisionRepository(ctx context.Context, cmd ProvisionRepositoryCommand) (domain.Repository, error) {
	if s.Provider == nil {
		return domain.Repository{}, domain.NewError(domain.CodeUnavailable, "git provider is not configured")
	}
	var repo domain.Repository
	var project domain.Project
	err := s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || r.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		p, ok := tx.GetProject(r.ProjectID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "project not found")
		}
		expected := r.Version
		if err := r.BeginProvisioning(s.Clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateRepository(r, expected); err != nil {
			return err
		}
		repo, project = r, p
		return nil
	})
	if err != nil {
		return domain.Repository{}, err
	}
	if repo.State == domain.RepositoryReady {
		return repo, nil
	}
	req := CreateRepositoryRequest{NamespaceID: repo.ProviderNamespaceID, Name: project.Name, Path: project.Slug, DefaultBranch: "main", CorrelationID: repo.CorrelationID}
	remote, createErr := s.Provider.CreateRepository(ctx, req)
	if createErr != nil {
		found, ok, findErr := s.Provider.FindRepositoryByCorrelation(ctx, repo.ProviderNamespaceID, repo.CorrelationID)
		if findErr == nil && ok {
			remote = found
			createErr = nil
		}
	}
	if createErr != nil {
		_ = s.Store.Transact(ctx, func(tx Tx) error {
			current, ok := tx.GetRepository(repo.ID)
			if !ok {
				return nil
			}
			expected := current.Version
			current.FailProvisioning(createErr.Error(), s.Clock.Now())
			return tx.UpdateRepository(current, expected)
		})
		return domain.Repository{}, domain.Wrap(domain.CodeExternal, "git repository provisioning failed", createErr)
	}
	if err := s.Provider.ProtectBranch(ctx, remote.ID, coalesce(remote.DefaultBranch, "main")); err != nil {
		return domain.Repository{}, domain.Wrap(domain.CodeExternal, "default branch protection failed", err)
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repo.ID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "repository disappeared")
		}
		expected := current.Version
		if err := current.AttachProvider(remote.ID, remote.PathWithNamespace, remote.WebURL, coalesce(remote.DefaultBranch, "main"), s.Clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateRepository(current, expected); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"repository_id": current.ID, "provider_project_id": current.ProviderProjectID})
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.repository_ready.v1", AggregateID: current.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.repository.provision", ResourceType: "repository", ResourceID: current.ID, Data: []byte(`{"provider":"gitlab"}`), CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		repo = current
		return nil
	})
	return repo, err
}

func (s *Service) RenameProject(ctx context.Context, tenantID, actorID, projectID, name string) (domain.Project, error) {
	var out domain.Project
	err := s.Store.Transact(ctx, func(tx Tx) error {
		p, ok := tx.GetProject(projectID)
		if !ok || p.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "project not found")
		}
		old := p.Version
		if err := p.Rename(name, s.Clock.Now()); err != nil {
			return err
		}
		if existing, ok := tx.FindProjectBySlug(tenantID, p.Slug); ok && existing.ID != p.ID {
			return domain.NewError(domain.CodeConflict, "project slug already exists")
		}
		if err := tx.UpdateProject(p, old); err != nil {
			return err
		}
		out = p
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: tenantID, ActorID: actorID, Action: "source.project.rename", ResourceType: "project", ResourceID: p.ID, Data: []byte(`{}`), CreatedAt: s.Clock.Now()})
	})
	return out, err
}

func (s *Service) GetProject(ctx context.Context, tenantID, projectID string) (domain.Project, error) {
	var out domain.Project
	err := s.Store.Transact(ctx, func(tx Tx) error {
		p, ok := tx.GetProject(projectID)
		if !ok || p.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "project not found")
		}
		out = p
		return nil
	})
	return out, err
}
func (s *Service) GetRepository(ctx context.Context, tenantID, repositoryID string) (domain.Repository, error) {
	var out domain.Repository
	err := s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.GetRepository(repositoryID)
		if !ok || r.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		out = r
		return nil
	})
	return out, err
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func coalesce(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
func IsLostResponse(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(fmt.Sprint(err)), "lost response")
}
