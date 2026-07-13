package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzPaaSAppValidateNeverPanics(f *testing.F) {
	valid := PaaSApp{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: Kind},
		Metadata: ObjectMeta{Name: "paas-app-a-production", Namespace: "app-a-production"},
		Spec: PaaSAppSpec{
			Identity:              IdentitySpec{TenantID: "tenant-a", ProjectID: "project-a", ApplicationID: "app-a", EnvironmentID: "env-a", Environment: "production", ReleaseID: "release-a"},
			Image:                 ImageSpec{Repository: "registry.example.invalid/tenants/tenant-a/apps/app-a", Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Runtime:               RuntimeSpec{Isolation: IsolationSandboxed, Unit: "u1"},
			Processes:             map[string]ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 3, HealthPath: "/health", StartupTimeout: 60, ReadinessTimeout: 30}},
			Release:               ReleaseSpec{Strategy: "Rolling", RolloutTimeout: 300},
			Route:                 RouteSpec{GeneratedHostname: "app-a-production.apps.example.invalid", Process: "web"},
			AttachmentSnapshotRef: "none",
			Network:               NetworkSpec{EgressProfile: "public-default"},
			Lifecycle:             LifecycleSpec{State: LifecycleActive},
		},
	}
	seed, err := json.Marshal(valid)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"apiVersion":"platform.example.com/v1alpha1"}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		var app PaaSApp
		if err := json.Unmarshal(raw, &app); err != nil {
			return
		}
		_ = app.Validate()
	})
}
