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
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	inframemory "github.com/keir-research/ai-native-paas/internal/infrastructure/memory"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
)

type httpInfraClock struct{ now time.Time }

func (c httpInfraClock) Now() time.Time { return c.now }

type httpInfraIDs struct{ n int }

func (i *httpInfraIDs) New(prefix string) string {
	i.n++
	return prefix + "-http-" + string(rune('0'+i.n))
}

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

func TestProjectHTTP_HumanApprovalIsBoundToStoredAgentAndTenantPlan(t *testing.T) {
	now := time.Now().UTC()
	infrastructure := &infraapp.Service{
		Store: inframemory.New(), Clock: httpInfraClock{now: now}, IDs: &httpInfraIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-http", Version: "1", MarkupBasisPoints: 2500, Currency: "RUB", PriceSnapshotID: "prices-http"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	plan, err := infrastructure.Plan(context.Background(), infraapp.PlanCommand{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1", WorkspaceID: "workspace-1",
		Target: "production", SourceSHA: strings.Repeat("a", 40), IdempotencyKey: "plan-http",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), StateGeneration: 1,
		PlanJSON: []byte(`{"resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := (httpauth.Middleware{Profile: platformprofile.Production, OIDC: oidcVerifier{}}).Wrap(Handler{Infrastructure: infrastructure})

	grantRequest := httptest.NewRequest(http.MethodPost, "/api/v2/projects/project-1/infrastructure/plans/"+plan.Summary.PlanID+"/approval", strings.NewReader(`{}`))
	grantRequest.Header.Set("Authorization", "Bearer signed")
	grantResponse := httptest.NewRecorder()
	handler.ServeHTTP(grantResponse, grantRequest)
	if grantResponse.Code != http.StatusCreated || !strings.Contains(grantResponse.Body.String(), `"actor_id":"agent-1"`) || !strings.Contains(grantResponse.Body.String(), `"approver_user_id":"user-1"`) {
		t.Fatalf("grant status=%d body=%s", grantResponse.Code, grantResponse.Body.String())
	}
	status, err := infrastructure.GetApprovalStatus(context.Background(), "tenant-1", "project-1", plan.Summary.PlanID, "agent-1")
	if err != nil || status.Status != "APPROVED" || status.GrantID == "" {
		t.Fatalf("approval status=%#v err=%v", status, err)
	}

	crossProject := httptest.NewRequest(http.MethodGet, "/api/v2/projects/project-2/infrastructure/plans/"+plan.Summary.PlanID, nil)
	crossProject.Header.Set("Authorization", "Bearer signed")
	crossResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossResponse, crossProject)
	if crossResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-project status=%d body=%s", crossResponse.Code, crossResponse.Body.String())
	}
}
