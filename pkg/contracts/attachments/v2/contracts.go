// Package v2 separates secrets, credentials, recipes, provider resources and capabilities.
package v2

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const APIVersion = "attachments.platform.example.com/v2"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type SecretVersionRef struct {
	SecretID  string `json:"secret_id"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	ValueHash string `json:"value_hash"`
}

func (r SecretVersionRef) Validate() error {
	if r.SecretID == "" || !regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`).MatchString(r.Name) || r.Version < 1 || !digest.MatchString(r.ValueHash) {
		return errors.New("invalid secret version reference")
	}
	return nil
}

// SecretWrite is accepted only by the write-only secret endpoint and must never
// be serialized to events, audit logs or snapshots.
type SecretWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CredentialBindingRef struct {
	BindingID    string    `json:"binding_id"`
	Provider     string    `json:"provider"`
	CredentialID string    `json:"credential_id"`
	Audience     string    `json:"audience"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (r CredentialBindingRef) Validate() error {
	if !validReference(r.BindingID) || strings.TrimSpace(r.Provider) == "" || !validReference(r.CredentialID) || strings.TrimSpace(r.Audience) == "" || r.ExpiresAt.IsZero() {
		return errors.New("invalid credential binding reference")
	}
	return nil
}

type ManagedResourceRef struct {
	ResourceID       string `json:"resource_id"`
	Provider         string `json:"provider"`
	Kind             string `json:"kind"`
	ExternalIdentity string `json:"external_identity"`
	LifecyclePolicy  string `json:"lifecycle_policy"`
}

func (r ManagedResourceRef) Validate() error {
	if !validReference(r.ResourceID) || strings.TrimSpace(r.Provider) == "" || strings.TrimSpace(r.Kind) == "" || !validReference(r.ExternalIdentity) || strings.TrimSpace(r.LifecyclePolicy) == "" {
		return errors.New("invalid managed resource reference")
	}
	return nil
}

type CapabilityBindingRef struct {
	BindingID string `json:"binding_id"`
	Kind      string `json:"kind"`
	Endpoint  string `json:"endpoint"`
	TokenRef  string `json:"token_ref"`
	BudgetID  string `json:"budget_id"`
}

func (r CapabilityBindingRef) Validate() error {
	if !validReference(r.BindingID) || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Endpoint) == "" || !validReference(r.TokenRef) || !validReference(r.BudgetID) {
		return errors.New("invalid capability binding reference")
	}
	return nil
}

type RecipeLock struct {
	RecipeID        string `json:"recipe_id"`
	Version         string `json:"version"`
	ContentDigest   string `json:"content_digest"`
	SignatureDigest string `json:"signature_digest"`
}

func (r RecipeLock) Validate() error {
	if r.RecipeID == "" || r.Version == "" || !digest.MatchString(r.ContentDigest) || !digest.MatchString(r.SignatureDigest) {
		return errors.New("invalid recipe lock")
	}
	return nil
}

type EnvironmentInputsSnapshot struct {
	SnapshotID         string                 `json:"snapshot_id"`
	ProjectID          string                 `json:"project_id"`
	Environment        string                 `json:"environment"`
	SecretVersions     []SecretVersionRef     `json:"secret_versions,omitempty"`
	CredentialBindings []CredentialBindingRef `json:"credential_bindings,omitempty"`
	Resources          []ManagedResourceRef   `json:"resources,omitempty"`
	Capabilities       []CapabilityBindingRef `json:"capabilities,omitempty"`
	Recipes            []RecipeLock           `json:"recipes,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	Digest             string                 `json:"digest"`
}

func (s EnvironmentInputsSnapshot) Validate() error {
	if s.SnapshotID == "" || s.ProjectID == "" || s.Environment == "" || s.CreatedAt.IsZero() || !digest.MatchString(s.Digest) {
		return errors.New("invalid environment inputs snapshot")
	}
	for _, secret := range s.SecretVersions {
		if err := secret.Validate(); err != nil {
			return err
		}
	}
	for _, credential := range s.CredentialBindings {
		if err := credential.Validate(); err != nil || !credential.ExpiresAt.After(s.CreatedAt) {
			if err == nil {
				err = errors.New("credential binding is expired at snapshot publication")
			}
			return err
		}
	}
	for _, resource := range s.Resources {
		if err := resource.Validate(); err != nil {
			return err
		}
	}
	for _, capability := range s.Capabilities {
		if err := capability.Validate(); err != nil {
			return err
		}
	}
	for _, recipe := range s.Recipes {
		if err := recipe.Validate(); err != nil {
			return err
		}
	}
	for _, check := range []error{
		uniqueSorted(s.SecretVersions, func(v SecretVersionRef) string { return v.Name }),
		uniqueSorted(s.CredentialBindings, func(v CredentialBindingRef) string { return v.BindingID }),
		uniqueSorted(s.Resources, func(v ManagedResourceRef) string { return v.ResourceID }),
		uniqueSorted(s.Capabilities, func(v CapabilityBindingRef) string { return v.BindingID }),
		uniqueSorted(s.Recipes, func(v RecipeLock) string { return v.RecipeID }),
	} {
		if check != nil {
			return check
		}
	}
	fingerprint, err := s.Fingerprint()
	if err != nil || fingerprint != s.Digest {
		return errors.New("environment inputs snapshot digest mismatch")
	}
	return nil
}

func (s EnvironmentInputsSnapshot) Fingerprint() (string, error) {
	copy := s
	copy.Digest = ""
	copy.SecretVersions = append([]SecretVersionRef(nil), s.SecretVersions...)
	copy.CredentialBindings = append([]CredentialBindingRef(nil), s.CredentialBindings...)
	copy.Resources = append([]ManagedResourceRef(nil), s.Resources...)
	copy.Capabilities = append([]CapabilityBindingRef(nil), s.Capabilities...)
	copy.Recipes = append([]RecipeLock(nil), s.Recipes...)
	sort.Slice(copy.SecretVersions, func(i, j int) bool { return copy.SecretVersions[i].Name < copy.SecretVersions[j].Name })
	sort.Slice(copy.CredentialBindings, func(i, j int) bool {
		return copy.CredentialBindings[i].BindingID < copy.CredentialBindings[j].BindingID
	})
	sort.Slice(copy.Resources, func(i, j int) bool { return copy.Resources[i].ResourceID < copy.Resources[j].ResourceID })
	sort.Slice(copy.Capabilities, func(i, j int) bool { return copy.Capabilities[i].BindingID < copy.Capabilities[j].BindingID })
	sort.Slice(copy.Recipes, func(i, j int) bool { return copy.Recipes[i].RecipeID < copy.Recipes[j].RecipeID })
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// DecodeEnvironmentInputsSnapshot is the strict API boundary. In particular,
// plaintext-style fields such as value, token or password are not accepted as
// forward-compatible extensions.
func DecodeEnvironmentInputsSnapshot(raw []byte) (EnvironmentInputsSnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot EnvironmentInputsSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return EnvironmentInputsSnapshot{}, errors.New("invalid environment inputs snapshot")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return EnvironmentInputsSnapshot{}, errors.New("environment inputs snapshot must be one JSON document")
	}
	if err := snapshot.Validate(); err != nil {
		return EnvironmentInputsSnapshot{}, err
	}
	return snapshot, nil
}

func validReference(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func uniqueSorted[T any](values []T, key func(T) string) error {
	previous := ""
	for index, value := range values {
		current := strings.TrimSpace(key(value))
		if current == "" || (index > 0 && current <= previous) {
			return errors.New("snapshot references must be unique and sorted")
		}
		previous = current
	}
	return nil
}
