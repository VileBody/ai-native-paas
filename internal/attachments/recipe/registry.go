package recipe

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	recipesv1 "github.com/keir-research/ai-native-paas/pkg/contracts/recipes/v1"
)

var (
	ErrInvalid   = errors.New("invalid recipe")
	ErrNotFound  = errors.New("recipe not found")
	ErrConflict  = errors.New("recipe conflict")
	ErrPolicy    = errors.New("recipe policy rejected")
	ErrSignature = errors.New("recipe signature rejected")
)

type Store interface {
	PutActiveRecipe(context.Context, recipesv1.RecipeVersion) (recipesv1.RecipeVersion, error)
	GetActiveRecipe(context.Context, string, string) (recipesv1.RecipeVersion, error)
	ListActiveRecipes(context.Context, string) ([]recipesv1.RecipeVersion, error)
}

type Registry struct {
	Store Store
	Keys  map[string]ed25519.PublicKey
}

func Sign(version recipesv1.RecipeVersion, keyID string, privateKey ed25519.PrivateKey) (recipesv1.RecipeVersion, error) {
	if len(privateKey) != ed25519.PrivateKeySize || strings.TrimSpace(keyID) == "" {
		return recipesv1.RecipeVersion{}, ErrInvalid
	}
	version.SigningKeyID = strings.TrimSpace(keyID)
	version.Artifacts = append([]recipesv1.ArtifactPin(nil), version.Artifacts...)
	version.Dependencies = cloneDependencies(version.Dependencies)
	version.Permissions = append([]recipesv1.ResourcePermission(nil), version.Permissions...)
	sort.Slice(version.Artifacts, func(i, j int) bool { return version.Artifacts[i].Name < version.Artifacts[j].Name })
	sort.Slice(version.Dependencies, func(i, j int) bool { return version.Dependencies[i].RecipeID < version.Dependencies[j].RecipeID })
	for index := range version.Dependencies {
		sort.Strings(version.Dependencies[index].AllowedVersions)
	}
	sort.Slice(version.Permissions, func(i, j int) bool {
		return permissionKey(version.Permissions[i]) < permissionKey(version.Permissions[j])
	})
	version.ContentDigest = digestOf(nil)
	version.SignatureDigest = digestOf(nil)
	version.Signature = base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := version.Validate(); err != nil {
		return recipesv1.RecipeVersion{}, errors.Join(ErrInvalid, err)
	}
	payload, err := CanonicalContent(version)
	if err != nil {
		return recipesv1.RecipeVersion{}, err
	}
	version.ContentDigest = digestOf(payload)
	signature := ed25519.Sign(privateKey, payload)
	version.Signature = base64.RawStdEncoding.EncodeToString(signature)
	version.SignatureDigest = digestOf(signature)
	if err := version.Validate(); err != nil {
		return recipesv1.RecipeVersion{}, errors.Join(ErrInvalid, err)
	}
	return version, nil
}

func CanonicalContent(version recipesv1.RecipeVersion) ([]byte, error) {
	copy := version
	copy.ContentDigest = ""
	copy.SignatureDigest = ""
	copy.Signature = ""
	copy.Artifacts = append([]recipesv1.ArtifactPin(nil), version.Artifacts...)
	copy.Dependencies = cloneDependencies(version.Dependencies)
	copy.Permissions = append([]recipesv1.ResourcePermission(nil), version.Permissions...)
	sort.Slice(copy.Artifacts, func(i, j int) bool { return copy.Artifacts[i].Name < copy.Artifacts[j].Name })
	sort.Slice(copy.Dependencies, func(i, j int) bool { return copy.Dependencies[i].RecipeID < copy.Dependencies[j].RecipeID })
	for index := range copy.Dependencies {
		sort.Strings(copy.Dependencies[index].AllowedVersions)
	}
	sort.Slice(copy.Permissions, func(i, j int) bool { return permissionKey(copy.Permissions[i]) < permissionKey(copy.Permissions[j]) })
	return json.Marshal(copy)
}

func Verify(version recipesv1.RecipeVersion, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize || version.Validate() != nil {
		return ErrSignature
	}
	payload, err := CanonicalContent(version)
	if err != nil || digestOf(payload) != version.ContentDigest {
		return ErrSignature
	}
	signature, err := base64.RawStdEncoding.DecodeString(version.Signature)
	if err != nil || digestOf(signature) != version.SignatureDigest || !ed25519.Verify(publicKey, payload, signature) {
		return ErrSignature
	}
	return nil
}

