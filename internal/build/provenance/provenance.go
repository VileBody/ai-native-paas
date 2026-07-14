// Package provenance creates and verifies a minimal signed in-toto statement
// for immutable v2 build outputs.
package provenance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

const (
	MediaType     = "application/vnd.dsse.envelope.v1+json"
	PayloadType   = "application/vnd.in-toto+json"
	StatementType = "https://in-toto.io/Statement/v1"
	PredicateType = "https://ai-native-paas.example.com/BuildProvenance/v2"
)

type Materials struct {
	BuildID, Repository, SourceSHA, BuildSpecDigest, BuilderDigest, OutputDigest string
	StartedAt, FinishedAt                                                        time.Time
}

type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type Predicate struct {
	BuildID         string    `json:"build_id"`
	SourceSHA       string    `json:"source_sha"`
	BuildSpecDigest string    `json:"build_spec_digest"`
	BuilderDigest   string    `json:"builder_digest"`
	BuildStartedAt  time.Time `json:"build_started_at"`
	BuildFinishedAt time.Time `json:"build_finished_at"`
}

type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

type EnvelopeSignature struct {
	KeyID     string `json:"keyid"`
	Signature string `json:"sig"`
}

type Envelope struct {
	PayloadType string              `json:"payloadType"`
	Payload     string              `json:"payload"`
	Signatures  []EnvelopeSignature `json:"signatures"`
}

type Result struct {
	Digest    string
	MediaType string
	Document  []byte
	Statement Statement
}

type Attestor struct {
	Issuer     string
	PrivateKey ed25519.PrivateKey
}

type Verifier struct {
	TrustedIssuers map[string]ed25519.PublicKey
}

func New(issuer string) (Attestor, Verifier, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" || len(issuer) > 256 || strings.ContainsAny(issuer, "\x00\r\n") {
		return Attestor{}, Verifier{}, errors.New("provenance issuer is invalid")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Attestor{}, Verifier{}, err
	}
	return Attestor{Issuer: issuer, PrivateKey: privateKey}, Verifier{TrustedIssuers: map[string]ed25519.PublicKey{issuer: publicKey}}, nil
}

func (a Attestor) Attest(ctx context.Context, materials Materials) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Issuer) == "" || len(a.PrivateKey) != ed25519.PrivateKeySize {
		return Result{}, domain.NewError(domain.CodeUnavailable, "provenance attestor is not configured")
	}
	statement, err := statementFor(materials)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(statement)
	if err != nil {
		return Result{}, err
	}
	signature := ed25519.Sign(a.PrivateKey, preAuthenticationEncoding(PayloadType, payload))
	envelope := Envelope{
		PayloadType: PayloadType,
		Payload:     base64.RawStdEncoding.EncodeToString(payload),
		Signatures:  []EnvelopeSignature{{KeyID: a.Issuer, Signature: base64.RawStdEncoding.EncodeToString(signature)}},
	}
	document, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, err
	}
	return Result{Digest: digest(document), MediaType: MediaType, Document: document, Statement: statement}, nil
}

func (v Verifier) Verify(ctx context.Context, document []byte) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "invalid provenance envelope")
	}
	if err := ensureEOF(decoder); err != nil || envelope.PayloadType != PayloadType || len(envelope.Signatures) != 1 {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "invalid provenance envelope")
	}
	signature := envelope.Signatures[0]
	publicKey, ok := v.TrustedIssuers[signature.KeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "untrusted provenance issuer")
	}
	payload, err := base64.RawStdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "invalid provenance payload encoding")
	}
	rawSignature, err := base64.RawStdEncoding.DecodeString(signature.Signature)
	if err != nil || !ed25519.Verify(publicKey, preAuthenticationEncoding(envelope.PayloadType, payload), rawSignature) {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "provenance signature verification failed")
	}
	var statement Statement
	decoder = json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&statement); err != nil || ensureEOF(decoder) != nil || validateStatement(statement) != nil {
		return Result{}, domain.NewError(domain.CodePolicyRejected, "invalid signed provenance statement")
	}
	copyDocument := append([]byte(nil), document...)
	return Result{Digest: digest(copyDocument), MediaType: MediaType, Document: copyDocument, Statement: statement}, nil
}

func statementFor(materials Materials) (Statement, error) {
	materials.BuildID = strings.TrimSpace(materials.BuildID)
	materials.Repository = strings.TrimSpace(materials.Repository)
	materials.SourceSHA = strings.ToLower(strings.TrimSpace(materials.SourceSHA))
	if materials.BuildID == "" || materials.Repository == "" || strings.ContainsAny(materials.BuildID+materials.Repository, "\x00\r\n") || !sourcev1.ValidCommitSHA(materials.SourceSHA) || len(materials.SourceSHA) < 40 || !buildv1.ValidDigest(materials.BuildSpecDigest) || !buildv1.ValidDigest(materials.BuilderDigest) || !buildv1.ValidDigest(materials.OutputDigest) || materials.StartedAt.IsZero() || materials.FinishedAt.IsZero() || materials.FinishedAt.Before(materials.StartedAt) {
		return Statement{}, domain.NewError(domain.CodeInvalidArgument, "invalid provenance materials")
	}
	return Statement{
		Type:          StatementType,
		Subject:       []Subject{{Name: materials.Repository, Digest: map[string]string{"sha256": strings.TrimPrefix(materials.OutputDigest, "sha256:")}}},
		PredicateType: PredicateType,
		Predicate: Predicate{
			BuildID: materials.BuildID, SourceSHA: materials.SourceSHA,
			BuildSpecDigest: materials.BuildSpecDigest, BuilderDigest: materials.BuilderDigest,
			BuildStartedAt: materials.StartedAt.UTC().Truncate(time.Microsecond), BuildFinishedAt: materials.FinishedAt.UTC().Truncate(time.Microsecond),
		},
	}, nil
}

func validateStatement(statement Statement) error {
	if statement.Type != StatementType || statement.PredicateType != PredicateType || len(statement.Subject) != 1 || len(statement.Subject[0].Digest) != 1 {
		return errors.New("invalid provenance statement")
	}
	output := "sha256:" + statement.Subject[0].Digest["sha256"]
	_, err := statementFor(Materials{
		BuildID: statement.Predicate.BuildID, Repository: statement.Subject[0].Name,
		SourceSHA: statement.Predicate.SourceSHA, BuildSpecDigest: statement.Predicate.BuildSpecDigest,
		BuilderDigest: statement.Predicate.BuilderDigest, OutputDigest: output,
		StartedAt: statement.Predicate.BuildStartedAt, FinishedAt: statement.Predicate.BuildFinishedAt,
	})
	return err
}

func preAuthenticationEncoding(payloadType string, payload []byte) []byte {
	prefix := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	return append([]byte(prefix), payload...)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
