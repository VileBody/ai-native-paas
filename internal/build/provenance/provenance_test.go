package provenance_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/provenance"
)

func materials() provenance.Materials {
	started := time.Date(2026, 7, 14, 12, 0, 0, 123456000, time.UTC)
	return provenance.Materials{
		BuildID: "build-1", Repository: "registry.test/tenants/t1/apps/p1",
		SourceSHA: strings.Repeat("a", 40), BuildSpecDigest: "sha256:" + strings.Repeat("b", 64),
		BuilderDigest: "sha256:" + strings.Repeat("c", 64), OutputDigest: "sha256:" + strings.Repeat("d", 64),
		StartedAt: started, FinishedAt: started.Add(2 * time.Minute),
	}
}

func TestBuild_ProvenanceBindsSourceSpecBuilderAndOutputDigest(t *testing.T) {
	attestor, verifier, err := provenance.New("platform-build-attestor")
	if err != nil {
		t.Fatal(err)
	}
	result, err := attestor.Attest(context.Background(), materials())
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(context.Background(), result.Document)
	if err != nil || verified.Digest != result.Digest || verified.Statement.Predicate.SourceSHA != materials().SourceSHA {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}

	var envelope provenance.Envelope
	if err := json.Unmarshal(result.Document, &envelope); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var statement provenance.Statement
	if err := json.Unmarshal(payload, &statement); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*provenance.Statement){
		func(value *provenance.Statement) { value.Predicate.SourceSHA = strings.Repeat("e", 40) },
		func(value *provenance.Statement) {
			value.Predicate.BuildSpecDigest = "sha256:" + strings.Repeat("e", 64)
		},
		func(value *provenance.Statement) { value.Predicate.BuilderDigest = "sha256:" + strings.Repeat("e", 64) },
		func(value *provenance.Statement) { value.Subject[0].Digest["sha256"] = strings.Repeat("e", 64) },
		func(value *provenance.Statement) {
			value.Predicate.BuildFinishedAt = value.Predicate.BuildFinishedAt.Add(time.Second)
		},
	}
	for index, mutate := range mutations {
		copyStatement := statement
		copyStatement.Subject = []provenance.Subject{{Name: statement.Subject[0].Name, Digest: map[string]string{"sha256": statement.Subject[0].Digest["sha256"]}}}
		mutate(&copyStatement)
		tamperedPayload, _ := json.Marshal(copyStatement)
		tamperedEnvelope := envelope
		tamperedEnvelope.Payload = base64.RawStdEncoding.EncodeToString(tamperedPayload)
		tamperedDocument, _ := json.Marshal(tamperedEnvelope)
		if _, err := verifier.Verify(context.Background(), tamperedDocument); !domain.HasCode(err, domain.CodePolicyRejected) {
			t.Fatalf("mutation %d accepted: %v", index, err)
		}
	}
}
