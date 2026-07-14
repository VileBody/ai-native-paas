package recipe_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/attachments/recipe"
	recipesv1 "github.com/keir-research/ai-native-paas/pkg/contracts/recipes/v1"
)

type memoryRegistry struct {
	values map[string]recipesv1.RecipeVersion
}

func newMemoryRegistry() *memoryRegistry {
	return &memoryRegistry{values: map[string]recipesv1.RecipeVersion{}}
}
func recipeKey(id, version string) string { return id + "\x00" + version }
func (m *memoryRegistry) PutActiveRecipe(_ context.Context, version recipesv1.RecipeVersion) (recipesv1.RecipeVersion, error) {
	key := recipeKey(version.RecipeID, version.Version)
	if existing, ok := m.values[key]; ok {
		if existing.ContentDigest != version.ContentDigest {
			return recipesv1.RecipeVersion{}, recipe.ErrConflict
		}
		return existing, nil
	}
	m.values[key] = version
	return version, nil
}
func (m *memoryRegistry) GetActiveRecipe(_ context.Context, id, version string) (recipesv1.RecipeVersion, error) {
	value, ok := m.values[recipeKey(id, version)]
	if !ok {
		return recipesv1.RecipeVersion{}, recipe.ErrNotFound
	}
	return value, nil
}
func (m *memoryRegistry) ListActiveRecipes(_ context.Context, id string) ([]recipesv1.RecipeVersion, error) {
	values := []recipesv1.RecipeVersion{}
	for _, value := range m.values {
		if value.RecipeID == id {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Version < values[j].Version })
	return values, nil
}

func recipeKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(strings.NewReader(strings.Repeat("k", ed25519.SeedSize)))
}

func unsignedRecipe(version string) recipesv1.RecipeVersion {
	return recipesv1.RecipeVersion{
		RecipeID: "temporal", Version: version, Kind: "stateful-service", Driver: "helm",
		SchemaDigest: "sha256:" + strings.Repeat("a", 64), Stateful: true,
		Capabilities: recipesv1.LifecycleCapabilities{Plan: true, Apply: true, Discover: true, Update: true, Retain: true, Purge: true, Backup: true, Restore: true},
		Artifacts: []recipesv1.ArtifactPin{
			{Name: "chart", Kind: "helm", Reference: "oci://registry.example/temporal", Digest: "sha256:" + strings.Repeat("b", 64)},
			{Name: "image", Kind: "oci-image", Reference: "registry.example/temporal", Digest: "sha256:" + strings.Repeat("c", 64)},
		},
		Permissions: []recipesv1.ResourcePermission{
			{APIGroup: "apps", Kind: "StatefulSet", Scope: recipesv1.PermissionNamespaced},
			{APIGroup: "temporal.io", Kind: "TemporalCluster", Scope: recipesv1.PermissionNamespaced},
		},
		Procedures: recipesv1.LifecycleProcedures{HealthCheck: "workflow-probe-v1", Backup: "backup-v1", Restore: "restore-v1", Upgrade: "upgrade-v1", Removal: "remove-v1", Retention: "retain-until-approved"},
	}
}

