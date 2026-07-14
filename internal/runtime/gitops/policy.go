package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	gitopsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/gitops/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	"gopkg.in/yaml.v3"
)

const (
	maximumChangeSetFiles = 512
	maximumManifestBytes  = 8 << 20
	gitOpsPolicyVersion   = "controlled-beta-v1"
)

type ProjectEnvironmentScope struct {
	CellID, TenantID, ProjectID, Environment, Namespace string
}

type ScopedChangeSet struct {
	Scope         ProjectEnvironmentScope
	Renderer      gitopsv1.Renderer
	TrustedRecipe bool
	Files         map[string][]byte
}

type ClusterResourceGrant struct {
	APIVersion, Kind, Name string
}

type ChangeSetCommitter interface {
	CommitChangeSet(context.Context, ScopedChangeSet) (string, error)
}

// GenericDelivery validates the complete change set and every rendered
// resource before invoking the Git writer once. Renderer provenance and a
// trusted recipe marker never bypass the rendered-resource policy.
type GenericDelivery struct {
	Committer ChangeSetCommitter
}

func (d GenericDelivery) Commit(ctx context.Context, changeSet ScopedChangeSet, grants []ClusterResourceGrant) (string, error) {
	paths, err := validateScopedPaths(changeSet)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if _, err := ValidateRenderedResources(changeSet.Files[path], changeSet.Scope.Namespace, grants); err != nil {
			return "", err
		}
	}
	if d.Committer == nil {
		return "", domain.NewError(domain.CodeUnavailable, "GitOps change-set committer unavailable")
	}
	return d.Committer.CommitChangeSet(ctx, cloneChangeSet(changeSet))
}

