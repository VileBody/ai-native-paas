package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

type mcpCatalog struct {
	APIVersion       string `json:"api_version"`
	SemanticsVersion string `json:"semantics_version"`
	Tools            []struct {
		Name             agentv2.Tool      `json:"name"`
		Group            agentv2.ToolGroup `json:"group"`
		ReadOnly         bool              `json:"read_only"`
		RequiredScope    string            `json:"required_scope"`
		InputSchema      string            `json:"inputSchema"`
		SemanticsVersion string            `json:"semanticsVersion"`
	} `json:"tools"`
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func TestMCPV2_CatalogAndSchemasAreVersionedStableAndClosed(t *testing.T) {
	root := repositoryRoot(t)
	var catalog mcpCatalog
	readJSON(t, filepath.Join(root, "contracts", "mcp", "v2", "catalog.json"), &catalog)
	if catalog.APIVersion != agentv2.APIVersion || catalog.SemanticsVersion != agentv2.SemanticsVersion {
		t.Fatalf("unexpected catalog versions: %#v", catalog)
	}
	definitions := agentv2.ToolCatalog()
	if len(catalog.Tools) != len(definitions) || len(definitions) != 53 {
		t.Fatalf("catalog size mismatch: JSON=%d Go=%d want=53", len(catalog.Tools), len(definitions))
	}
	for index, definition := range definitions {
		entry := catalog.Tools[index]
		if entry.Name != definition.Name || entry.Group != definition.Group || entry.ReadOnly != definition.ReadOnly || entry.RequiredScope != definition.RequiredScope || entry.SemanticsVersion != agentv2.SemanticsVersion {
			t.Fatalf("catalog mismatch at %d: JSON=%#v Go=%#v", index, entry, definition)
		}
		var schema map[string]any
		readJSON(t, filepath.Join(root, "contracts", "mcp", "v2", string(entry.Name)+".schema.json"), &schema)
		if schema["additionalProperties"] != false || schema["type"] != "object" {
			t.Fatalf("tool %s input is not a closed object", entry.Name)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %s has no properties object", entry.Name)
		}
		if _, exists := properties["tenant_id"]; exists {
			t.Fatalf("tool %s accepts client-controlled tenant scope", entry.Name)
		}
		if _, exists := properties["project_id"]; exists {
			t.Fatalf("tool %s accepts client-controlled project scope", entry.Name)
		}
	}
	toolNames := make([]string, len(catalog.Tools))
	for index, tool := range catalog.Tools {
		toolNames[index] = string(tool.Name)
	}
	if !sort.StringsAreSorted(toolNames) {
		t.Fatal("MCP v2 catalog is not deterministically sorted")
	}
	var invocation map[string]any
	readJSON(t, filepath.Join(root, "contracts", "mcp", "v2", "invocation-request.schema.json"), &invocation)
	properties := invocation["properties"].(map[string]any)
	for _, forbidden := range []string{"tenant_id", "project_id", "user_id", "agent_id"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("invocation body accepts verified identity field %s", forbidden)
		}
	}
}

func TestMCPV1CompatibilityCannotBypassV2Governance(t *testing.T) {
	routes := agentv2.CompatibilityCatalog()
	if len(routes) != len(agentv1.ToolCatalog()) {
		t.Fatalf("compatibility routes=%d, v1 tools=%d", len(routes), len(agentv1.ToolCatalog()))
	}
	seen := make(map[agentv1.Tool]struct{}, len(routes))
	for _, route := range routes {
		if !agentv1.ValidTool(route.V1Tool) || !route.GovernanceRequired || route.Deprecation == "" || len(route.V2Targets) == 0 {
			t.Fatalf("unsafe compatibility route: %#v", route)
		}
		if _, exists := seen[route.V1Tool]; exists {
			t.Fatalf("duplicate compatibility route for %s", route.V1Tool)
		}
		seen[route.V1Tool] = struct{}{}
		for _, target := range route.V2Targets {
			if !agentv2.ValidTool(target) {
				t.Fatalf("v1 tool %s routes to unknown v2 tool %s", route.V1Tool, target)
			}
		}
	}
	for _, v1Tool := range agentv1.ToolCatalog() {
		if _, ok := agentv2.CompatibilityFor(v1Tool); !ok {
			t.Fatalf("v1 tool %s has no governed compatibility route", v1Tool)
		}
	}
	var generated struct {
		Routes []struct {
			V1Tool             agentv1.Tool   `json:"v1_tool"`
			V2Targets          []agentv2.Tool `json:"v2_targets"`
			GovernanceRequired bool           `json:"governance_required"`
		} `json:"routes"`
	}
	readJSON(t, filepath.Join(repositoryRoot(t), "contracts", "mcp", "v2", "compatibility-v1.json"), &generated)
	if len(generated.Routes) != len(routes) {
		t.Fatalf("generated compatibility routes=%d, Go routes=%d", len(generated.Routes), len(routes))
	}
	for index := range routes {
		if generated.Routes[index].V1Tool != routes[index].V1Tool || !generated.Routes[index].GovernanceRequired {
			t.Fatalf("generated route mismatch at %d", index)
		}
	}
}
