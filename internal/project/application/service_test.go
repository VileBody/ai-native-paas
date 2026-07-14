package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type sourceStub struct {
	create sourceapp.CreateProjectResult
	repo   domain.Repository
	cmd    sourceapp.CreateProjectCommand
	record sourceapp.RecordBootstrapRevisionCommand
}

func (s *sourceStub) RecordBootstrapRevision(_ context.Context, command sourceapp.RecordBootstrapRevisionCommand) (domain.Repository, error) {
	s.record = command
	_, err := s.repo.RecordBootstrapRevision(command.Revision, time.Now())
	return s.repo, err
}

func (s *sourceStub) CreateProject(_ context.Context, command sourceapp.CreateProjectCommand) (sourceapp.CreateProjectResult, error) {
	s.cmd = command
	return s.create, nil
}
func (s *sourceStub) ProvisionRepository(_ context.Context, command sourceapp.ProvisionRepositoryCommand) (domain.Repository, error) {
	if command.RepositoryID != s.create.Repository.ID {
		return domain.Repository{}, errors.New("wrong repository")
	}
	return s.repo, nil
}

type bootstrapperStub struct {
	request sourceapp.BootstrapRepositoryRequest
}

func (b *bootstrapperStub) GetBranchHead(context.Context, int64, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (b *bootstrapperStub) BootstrapRepository(_ context.Context, request sourceapp.BootstrapRepositoryRequest) (string, error) {
	b.request = request
	return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
}

type enrollmentStub struct{ binding enrollment.Binding }

func (e *enrollmentStub) Issue(_ context.Context, binding enrollment.Binding) (enrollment.EnrollmentToken, error) {
	e.binding = binding
	return enrollment.EnrollmentToken{EnrollmentID: "enrollment-1", Token: "one-time-token", ExpiresAt: time.Now().Add(enrollment.EnrollmentTTL)}, nil
}

func projectServiceFixture() (*Service, *sourceStub, *bootstrapperStub, *enrollmentStub) {
	project, _ := domain.NewProject("prj_123", "tenant-1", "booking", time.Now())
	repository, _ := domain.NewRepository("repo_123", "tenant-1", project.ID, "gitlab", "corr_123", 77, time.Now())
	ready := repository
	_ = ready.BeginProvisioning(time.Now())
	_ = ready.AttachProvider(42, "beta/booking", "https://gitlab.com/beta/booking", "main", time.Now())
	source := &sourceStub{create: sourceapp.CreateProjectResult{Project: project, Repository: repository}, repo: ready}
	bootstrapper := &bootstrapperStub{}
	enrollmentIssuer := &enrollmentStub{}
	service := &Service{
		Source: source, Bootstrapper: bootstrapper, Enrollment: enrollmentIssuer, GitLabNamespaceID: 77,
		MCPBaseURL: "https://mcp.example.com", WorkspaceImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	return service, source, bootstrapper, enrollmentIssuer
}

func TestCreateProject_ProvisionsBootstrapsAndIssuesBoundEnrollment(t *testing.T) {
	service, source, bootstrapper, enrollmentIssuer := projectServiceFixture()
	response, err := service.Create(context.Background(), CreateCommand{TenantID: "tenant-1", UserID: "user-1", Name: "booking", IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ProjectID != "prj_123" || response.GitURL != "https://gitlab.com/beta/booking.git" || response.AgentID != "agent-prj_123" || response.AgentEnrollmentToken != "one-time-token" || response.EnrollmentExpiresIn != 600 {
		t.Fatalf("response=%#v", response)
	}
	if source.cmd.ProviderNamespaceID != 77 || source.cmd.TenantID != "tenant-1" || source.cmd.ActorID != "user-1" {
		t.Fatalf("source command=%#v", source.cmd)
	}
	if bootstrapper.request.ExpectedBaseSHA == "" || len(bootstrapper.request.Files) < 8 || !strings.Contains(bootstrapper.request.CommitMessage, "corr_123") {
		t.Fatalf("bootstrap request=%#v", bootstrapper.request)
	}
	if source.record.RepositoryID != "repo_123" || source.record.Revision != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || source.repo.BootstrapRevision != source.record.Revision {
		t.Fatalf("bootstrap revision command=%#v repository=%#v", source.record, source.repo)
	}
	if enrollmentIssuer.binding.ProjectID != "prj_123" || enrollmentIssuer.binding.UserID != "user-1" || enrollmentIssuer.binding.AgentID != response.AgentID || len(enrollmentIssuer.binding.Scopes) < 40 {
		t.Fatalf("binding=%#v", enrollmentIssuer.binding)
	}
}

func TestCreateProject_RejectsUnpinnedWorkspaceImageBeforeSideEffects(t *testing.T) {
	service, source, _, _ := projectServiceFixture()
	service.WorkspaceImageDigest = "latest"
	_, err := service.Create(context.Background(), CreateCommand{TenantID: "tenant-1", UserID: "user-1", Name: "booking", IdempotencyKey: "create-1"})
	if err == nil || source.cmd.TenantID != "" {
		t.Fatalf("err=%v source command=%#v", err, source.cmd)
	}
}
