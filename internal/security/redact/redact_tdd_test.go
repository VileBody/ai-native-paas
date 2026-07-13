package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestKernel_AuditRedactsWorkspaceCommandSecrets(t *testing.T) {
	const sentinel = "sentinel-super-secret-value"
	raw := json.RawMessage(`{"argv":["tofu","apply","-var","db_password=sentinel-super-secret-value"],"environment":{"API_TOKEN":"another-secret","NORMAL":"safe"},"provider":{"credential":"opaque"}}`)
	redacted, err := JSON(raw, sentinel, "another-secret", "opaque")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{sentinel, "another-secret", "opaque"} {
		if bytes.Contains(redacted, []byte(forbidden)) {
			t.Fatalf("audit leaked %q: %s", forbidden, redacted)
		}
	}
	if !bytes.Contains(redacted, []byte(`"NORMAL":"safe"`)) || !bytes.Contains(redacted, []byte(Replacement)) {
		t.Fatalf("audit redaction destroyed non-secret context: %s", redacted)
	}
}

func TestWorkspace_StdoutStderrStreamingRedactsSecretsAcrossChunkBoundaries(t *testing.T) {
	const secret = "workspace-secret-sentinel"
	input := []byte("before " + secret + " middle " + secret + " after")
	for boundary := 1; boundary < len(input); boundary++ {
		stream, err := NewStream(secret)
		if err != nil {
			t.Fatal(err)
		}
		first, err := stream.Write(input[:boundary])
		if err != nil {
			t.Fatal(err)
		}
		second, err := stream.Write(input[boundary:])
		if err != nil {
			t.Fatal(err)
		}
		last, err := stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		output := string(append(append(first, second...), last...))
		if strings.Contains(output, secret) || strings.Count(output, Replacement) != 2 {
			t.Fatalf("boundary %d leaked or lost redaction: %q", boundary, output)
		}
	}
}
