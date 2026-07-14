package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type oidcVerifier struct{}

func (oidcVerifier) VerifyOIDC(context.Context, string) (httpauth.Identity, error) {
	return httpauth.Identity{SubjectID: "user-1", TenantID: "tenant-1", UserID: "user-1", Scopes: []string{"project:create"}}, nil
}

type httpSource struct {
	project    domain.Project
	repository domain.Repository
}

func (s httpSource) CreateProject(context.Context, sourceapp.CreateProjectCommand) (sourceapp.CreateProjectResult, error) {
	return sourceapp.CreateProjectResult{Project: s.project, Repository: s.repository}, nil
}
func (s httpSource) ProvisionRepository(context.Context, sourceapp.ProvisionRepositoryCommand) (domain.Repository, error) {
	return s.repository, nil
}

type httpBootstrapper struct{}

func (httpBootstrapper) GetBranchHead(context.Context, int64, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (httpBootstrapper) BootstrapRepository(context.Context, sourceapp.BootstrapRepositoryRequest) (string, error) {
	return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
}

type httpEnrollment struct{}

func (httpEnrollment) Issue(context.Context, enrollment.Binding) (enrollment.EnrollmentToken, error) {
	return enrollment.EnrollmentToken{Token: "one-time", ExpiresAt: time.Now().Add(enrollment.EnrollmentTTL)}, nil
}

func httpProjectService(t *testing.T) *projectapp.Service {
	t.Helper()
	project, err := domain.NewProject("project-1", "tenant-1", "booking", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := domain.NewRepository("repo-1", "tenant-1", project.ID, "gitlab", "corr-1", 77, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginProvisioning(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repository.AttachProvider(42, "beta/booking", "https://gitlab.com/beta/booking", "main", time.Now()); err != nil {
		t.Fatal(err)
	}
	return &projectapp.Service{
		Source: httpSource{project: project, repository: repository}, Bootstrapper: httpBootstrapper{}, Enrollment: httpEnrollment{},
		GitLabNamespaceID: 77, MCPBaseURL: "https://mcp.example.com", WorkspaceImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestProjectHTTP_RequiresVerifiedIdentityAndStrictRequest(t *testing.T) {
	service := httpProjectService(t)
	handler := (httpauth.Middleware{Profile: platformprofile.Production, OIDC: oidcVerifier{}, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(Handler{Projects: service})

	unauthenticated := httptest.NewRequest(http.MethodPost, "/api/v2/projects", strings.NewReader(`{"name":"booking"}`))
	unauthenticated.Header.Set("Idempotency-Key", "create-1")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticatedResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v2/projects", strings.NewReader(`{"name":"booking","tenant_id":"victim"}`))
	request.Header.Set("Authorization", "Bearer signed")
	request.Header.Set("Idempotency-Key", "create-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte("victim")) {
		t.Fatalf("strict status=%d body=%s", response.Code, response.Body.String())
	}
}