func signedRecipe(t *testing.T, version string) (recipesv1.RecipeVersion, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := recipeKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := recipe.Sign(unsignedRecipe(version), "beta-recipe-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signed, publicKey
}

func TestRecipe_ResolutionPinsExactVersionAndArtifactDigests(t *testing.T) {
	store := newMemoryRegistry()
	first, publicKey := signedRecipe(t, "1.0.0")
	second, _ := signedRecipe(t, "1.1.0")
	registry := recipe.Registry{Store: store, Keys: map[string]ed25519.PublicKey{"beta-recipe-key": publicKey}}
	if _, err := registry.Activate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// Both fixtures use the same deterministic key seed.
	if _, err := registry.Activate(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	lock, err := registry.Resolve(context.Background(), recipe.ResolveRequest{RecipeID: "temporal", AllowedVersions: []string{"1.0.0", "1.1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != "1.1.0" || len(lock.Artifacts) != 2 {
		t.Fatalf("lock=%+v", lock)
	}
	for _, artifact := range lock.Artifacts {
		if !strings.HasPrefix(artifact.Digest, "sha256:") || strings.Contains(strings.ToLower(artifact.Reference), ":latest") {
			t.Fatalf("floating artifact in lock: %+v", artifact)
		}
	}
}

func TestRecipe_DeclaredPermissionsMatchRenderedResources(t *testing.T) {
	version, publicKey := signedRecipe(t, "1.0.0")
	store := newMemoryRegistry()
	registry := recipe.Registry{Store: store, Keys: map[string]ed25519.PublicKey{"beta-recipe-key": publicKey}}
	if _, err := registry.Activate(context.Background(), version); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateRenderedResources(context.Background(), version.RecipeID, version.Version, []recipe.ManifestResource{{APIGroup: "apps", Kind: "StatefulSet", Namespace: "project-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateRenderedResources(context.Background(), version.RecipeID, version.Version, []recipe.ManifestResource{{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", ClusterScope: true}}); !errors.Is(err, recipe.ErrPolicy) {
		t.Fatalf("undeclared ClusterRole accepted: %v", err)
	}
}

func TestRecipe_HealthBackupUpgradeAndRemovalProceduresAreComplete(t *testing.T) {
	_, privateKey, err := recipeKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	incomplete := unsignedRecipe("1.0.0")
	incomplete.Procedures.Backup = ""
	if _, err := recipe.Sign(incomplete, "beta-recipe-key", privateKey); err == nil {
		t.Fatal("stateful recipe without backup procedure was signed")
	}
	if _, err := recipe.Sign(unsignedRecipe("1.0.1"), "beta-recipe-key", privateKey); err != nil {
		t.Fatalf("complete stateful recipe rejected: %v", err)
	}
}

func TestRecipe_CustomAdHocInstallIsAllowedWithinStricterPolicy(t *testing.T) {
	decision, err := recipe.EvaluateCustomInstall([]recipe.ManifestResource{
		{APIGroup: "apps", Kind: "Deployment", Namespace: "project-1"},
		{APIGroup: "", Kind: "PersistentVolumeClaim", Namespace: "project-1"},
	})
	if err != nil || !decision.Allowed || decision.Trusted || !decision.RequiresApproval || decision.PolicyProfile != "custom-strict-v1" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if _, err := recipe.EvaluateCustomInstall([]recipe.ManifestResource{{APIGroup: "example.io", Kind: "Product", Namespace: "project-1"}}); !errors.Is(err, recipe.ErrPolicy) {
		t.Fatalf("custom operator CR accepted: %v", err)
	}
}

func TestRuntime_ProductOperatorCRAllowedOnlyByRecipePolicy(t *testing.T) {
	version, publicKey := signedRecipe(t, "1.0.0")
	store := newMemoryRegistry()
	registry := recipe.Registry{Store: store, Keys: map[string]ed25519.PublicKey{"beta-recipe-key": publicKey}}
	if _, err := registry.Activate(context.Background(), version); err != nil {
		t.Fatal(err)
	}
	operatorResource := recipe.ManifestResource{APIGroup: "temporal.io", Kind: "TemporalCluster", Namespace: "project-1"}
	if err := registry.ValidateRenderedResources(context.Background(), version.RecipeID, version.Version, []recipe.ManifestResource{operatorResource}); err != nil {
		t.Fatalf("declared signed operator CR rejected: %v", err)
	}
	if _, err := recipe.EvaluateCustomInstall([]recipe.ManifestResource{operatorResource}); !errors.Is(err, recipe.ErrPolicy) {
		t.Fatalf("untrusted custom operator CR accepted: %v", err)
	}
}