func (r Registry) Activate(ctx context.Context, version recipesv1.RecipeVersion) (recipesv1.RecipeVersion, error) {
	if r.Store == nil {
		return recipesv1.RecipeVersion{}, ErrInvalid
	}
	key, ok := r.Keys[version.SigningKeyID]
	if !ok || Verify(version, key) != nil {
		return recipesv1.RecipeVersion{}, ErrSignature
	}
	return r.Store.PutActiveRecipe(ctx, version)
}

type ResolveRequest struct {
	RecipeID        string
	AllowedVersions []string
}

func (r Registry) Resolve(ctx context.Context, request ResolveRequest) (recipesv1.RecipeLock, error) {
	if r.Store == nil || strings.TrimSpace(request.RecipeID) == "" || len(request.AllowedVersions) == 0 {
		return recipesv1.RecipeLock{}, ErrInvalid
	}
	allowed := map[string]struct{}{}
	for _, version := range request.AllowedVersions {
		allowed[strings.TrimSpace(version)] = struct{}{}
	}
	versions, err := r.Store.ListActiveRecipes(ctx, request.RecipeID)
	if err != nil {
		return recipesv1.RecipeLock{}, err
	}
	filtered := make([]recipesv1.RecipeVersion, 0, len(versions))
	for _, version := range versions {
		if _, ok := allowed[version.Version]; ok {
			filtered = append(filtered, version)
		}
	}
	if len(filtered) == 0 {
		return recipesv1.RecipeLock{}, ErrNotFound
	}
	sort.Slice(filtered, func(i, j int) bool { return compareSemver(filtered[i].Version, filtered[j].Version) > 0 })
	selected := filtered[0]
	key, ok := r.Keys[selected.SigningKeyID]
	if !ok || Verify(selected, key) != nil {
		return recipesv1.RecipeLock{}, ErrSignature
	}
	lock := recipesv1.RecipeLock{RecipeID: selected.RecipeID, Version: selected.Version, ContentDigest: selected.ContentDigest, SignatureDigest: selected.SignatureDigest, Artifacts: append([]recipesv1.ArtifactPin(nil), selected.Artifacts...)}
	if err := lock.Validate(); err != nil {
		return recipesv1.RecipeLock{}, err
	}
	return lock, nil
}

