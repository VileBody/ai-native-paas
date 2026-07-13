package v1

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestAttachmentSnapshot_PublicShapeIsFrozen(t *testing.T) {
	snapshot := AttachmentSnapshot{
		SnapshotID: "snapshot-1", TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1",
		Version: 2, SecretSetRef: "secret-set-1:v2", ServiceBindings: []string{"binding-1:0123456789ab"},
		ActiveDomains: []string{"app.example.test"}, CreatedAt: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"active_domains", "application_id", "created_at", "environment_id", "secret_set_ref", "service_bindings", "snapshot_id", "tenant_id", "version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public fields=%v want=%v", got, want)
	}
	for _, forbidden := range []string{"password", "private_key", "secret_value", "token", "value"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("forbidden field in snapshot: %s", raw)
		}
	}
}

func TestAttachmentSnapshot_BackwardCompatiblePayload(t *testing.T) {
	raw := []byte(`{
		"snapshot_id":"snapshot-1","tenant_id":"tenant-1","application_id":"app-1",
		"environment_id":"env-1","version":1,"secret_set_ref":"secret-set-1:v1",
		"created_at":"2026-07-13T10:00:00Z"
	}`)
	var snapshot AttachmentSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("previous v1 payload rejected: %v", err)
	}
}

func TestAttachmentSchemas_AreValidJSONAndForbidExtraFields(t *testing.T) {
	for _, path := range []string{"schemas/attachment-snapshot.schema.json", "schemas/public-errors.schema.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s does not close its public shape", path)
		}
	}
}
