package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
)

type parsedSchema struct {
	Schema               string                     `json:"$schema"`
	ID                   string                     `json:"$id"`
	Type                 string                     `json:"type"`
	AdditionalProperties bool                       `json:"additionalProperties"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func argumentTypes() map[agentv1.Tool]reflect.Type {
	return map[agentv1.Tool]reflect.Type{
		agentv1.ToolCreateProject:        reflect.TypeOf(CreateProjectArguments{}),
		agentv1.ToolGetProject:           reflect.TypeOf(GetProjectArguments{}),
		agentv1.ToolApplyRepositoryPatch: reflect.TypeOf(ApplyPatchArguments{}),
		agentv1.ToolCreateBranch:         reflect.TypeOf(CreateBranchArguments{}),
		agentv1.ToolCreateMergeRequest:   reflect.TypeOf(CreateMergeRequestArguments{}),
		agentv1.ToolRequestBuild:         reflect.TypeOf(RequestBuildArguments{}),
		agentv1.ToolGetBuild:             reflect.TypeOf(GetBuildArguments{}),
		agentv1.ToolDeploy:               reflect.TypeOf(DeployArguments{}),
		agentv1.ToolGetDeployment:        reflect.TypeOf(GetDeploymentArguments{}),
		agentv1.ToolRollback:             reflect.TypeOf(RollbackArguments{}),
		agentv1.ToolSetSecret:            reflect.TypeOf(SetSecretArguments{}),
		agentv1.ToolListSecretMetadata:   reflect.TypeOf(ListSecretMetadataArguments{}),
		agentv1.ToolProvisionService:     reflect.TypeOf(ProvisionServiceArguments{}),
		agentv1.ToolBindService:          reflect.TypeOf(BindServiceArguments{}),
		agentv1.ToolAddDomain:            reflect.TypeOf(AddDomainArguments{}),
		agentv1.ToolGetLogs:              reflect.TypeOf(GetLogsArguments{}),
		agentv1.ToolGetUsage:             reflect.TypeOf(GetUsageArguments{}),
		agentv1.ToolRequestApproval:      reflect.TypeOf(RequestApprovalArguments{}),
		agentv1.ToolGetOperation:         reflect.TypeOf(GetOperationArguments{}),
		agentv1.ToolCancelOperation:      reflect.TypeOf(CancelOperationArguments{}),
	}
}

func jsonFields(v reflect.Type) []string {
	out := make([]string, 0, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		tag := strings.Split(v.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func schemaFields(v parsedSchema) []string {
	out := make([]string, 0, len(v.Properties))
	for key := range v.Properties {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestMCPArgumentSchemasMatchStrictGoArguments(t *testing.T) {
	root := repositoryRoot(t)
	types := argumentTypes()
	if len(types) != len(agentv1.ToolCatalog()) {
		t.Fatalf("type map=%d catalog=%d", len(types), len(agentv1.ToolCatalog()))
	}
	for _, tool := range agentv1.ToolCatalog() {
		t.Run(string(tool), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, "contracts/mcp/v1", string(tool)+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var schema parsedSchema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.Schema != "https://json-schema.org/draft/2020-12/schema" || !strings.Contains(schema.ID, "/mcp/v1/") {
				t.Fatalf("unversioned schema: %+v", schema)
			}
			if schema.Type != "object" || schema.AdditionalProperties {
				t.Fatalf("schema must be a strict object")
			}
			got, want := schemaFields(schema), jsonFields(types[tool])
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("schema fields=%v Go fields=%v", got, want)
			}
		})
	}
}

func canonicalToolArguments() map[agentv1.Tool]json.RawMessage {
	return map[agentv1.Tool]json.RawMessage{
		agentv1.ToolCreateProject:        json.RawMessage(`{"name":"booking"}`),
		agentv1.ToolGetProject:           json.RawMessage(`{"project_id":"project-1"}`),
		agentv1.ToolApplyRepositoryPatch: json.RawMessage(`{"project_id":"project-1","base_commit_sha":"abcdef0123456789","branch":"feature/payments","message":"add payments","files":[{"path":"cmd/api/main.go","content":"package main"}]}`),
		agentv1.ToolCreateBranch:         json.RawMessage(`{"project_id":"project-1","branch":"feature/payments","base_commit_sha":"abcdef0123456789"}`),
		agentv1.ToolCreateMergeRequest:   json.RawMessage(`{"project_id":"project-1","source_branch":"feature/payments","target_branch":"main","title":"Add payments"}`),
		agentv1.ToolRequestBuild:         json.RawMessage(`{"revision":{"project_id":"project-1","repository_id":"repo-1","branch":"main","commit_sha":"abcdef0123456789","source_root":"cmd/api"},"estimated_minutes":5}`),
		agentv1.ToolGetBuild:             json.RawMessage(`{"build_id":"build-1"}`),
		agentv1.ToolDeploy:               json.RawMessage(`{"build_id":"build-1","application_id":"app-1","environment_id":"env-production","environment_name":"production","expected_environment_revision":1,"configuration":{"region":"eu1","isolation":"sandboxed","unit":"u1","processes":{"web":{"port":8080,"minReplicas":1,"maxReplicas":2,"healthPath":"/health"}},"generated_hostname":"booking.example.test","rollout_timeout_seconds":300,"egress_profile":"public-default"}}`),
		agentv1.ToolGetDeployment:        json.RawMessage(`{"deployment_id":"deployment-1"}`),
		agentv1.ToolRollback:             json.RawMessage(`{"deployment_id":"deployment-1","target_release_id":"release-1","expected_environment_revision":2}`),
		agentv1.ToolSetSecret:            json.RawMessage(`{"application_id":"app-1","environment_id":"env-production","name":"DATABASE_URL","value":"postgres://redacted"}`),
		agentv1.ToolListSecretMetadata:   json.RawMessage(`{"application_id":"app-1","environment_id":"env-production"}`),
		agentv1.ToolProvisionService:     json.RawMessage(`{"service_type":"postgres","plan":"small","name":"primary"}`),
		agentv1.ToolBindService:          json.RawMessage(`{"service_instance_id":"service-1","application_id":"app-1","environment_id":"env-production"}`),
		agentv1.ToolAddDomain:            json.RawMessage(`{"application_id":"app-1","environment_id":"env-production","hostname":"booking.example.com"}`),
		agentv1.ToolGetLogs:              json.RawMessage(`{"application_id":"app-1","limit":100}`),
		agentv1.ToolGetUsage:             json.RawMessage(`{"period_id":"period-1"}`),
		agentv1.ToolRequestApproval:      json.RawMessage(`{"action":"deployment.production","resource":{"type":"environment","id":"env-production"},"payload":{"release":"release-1"},"ttl_seconds":600}`),
		agentv1.ToolGetOperation:         json.RawMessage(`{"operation_id":"operation-1"}`),
		agentv1.ToolCancelOperation:      json.RawMessage(`{"operation_id":"operation-1"}`),
	}
}

func TestMCPAllCanonicalArgumentsPassServerValidator(t *testing.T) {
	fixtures := canonicalToolArguments()
	for _, tool := range agentv1.ToolCatalog() {
		raw, ok := fixtures[tool]
		if !ok {
			t.Fatalf("missing fixture for %s", tool)
		}
		if err := validateToolArguments(tool, raw); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
	}
}

func TestMCPSchemaCatalogHasNoMissingOrExtraTools(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "contracts/mcp/v1/catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		APIVersion       string `json:"api_version"`
		SemanticsVersion string `json:"semantics_version"`
		Tools            []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.APIVersion != agentv1.APIVersion || catalog.SemanticsVersion != agentv1.SemanticsVersion {
		t.Fatal("catalog version drift")
	}
	got := make([]string, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		got = append(got, tool.Name)
	}
	want := make([]string, 0, len(agentv1.ToolCatalog()))
	for _, tool := range agentv1.ToolCatalog() {
		want = append(want, string(tool))
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog=%v contract=%v", got, want)
	}
}

func TestAgentOpenAPIIsValidVersionedJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contracts/openapi/agent-v1.openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi=%v", doc["openapi"])
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || paths["/mcp/v1/invoke"] == nil || paths["/v1/approval-requests/{request_id}/grant"] == nil {
		t.Fatal("required paths missing")
	}
}

func TestGitBranchValidationAllowsFeaturePathAndRejectsRevisionExpressions(t *testing.T) {
	for _, value := range []string{"feature/payments", "release/v1.2.3", "fix-123"} {
		if !validGitBranch(value) {
			t.Fatalf("valid branch rejected: %q", value)
		}
	}
	for _, value := range []string{"HEAD", "main~1", "main@{1}", "../main", "main.lock", "a//b"} {
		if validGitBranch(value) {
			t.Fatalf("unsafe branch accepted: %q", value)
		}
	}
}

func TestCommitValidationRejectsNonHexRevision(t *testing.T) {
	if validCommitSHA("not-a-sha") || !validCommitSHA("abcdef0") {
		t.Fatal("commit validator mismatch")
	}
}
