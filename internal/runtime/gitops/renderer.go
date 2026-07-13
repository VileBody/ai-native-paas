package gitops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Renderer struct{}

type namespaceManifest struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Metadata   runtimev1.ObjectMeta `json:"metadata"`
}

var safeSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (Renderer) Render(input application.RenderInput) (application.RenderedBundle, error) {
	if input.Application.ID == "" || input.Environment.ID == "" || input.Release.ID == "" || input.Placement.ID == "" || input.Cell.ID == "" {
		return application.RenderedBundle{}, domain.NewError(domain.CodeInvalidArgument, "render input is incomplete")
	}
	if input.Application.TenantID != input.Environment.TenantID || input.Application.TenantID != input.Release.TenantID || input.Environment.ID != input.Release.EnvironmentID || input.Placement.EnvironmentID != input.Environment.ID || input.Placement.CellID != input.Cell.ID {
		return application.RenderedBundle{}, domain.NewError(domain.CodeConflict, "render input ownership mismatch")
	}
	for _, segment := range []string{input.Cell.ID, input.Application.TenantID, input.Application.ID, input.Environment.Name} {
		if !safeSegment.MatchString(segment) || strings.Contains(segment, "..") {
			return application.RenderedBundle{}, domain.NewError(domain.CodeInvalidArgument, "GitOps path segment is unsafe")
		}
	}
	routeProcess, ok := runtimev1.SelectRouteProcess(input.Release.Configuration.Processes)
	if !ok {
		return application.RenderedBundle{}, domain.NewError(domain.CodeInvalidArgument, "release has no routable process")
	}
	name := domain.PaaSAppName(input.Application.ID, input.Environment.Name)
	labels := map[string]string{
		"platform.example.com/tenant-id":      input.Application.TenantID,
		"platform.example.com/application-id": input.Application.ID,
		"platform.example.com/environment-id": input.Environment.ID,
		"platform.example.com/managed-by":     "paas-control-plane",
	}
	namespace := namespaceManifest{APIVersion: "v1", Kind: "Namespace", Metadata: runtimev1.ObjectMeta{Name: input.Environment.Namespace, Labels: labels}}
	app := runtimev1.PaaSApp{
		TypeMeta: runtimev1.TypeMeta{APIVersion: runtimev1.APIVersion, Kind: runtimev1.Kind},
		Metadata: runtimev1.ObjectMeta{Name: name, Namespace: input.Environment.Namespace, Labels: labels, Annotations: map[string]string{"platform.example.com/release-id": input.Release.ID}},
		Spec: runtimev1.PaaSAppSpec{
			Identity:              runtimev1.IdentitySpec{TenantID: input.Application.TenantID, ProjectID: input.Application.ProjectID, ApplicationID: input.Application.ID, EnvironmentID: input.Environment.ID, Environment: input.Environment.Name, ReleaseID: input.Release.ID},
			Image:                 runtimev1.ImageSpec{Repository: input.Release.Artifact.Repository, Digest: input.Release.Artifact.Digest, MediaType: input.Release.Artifact.MediaType},
			Runtime:               runtimev1.RuntimeSpec{Isolation: input.Release.Configuration.Isolation, Unit: input.Release.Configuration.Unit},
			Processes:             cloneProcesses(input.Release.Configuration.Processes),
			Release:               runtimev1.ReleaseSpec{Strategy: "Rolling", Migration: input.Release.Configuration.Migration, RolloutTimeout: input.Release.Configuration.RolloutTimeoutSeconds, PreviousRelease: previousActive(input)},
			Route:                 runtimev1.RouteSpec{GeneratedHostname: input.Release.Configuration.GeneratedHostname, Process: routeProcess},
			AttachmentSnapshotRef: input.Release.Configuration.AttachmentSnapshotRef,
			Network:               runtimev1.NetworkSpec{EgressProfile: input.Release.Configuration.EgressProfile},
			Lifecycle:             runtimev1.LifecycleSpec{State: input.Application.Lifecycle},
		},
	}
	if err := app.Validate(); err != nil {
		return application.RenderedBundle{}, domain.Wrap(domain.CodeInvalidArgument, "rendered PaaSApp is invalid", err)
	}
	namespaceRaw, err := canonicalJSON(namespace)
	if err != nil {
		return application.RenderedBundle{}, domain.Wrap(domain.CodeInternal, "encode namespace manifest", err)
	}
	appRaw, err := canonicalJSON(app)
	if err != nil {
		return application.RenderedBundle{}, domain.Wrap(domain.CodeInternal, "encode PaaSApp manifest", err)
	}
	path := filepath.ToSlash(filepath.Join("cells", input.Cell.ID, "tenants", input.Application.TenantID, "apps", input.Application.ID, input.Environment.Name))
	files := map[string][]byte{"namespace.yaml": namespaceRaw, "paasapp.yaml": appRaw}
	return application.RenderedBundle{CellID: input.Cell.ID, ReleaseID: input.Release.ID, Path: path, ManifestHash: hashFiles(files), Files: files}, nil
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func cloneProcesses(in map[string]runtimev1.ProcessSpec) map[string]runtimev1.ProcessSpec {
	out := make(map[string]runtimev1.ProcessSpec, len(in))
	for k, v := range in {
		v.Command = append([]string(nil), v.Command...)
		out[k] = v
	}
	return out
}

func previousActive(input application.RenderInput) string {
	// The environment contains the release that was active when this candidate
	// was rendered. It is intentionally not inferred from mutable cluster state.
	return input.Environment.ActiveReleaseID
}
func hashFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(files[name])
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
func BundleBytes(bundle application.RenderedBundle) []byte {
	names := make([]string, 0, len(bundle.Files))
	for name := range bundle.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	var out bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&out, "# file: %s\n", name)
		out.Write(bundle.Files[name])
	}
	return out.Bytes()
}

var _ application.Renderer = Renderer{}
