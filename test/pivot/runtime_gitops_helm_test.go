package pivot_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspaceagent"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func TestGitOps_HelmRenderIsDeterministicForPinnedInputs(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm CLI is not installed in this test workspace")
	}
	root := t.TempDir()
	chartRoot := filepath.Join(root, "chart")
	if err := os.MkdirAll(filepath.Join(chartRoot, "templates"), 0o750); err != nil {
		t.Fatal(err)
	}
	chart := "apiVersion: v2\nname: deterministic-app\nversion: 1.2.3\ntype: application\n"
	template := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
  annotations:
    test.platform.example.com/kube-version: {{ .Capabilities.KubeVersion.Version | quote }}
    test.platform.example.com/widget-api: {{ has "example.com/v1/Widget" .Capabilities.APIVersions | quote }}
spec:
  selector:
    matchLabels:
      app: {{ .Release.Name }}
  template:
    metadata:
      labels:
        app: {{ .Release.Name }}
    spec:
      containers:
        - name: app
          image: {{ .Values.image | quote }}
          securityContext:
            allowPrivilegeEscalation: false
            privileged: false
`
	values := "image: registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	for path, raw := range map[string]string{
		filepath.Join(chartRoot, "Chart.yaml"):                   chart,
		filepath.Join(chartRoot, "templates", "deployment.yaml"): template,
		filepath.Join(root, "values.yaml"):                       values,
	} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := gitops.PinnedHelmChartDigest(chartRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitops.VerifyPinnedHelmChart(chartRoot, "1.2.3", digest); err != nil {
		t.Fatal(err)
	}

	executor := workspaceagent.Executor{
		WorkspaceRoot: root,
		Policy:        workspace.DefaultCommandPolicy(),
		Now:           func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
		KillGrace:     100 * time.Millisecond,
	}
	spec := workspacev1.CommandSpec{
		Argv: []string{
			"helm", "template", "deterministic-app", "./chart",
			"--namespace", "tenant-1-p1-staging",
			"--values", "values.yaml",
			"--kube-version", "1.31.0",
			"--api-versions", "example.com/v1/Widget",
			"--include-crds",
		},
		WorkingDir: ".", TimeoutSeconds: 30, OutputLimitBytes: 1 << 20,
	}
	environment := workspaceagent.ResolvedEnvironment{Values: map[string]string{}, SystemValues: map[string]string{
		"HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1", "NO_PROXY": "",
	}}
	first, err := executor.Execute(context.Background(), "helm-render-1", spec, environment)
	if err != nil || first.State != workspacev1.CommandSucceeded {
		t.Fatalf("first pinned Helm render failed: state=%s stderr=%s err=%v", first.State, first.Stderr, err)
	}
	second, err := executor.Execute(context.Background(), "helm-render-2", spec, environment)
	if err != nil || second.State != workspacev1.CommandSucceeded {
		t.Fatalf("second pinned Helm render failed: state=%s stderr=%s err=%v", second.State, second.Stderr, err)
	}
	if string(first.Stdout) != string(second.Stdout) || len(first.Stdout) == 0 {
		t.Fatalf("same pinned Helm inputs produced different manifests\nfirst:\n%s\nsecond:\n%s", first.Stdout, second.Stdout)
	}
	decision, err := gitops.ValidateRenderedResources(first.Stdout, "tenant-1-p1-staging", nil)
	if err != nil || !decision.Allowed {
		t.Fatalf("deterministic Helm output failed rendered policy: decision=%+v err=%v", decision, err)
	}

	if err := os.WriteFile(filepath.Join(chartRoot, "templates", "deployment.yaml"), []byte(template+"# tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gitops.VerifyPinnedHelmChart(chartRoot, "1.2.3", digest); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("tampered chart retained its pin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartRoot, "templates", "deployment.yaml"), []byte(template+"# {{ now }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitops.PinnedHelmChartDigest(chartRoot); !domain.HasCode(err, domain.CodeForbidden) {
		t.Fatalf("wall-clock Helm template accepted: %v", err)
	}
}