// ResolveGraph produces dependency-first immutable locks for a recipe and its
// transitive dependencies. Resolution is read-only, deterministic, verifies
// every signature and backtracks across active versions when constraints are
// incompatible. No provider port is present on Registry, so a rejected graph
// cannot create an external resource.
func (r Registry) ResolveGraph(ctx context.Context, request ResolveRequest) ([]recipesv1.RecipeLock, error) {
	if r.Store == nil || strings.TrimSpace(request.RecipeID) == "" {
		return nil, ErrInvalid
	}
	rootConstraint, err := versionConstraint(request.AllowedVersions)
	if err != nil {
		return nil, err
	}
	constraints := map[string][]map[string]struct{}{
		strings.TrimSpace(request.RecipeID): {rootConstraint},
	}
	selected, err := r.solveGraph(ctx, constraints, map[string]recipesv1.RecipeVersion{})
	if err != nil {
		return nil, err
	}
	order, err := dependencyOrder(strings.TrimSpace(request.RecipeID), selected)
	if err != nil {
		return nil, err
	}
	locks := make([]recipesv1.RecipeLock, 0, len(order))
	for _, recipeID := range order {
		lock, lockErr := lockRecipe(selected[recipeID])
		if lockErr != nil {
			return nil, lockErr
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

// Plan resolves a complete immutable dependency graph and hashes only its
// normalized public inputs. It has no provider port and performs no store
// mutation, making repeated plans pure and safe before approval.
func (r Registry) Plan(ctx context.Context, request ResolveRequest) (recipesv1.InstallationPlan, error) {
	locks, err := r.ResolveGraph(ctx, request)
	if err != nil {
		return recipesv1.InstallationPlan{}, err
	}
	plan := recipesv1.InstallationPlan{RootRecipeID: strings.TrimSpace(request.RecipeID), Recipes: locks}
	payload, err := json.Marshal(struct {
		RootRecipeID string                 `json:"root_recipe_id"`
		Recipes      []recipesv1.RecipeLock `json:"recipes"`
	}{RootRecipeID: plan.RootRecipeID, Recipes: plan.Recipes})
	if err != nil {
		return recipesv1.InstallationPlan{}, ErrInvalid
	}
	plan.PlanHash = digestOf(payload)
	if err := plan.Validate(); err != nil {
		return recipesv1.InstallationPlan{}, errors.Join(ErrInvalid, err)
	}
	return plan, nil
}

func (r Registry) solveGraph(ctx context.Context, constraints map[string][]map[string]struct{}, selected map[string]recipesv1.RecipeVersion) (map[string]recipesv1.RecipeVersion, error) {
	for recipeID, version := range selected {
		if !matchesConstraints(version.Version, constraints[recipeID]) {
			return nil, ErrConflict
		}
	}
	unresolved := make([]string, 0, len(constraints))
	for recipeID := range constraints {
		if _, ok := selected[recipeID]; !ok {
			unresolved = append(unresolved, recipeID)
		}
	}
	if len(unresolved) == 0 {
		if _, err := dependencyOrder("", selected); err != nil {
			return nil, err
		}
		return selected, nil
	}
	sort.Strings(unresolved)
	recipeID := unresolved[0]
	versions, err := r.Store.ListActiveRecipes(ctx, recipeID)
	if err != nil {
		return nil, err
	}
	sort.Slice(versions, func(i, j int) bool { return compareSemver(versions[i].Version, versions[j].Version) > 0 })
	matched := false
	for _, version := range versions {
		if !matchesConstraints(version.Version, constraints[recipeID]) {
			continue
		}
		matched = true
		key, ok := r.Keys[version.SigningKeyID]
		if !ok || Verify(version, key) != nil {
			return nil, ErrSignature
		}
		nextSelected := cloneSelected(selected)
		nextSelected[recipeID] = version
		nextConstraints := cloneConstraints(constraints)
		valid := true
		for _, dependency := range version.Dependencies {
			constraint, constraintErr := versionConstraint(dependency.AllowedVersions)
			if constraintErr != nil {
				return nil, constraintErr
			}
			nextConstraints[dependency.RecipeID] = append(nextConstraints[dependency.RecipeID], constraint)
			if current, exists := nextSelected[dependency.RecipeID]; exists && !matchesConstraints(current.Version, nextConstraints[dependency.RecipeID]) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		resolved, solveErr := r.solveGraph(ctx, nextConstraints, nextSelected)
		if solveErr == nil {
			return resolved, nil
		}
		if !errors.Is(solveErr, ErrConflict) {
			return nil, solveErr
		}
	}
	if !matched {
		return nil, ErrConflict
	}
	return nil, ErrConflict
}

func versionConstraint(versions []string) (map[string]struct{}, error) {
	if len(versions) == 0 {
		return nil, ErrInvalid
	}
	constraint := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		version = strings.TrimSpace(version)
		parts := strings.Split(version, ".")
		if len(parts) != 3 {
			return nil, ErrInvalid
		}
		for _, part := range parts {
			if _, err := strconv.ParseUint(part, 10, 64); err != nil {
				return nil, ErrInvalid
			}
		}
		constraint[version] = struct{}{}
	}
	return constraint, nil
}

func matchesConstraints(version string, constraints []map[string]struct{}) bool {
	if len(constraints) == 0 {
		return false
	}
	for _, constraint := range constraints {
		if _, ok := constraint[version]; !ok {
			return false
		}
	}
	return true
}

func dependencyOrder(root string, selected map[string]recipesv1.RecipeVersion) ([]string, error) {
	states := map[string]uint8{}
	order := make([]string, 0, len(selected))
	var visit func(string) error
	visit = func(recipeID string) error {
		switch states[recipeID] {
		case 1:
			return ErrConflict
		case 2:
			return nil
		}
		version, ok := selected[recipeID]
		if !ok {
			return ErrConflict
		}
		states[recipeID] = 1
		for _, dependency := range version.Dependencies {
			if err := visit(dependency.RecipeID); err != nil {
				return err
			}
		}
		states[recipeID] = 2
		order = append(order, recipeID)
		return nil
	}
	if root != "" {
		if err := visit(root); err != nil {
			return nil, err
		}
		return order, nil
	}
	ids := make([]string, 0, len(selected))
	for recipeID := range selected {
		ids = append(ids, recipeID)
	}
	sort.Strings(ids)
	for _, recipeID := range ids {
		if err := visit(recipeID); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func lockRecipe(selected recipesv1.RecipeVersion) (recipesv1.RecipeLock, error) {
	lock := recipesv1.RecipeLock{RecipeID: selected.RecipeID, Version: selected.Version, ContentDigest: selected.ContentDigest, SignatureDigest: selected.SignatureDigest, Artifacts: append([]recipesv1.ArtifactPin(nil), selected.Artifacts...)}
	if err := lock.Validate(); err != nil {
		return recipesv1.RecipeLock{}, err
	}
	return lock, nil
}

func cloneDependencies(values []recipesv1.DependencyConstraint) []recipesv1.DependencyConstraint {
	copy := make([]recipesv1.DependencyConstraint, len(values))
	for index, value := range values {
		copy[index] = value
		copy[index].AllowedVersions = append([]string(nil), value.AllowedVersions...)
	}
	return copy
}

func cloneSelected(values map[string]recipesv1.RecipeVersion) map[string]recipesv1.RecipeVersion {
	copy := make(map[string]recipesv1.RecipeVersion, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func cloneConstraints(values map[string][]map[string]struct{}) map[string][]map[string]struct{} {
	copy := make(map[string][]map[string]struct{}, len(values))
	for recipeID, constraints := range values {
		copy[recipeID] = make([]map[string]struct{}, len(constraints))
		for index, constraint := range constraints {
			copy[recipeID][index] = make(map[string]struct{}, len(constraint))
			for version := range constraint {
				copy[recipeID][index][version] = struct{}{}
			}
		}
	}
	return copy
}

type ManifestResource struct {
	APIGroup     string
	Kind         string
	Namespace    string
	ClusterScope bool
}

type InstallDecision struct {
	Allowed          bool
	Trusted          bool
	RequiresApproval bool
	PolicyProfile    string
}

func (r Registry) ValidateRenderedResources(ctx context.Context, recipeID, version string, resources []ManifestResource) error {
	if r.Store == nil {
		return ErrInvalid
	}
	active, err := r.Store.GetActiveRecipe(ctx, recipeID, version)
	if err != nil {
		return err
	}
	key, ok := r.Keys[active.SigningKeyID]
	if !ok || Verify(active, key) != nil {
		return ErrSignature
	}
	return validateDeclaredResources(active, resources)
}

func validateDeclaredResources(version recipesv1.RecipeVersion, resources []ManifestResource) error {
	if version.Validate() != nil || len(resources) == 0 {
		return ErrInvalid
	}
	for _, resource := range resources {
		allowed := false
		for _, permission := range version.Permissions {
			if permission.APIGroup == resource.APIGroup && permission.Kind == resource.Kind &&
				((permission.Scope == recipesv1.PermissionCluster && resource.ClusterScope) || (permission.Scope == recipesv1.PermissionNamespaced && !resource.ClusterScope && strings.TrimSpace(resource.Namespace) != "")) {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrPolicy
		}
	}
	return nil
}

func EvaluateCustomInstall(resources []ManifestResource) (InstallDecision, error) {
	allowed := map[string]struct{}{
		"/ConfigMap": {}, "/PersistentVolumeClaim": {}, "/Secret": {}, "/Service": {},
		"apps/Deployment": {}, "apps/StatefulSet": {}, "batch/CronJob": {}, "batch/Job": {},
	}
	for _, resource := range resources {
		if resource.ClusterScope || strings.TrimSpace(resource.Namespace) == "" {
			return InstallDecision{}, ErrPolicy
		}
		if _, ok := allowed[resource.APIGroup+"/"+resource.Kind]; !ok {
			return InstallDecision{}, ErrPolicy
		}
	}
	return InstallDecision{Allowed: true, Trusted: false, RequiresApproval: true, PolicyProfile: "custom-strict-v1"}, nil
}

func digestOf(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func permissionKey(value recipesv1.ResourcePermission) string {
	return value.APIGroup + "/" + value.Kind + "/" + string(value.Scope)
}

func compareSemver(left, right string) int {
	a, b := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < 3; index++ {
		av, _ := strconv.Atoi(a[index])
		bv, _ := strconv.Atoi(b[index])
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
