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
	version.Permissions = append([]recipesv1.ResourcePermission(nil), version.Permissions...)
	sort.Slice(version.Artifacts, func(i, j int) bool { return version.Artifacts[i].Name < version.Artifacts[j].Name })
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
	copy.Permissions = append([]recipesv1.ResourcePermission(nil), version.Permissions...)
	sort.Slice(copy.Artifacts, func(i, j int) bool { return copy.Artifacts[i].Name < copy.Artifacts[j].Name })
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
