package gitops

import (
	"context"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	gitopsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/gitops/v1"
)

type recordingChangeSetCommitter struct {
	calls int
	last  ScopedChangeSet
}

func (r *recordingChangeSetCommitter) CommitChangeSet(_ context.Context, value ScopedChangeSet) (string, error) {
	r.calls++
	r.last = value
	return strings.Repeat("a", 40), nil
}

func validScopedChangeSet(manifest string) ScopedChangeSet {
	return ScopedChangeSet{
		Scope:    ProjectEnvironmentScope{CellID: "cell-a", TenantID: "tenant-1", ProjectID: "p1", Environment: "staging", Namespace: "tenant-1-p1-staging"},
		Renderer: gitopsv1.RendererPlain,
		Files: map[string][]byte{
			"cells/cell-a/tenants/tenant-1/apps/p1/staging/deployment.yaml": []byte(manifest),
		},
	}
}

func safeDeployment(extraPodSpec, extraContainer string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  namespace: tenant-1-p1-staging
spec:
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
` + extraPodSpec + `      containers:
        - name: app
          image: registry.example/app@sha256:` + strings.Repeat("a", 64) + "\n" + extraContainer
}

func TestGitOps_CommitMayModifyOnlyProjectEnvironmentPath(t *testing.T) {
	valid := validScopedChangeSet(safeDeployment("", ""))
	for name, foreignPath := range map[string]string{
		"other cell":    "cells/cell-b/tenants/tenant-1/apps/p1/staging/deployment.yaml",
		"other project": "cells/cell-a/tenants/tenant-1/apps/p2/staging/deployment.yaml",
		"other env":     "cells/cell-a/tenants/tenant-1/apps/p1/production/deployment.yaml",
		"system path":   ".platform/runtime-system.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			committer := &recordingChangeSetCommitter{}
			delivery := GenericDelivery{Committer: committer}
			changeSet := valid
			changeSet.Files = map[string][]byte{
				"cells/cell-a/tenants/tenant-1/apps/p1/staging/service.yaml": []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: app\n  namespace: tenant-1-p1-staging\n"),
				foreignPath: []byte(safeDeployment("", "")),
			}
			if _, err := delivery.Commit(context.Background(), changeSet, nil); !domain.HasCode(err, domain.CodeForbidden) {
				t.Fatalf("foreign path error=%v", err)
			}
			if committer.calls != 0 {
				t.Fatalf("partial commit occurred before complete path validation: calls=%d", committer.calls)
			}
		})
	}
	committer := &recordingChangeSetCommitter{}
	sha, err := (GenericDelivery{Committer: committer}).Commit(context.Background(), valid, nil)
	if err != nil || len(sha) != 40 || committer.calls != 1 {
		t.Fatalf("verified project path was not committed exactly once: sha=%q calls=%d err=%v", sha, committer.calls, err)
	}
}

func TestGitOps_ForbiddenClusterScopedResourceRejectedBeforeCommit(t *testing.T) {
	for _, resource := range []struct {
		apiVersion, kind, name string
	}{
		{apiVersion: "rbac.authorization.k8s.io/v1", kind: "ClusterRole", name: "project-admin"},
		{apiVersion: "apiextensions.k8s.io/v1", kind: "CustomResourceDefinition", name: "widgets.example.com"},
		{apiVersion: "v1", kind: "Node", name: "worker-1"},
		{apiVersion: "storage.k8s.io/v1", kind: "StorageClass", name: "project-storage"},
	} {
		t.Run(resource.kind, func(t *testing.T) {
			manifest := "apiVersion: " + resource.apiVersion + "\nkind: " + resource.kind + "\nmetadata:\n  name: " + resource.name + "\n"
			committer := &recordingChangeSetCommitter{}
			delivery := GenericDelivery{Committer: committer}
			changeSet := validScopedChangeSet(manifest)
			if _, err := delivery.Commit(context.Background(), changeSet, nil); !domain.HasCode(err, domain.CodeForbidden) {
				t.Fatalf("cluster-scoped resource error=%v", err)
			}
			if committer.calls != 0 {
				t.Fatalf("cluster-scoped resource reached Git commit: calls=%d", committer.calls)
			}
			wrongGrant := []ClusterResourceGrant{{APIVersion: resource.apiVersion, Kind: resource.kind, Name: "other"}}
			if _, err := delivery.Commit(context.Background(), changeSet, wrongGrant); !domain.HasCode(err, domain.CodeForbidden) {
				t.Fatalf("non-exact grant accepted: %v", err)
			}
		})
	}
	exactManifest := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\n"
	committer := &recordingChangeSetCommitter{}
	grant := []ClusterResourceGrant{{APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition", Name: "widgets.example.com"}}
	if _, err := (GenericDelivery{Committer: committer}).Commit(context.Background(), validScopedChangeSet(exactManifest), grant); err != nil || committer.calls != 1 {
		t.Fatalf("exact platform recipe grant did not authorize exact cluster resource: calls=%d err=%v", committer.calls, err)
	}
}

func TestGitOps_PrivilegedWorkloadRejectedRegardlessOfHelmSource(t *testing.T) {
	cases := map[string]string{
		"privileged":                 safeDeployment("", "          securityContext:\n            privileged: true\n"),
		"allow privilege escalation": safeDeployment("", "          securityContext:\n            allowPrivilegeEscalation: true\n"),
		"host network":               safeDeployment("      hostNetwork: true\n", ""),
		"host path":                  safeDeployment("      volumes:\n        - name: host\n          hostPath:\n            path: /\n", ""),
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			committer := &recordingChangeSetCommitter{}
			delivery := GenericDelivery{Committer: committer}
			changeSet := validScopedChangeSet(manifest)
			changeSet.Renderer = gitopsv1.RendererHelm
			changeSet.TrustedRecipe = true
			if _, err := delivery.Commit(context.Background(), changeSet, nil); !domain.HasCode(err, domain.CodeForbidden) {
				t.Fatalf("privileged Helm render error=%v", err)
			}
			if committer.calls != 0 {
				t.Fatalf("privileged Helm render reached Git commit: calls=%d", committer.calls)
			}
		})
	}
}