func validateScopedPaths(changeSet ScopedChangeSet) ([]string, error) {
	scope := changeSet.Scope
	if !runtimev1.ValidDNSLabel(scope.CellID) || !runtimev1.ValidPlatformID(scope.TenantID) ||
		!runtimev1.ValidPlatformID(scope.ProjectID) || !runtimev1.ValidDNSLabel(scope.Environment) ||
		!runtimev1.ValidDNSLabel(scope.Namespace) || len(changeSet.Files) == 0 || len(changeSet.Files) > maximumChangeSetFiles {
		return nil, domain.NewError(domain.CodeInvalidArgument, "GitOps project scope is incomplete")
	}
	switch changeSet.Renderer {
	case gitopsv1.RendererHelm, gitopsv1.RendererKustomize, gitopsv1.RendererPlain:
	default:
		return nil, domain.NewError(domain.CodeInvalidArgument, "GitOps renderer is invalid")
	}
	prefix := strings.Join([]string{"cells", scope.CellID, "tenants", scope.TenantID, "apps", scope.ProjectID, scope.Environment}, "/") + "/"
	paths := make([]string, 0, len(changeSet.Files))
	for path, raw := range changeSet.Files {
		if err := safeRelativePath(path); err != nil || !strings.HasPrefix(path, prefix) || len(path) <= len(prefix) || len(raw) == 0 || len(raw) > maximumManifestBytes {
			return nil, domain.NewError(domain.CodeForbidden, "GitOps change escapes verified project environment")
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".yaml" && extension != ".yml" {
			return nil, domain.NewError(domain.CodeInvalidArgument, "generic GitOps change must contain rendered YAML")
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

type policyManifest struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   policyMetadata `yaml:"metadata"`
	Spec       map[string]any `yaml:"spec"`
}

type policyMetadata struct {
	Name, Namespace string
}

var knownClusterResources = map[string]struct{}{
	"/Namespace": {}, "/Node": {}, "/PersistentVolume": {},
	"apiextensions.k8s.io/CustomResourceDefinition":               {},
	"apiregistration.k8s.io/APIService":                           {},
	"rbac.authorization.k8s.io/ClusterRole":                       {},
	"rbac.authorization.k8s.io/ClusterRoleBinding":                {},
	"scheduling.k8s.io/PriorityClass":                             {},
	"storage.k8s.io/CSIDriver":                                    {},
	"storage.k8s.io/StorageClass":                                 {},
	"node.k8s.io/RuntimeClass":                                    {},
	"admissionregistration.k8s.io/MutatingWebhookConfiguration":   {},
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration": {},
}

var knownNamespacedResources = map[string]struct{}{
	"/ConfigMap": {}, "/Endpoints": {}, "/PersistentVolumeClaim": {}, "/Pod": {}, "/Secret": {}, "/Service": {}, "/ServiceAccount": {},
	"apps/ControllerRevision": {}, "apps/DaemonSet": {}, "apps/Deployment": {}, "apps/ReplicaSet": {}, "apps/StatefulSet": {},
	"autoscaling/HorizontalPodAutoscaler": {},
	"batch/CronJob":                       {}, "batch/Job": {},
	"networking.k8s.io/Ingress": {}, "networking.k8s.io/NetworkPolicy": {},
	"policy/PodDisruptionBudget":     {},
	"rbac.authorization.k8s.io/Role": {}, "rbac.authorization.k8s.io/RoleBinding": {},
}

// ValidateRenderedResources evaluates rendered Kubernetes objects, never the
// source chart's trust label. Cluster-scoped objects require an exact
// apiVersion/kind/name grant; all other objects must target the verified
// namespace.
func ValidateRenderedResources(raw []byte, targetNamespace string, grants []ClusterResourceGrant) (gitopsv1.PolicyDecision, error) {
	sum := sha256.Sum256(raw)
	decision := gitopsv1.PolicyDecision{PolicyVersion: gitOpsPolicyVersion, ManifestDigest: "sha256:" + hex.EncodeToString(sum[:])}
	if len(raw) == 0 || len(raw) > maximumManifestBytes || !runtimev1.ValidDNSLabel(targetNamespace) {
		return decision, domain.NewError(domain.CodeInvalidArgument, "invalid rendered GitOps manifest")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	documents := 0
	for {
		var manifest policyManifest
		err := decoder.Decode(&manifest)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return decision, domain.NewError(domain.CodeInvalidArgument, "invalid rendered Kubernetes YAML")
		}
		if manifest.APIVersion == "" && manifest.Kind == "" && manifest.Metadata.Name == "" {
			continue
		}
		documents++
		if documents > maximumChangeSetFiles || strings.TrimSpace(manifest.APIVersion) == "" || strings.TrimSpace(manifest.Kind) == "" || strings.TrimSpace(manifest.Metadata.Name) == "" {
			return rejectPolicy(decision, "resource_identity")
		}
		resourceKey := apiGroup(manifest.APIVersion) + "/" + manifest.Kind
		_, knownCluster := knownClusterResources[resourceKey]
		_, knownNamespaced := knownNamespacedResources[resourceKey]
		clusterScoped := knownCluster || !knownNamespaced && strings.TrimSpace(manifest.Metadata.Namespace) == ""
		if manifest.Kind == "List" {
			return rejectPolicy(decision, "resource_list_not_allowed")
		}
		if clusterScoped {
			if !hasExactGrant(grants, manifest) {
				return rejectPolicy(decision, "cluster_scoped_resource")
			}
		} else if manifest.Metadata.Namespace != targetNamespace {
			return rejectPolicy(decision, "namespace_mismatch")
		}
		if podSpec := workloadPodSpec(manifest); podSpec != nil && privilegedPodSpec(podSpec) {
			return rejectPolicy(decision, "privileged_workload")
		}
	}
	if documents == 0 {
		return decision, domain.NewError(domain.CodeInvalidArgument, "rendered GitOps manifest is empty")
	}
	decision.Allowed = true
	return decision, nil
}

func rejectPolicy(decision gitopsv1.PolicyDecision, reason string) (gitopsv1.PolicyDecision, error) {
	decision.Reasons = []string{reason}
	return decision, domain.NewError(domain.CodeForbidden, "rendered GitOps resource rejected by policy")
}

func apiGroup(apiVersion string) string {
	apiVersion = strings.TrimSpace(apiVersion)
	if index := strings.IndexByte(apiVersion, '/'); index >= 0 {
		return apiVersion[:index]
	}
	return ""
}

func hasExactGrant(grants []ClusterResourceGrant, manifest policyManifest) bool {
	for _, grant := range grants {
		if grant.APIVersion == manifest.APIVersion && grant.Kind == manifest.Kind && grant.Name == manifest.Metadata.Name {
			return true
		}
	}
	return false
}

func workloadPodSpec(manifest policyManifest) map[string]any {
	switch manifest.Kind {
	case "Pod":
		return manifest.Spec
	case "DaemonSet", "Deployment", "ReplicaSet", "StatefulSet", "Job":
		return nestedMap(manifest.Spec, "template", "spec")
	case "CronJob":
		return nestedMap(manifest.Spec, "jobTemplate", "spec", "template", "spec")
	default:
		return nil
	}
}

func privilegedPodSpec(spec map[string]any) bool {
	for _, key := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if value, ok := spec[key].(bool); ok && value {
			return true
		}
	}
	for _, volume := range objectSlice(spec["volumes"]) {
		if _, exists := volume["hostPath"]; exists {
			return true
		}
	}
	if securityContext, ok := spec["securityContext"].(map[string]any); ok && windowsHostProcess(securityContext) {
		return true
	}
	for _, key := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, container := range objectSlice(spec[key]) {
			securityContext, _ := container["securityContext"].(map[string]any)
			for _, field := range []string{"privileged", "allowPrivilegeEscalation"} {
				if value, ok := securityContext[field].(bool); ok && value {
					return true
				}
			}
			if windowsHostProcess(securityContext) {
				return true
			}
		}
	}
	return false
}

func windowsHostProcess(securityContext map[string]any) bool {
	windows, _ := securityContext["windowsOptions"].(map[string]any)
	value, _ := windows["hostProcess"].(bool)
	return value
}

func nestedMap(value map[string]any, path ...string) map[string]any {
	current := value
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func objectSlice(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func cloneChangeSet(value ScopedChangeSet) ScopedChangeSet {
	copy := value
	copy.Files = make(map[string][]byte, len(value.Files))
	for path, raw := range value.Files {
		copy.Files[path] = append([]byte(nil), raw...)
	}
	return copy
}
