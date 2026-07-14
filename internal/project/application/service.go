// Package application orchestrates project creation across source, GitLab bootstrap, and agent enrollment.
package application

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	projectbootstrap "github.com/keir-research/ai-native-paas/internal/project/bootstrap"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

type Source interface {
	CreateProject(context.Context, sourceapp.CreateProjectCommand) (sourceapp.CreateProjectResult, error)
	ProvisionRepository(context.Context, sourceapp.ProvisionRepositoryCommand) (domain.Repository, error)
	RecordBootstrapRevision(context.Context, sourceapp.RecordBootstrapRevisionCommand) (domain.Repository, error)
}

type EnrollmentIssuer interface {
	Issue(context.Context, enrollment.Binding) (enrollment.EnrollmentToken, error)
}

type Service struct {
	Source               Source
	Bootstrapper         sourceapp.RepositoryBootstrapper
	Enrollment           EnrollmentIssuer
	GitLabNamespaceID    int64
	MCPBaseURL           string
	WorkspaceImageDigest string
}

type CreateCommand struct {
	TenantID       string
	UserID         string
	Name           string
	IdempotencyKey string
}

func (s *Service) Create(ctx context.Context, command CreateCommand) (projectv2.CreateProjectResponse, error) {
	if err := s.validateConfig(); err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	request := projectv2.CreateProjectRequest{Name: command.Name}
	if err := request.Validate(); err != nil || strings.TrimSpace(command.TenantID) == "" || strings.TrimSpace(command.UserID) == "" || strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 128 {
		return projectv2.CreateProjectResponse{}, errors.New("project creation request is invalid")
	}
	created, err := s.Source.CreateProject(ctx, sourceapp.CreateProjectCommand{
		TenantID: command.TenantID, ActorID: command.UserID, Name: command.Name,
		IdempotencyKey: command.IdempotencyKey, ProviderNamespaceID: s.GitLabNamespaceID,
	})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	repository, err := s.Source.ProvisionRepository(ctx, sourceapp.ProvisionRepositoryCommand{
		TenantID: command.TenantID, ActorID: command.UserID, RepositoryID: created.Repository.ID,
	})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	if repository.ProviderProjectID <= 0 || repository.WebURL == "" || repository.DefaultBranch == "" {
		return projectv2.CreateProjectResponse{}, errors.New("provisioned repository is incomplete")
	}
	files, err := projectbootstrap.Files(projectbootstrap.Options{Name: created.Project.Slug, WorkspaceImageDigest: s.WorkspaceImageDigest})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	baseSHA, err := s.repositoryHead(ctx, repository)
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	bootstrapRevision, err := s.Bootstrapper.BootstrapRepository(ctx, sourceapp.BootstrapRepositoryRequest{
		ProviderProjectID: repository.ProviderProjectID, Branch: repository.DefaultBranch,
		ExpectedBaseSHA: baseSHA, Files: files,
		CommitMessage: "Initialize AI-native platform project\n\nPaaS-Correlation: " + repository.CorrelationID,
	})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	repository, err = s.Source.RecordBootstrapRevision(ctx, sourceapp.RecordBootstrapRevisionCommand{
		TenantID: command.TenantID, ActorID: command.UserID, RepositoryID: repository.ID, Revision: bootstrapRevision,
	})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	if repository.BootstrapRevision != strings.ToLower(strings.TrimSpace(bootstrapRevision)) {
		return projectv2.CreateProjectResponse{}, errors.New("bootstrap revision was not persisted")
	}
	agentID := "agent-" + created.Project.ID
	scopes := make([]string, 0, len(agentv2.ToolCatalog()))
	for _, definition := range agentv2.ToolCatalog() {
		scopes = append(scopes, definition.RequiredScope)
	}
	token, err := s.Enrollment.Issue(ctx, enrollment.Binding{
		TenantID: command.TenantID, ProjectID: created.Project.ID, UserID: command.UserID,
		AgentID: agentID, Scopes: scopes,
	})
	if err != nil {
		return projectv2.CreateProjectResponse{}, err
	}
	mcpURL := strings.TrimRight(s.MCPBaseURL, "/") + "/projects/" + url.PathEscape(created.Project.ID) + "/mcp/v2"
	return projectv2.CreateProjectResponse{
		ProjectID: created.Project.ID, GitURL: strings.TrimRight(repository.WebURL, "/") + ".git",
		MCPURL: mcpURL, AgentID: agentID, AgentEnrollmentToken: token.Token,
		EnrollmentExpiresIn: int64(enrollment.EnrollmentTTL.Seconds()),
	}, nil
}

type branchReader interface {
	GetBranchHead(context.Context, int64, string) (string, error)
}

func (s *Service) repositoryHead(ctx context.Context, repository domain.Repository) (string, error) {
	reader, ok := s.Bootstrapper.(branchReader)
	if !ok {
		return "", errors.New("repository bootstrapper cannot resolve an exact base SHA")
	}
	return reader.GetBranchHead(ctx, repository.ProviderProjectID, repository.DefaultBranch)
}

func (s *Service) validateConfig() error {
	if s == nil || s.Source == nil || s.Bootstrapper == nil || s.Enrollment == nil || s.GitLabNamespaceID <= 0 {
		return errors.New("project service dependencies are unavailable")
	}
	parsed, err := url.Parse(s.MCPBaseURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("project MCP base URL is invalid")
	}
	if _, err := projectbootstrap.Files(projectbootstrap.Options{Name: "config-check", WorkspaceImageDigest: s.WorkspaceImageDigest}); err != nil {
		return errors.New("project workspace image digest is invalid: " + strconv.Quote(s.WorkspaceImageDigest))
	}
	return nil
}
