// Package v2 separates secrets, credentials, recipes, provider resources and capabilities.
package v2

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

type ManagedResourceRef struct {
	ResourceID       string `json:"resource_id"`
	Provider         string `json:"provider"`
	Kind             string `json:"kind"`
	ExternalIdentity string `json:"external_identity"`
	LifecyclePolicy  string `json:"lifecycle_policy"`
}

type CapabilityBindingRef struct {
	BindingID string `json:"binding_id"`
	Kind      string `json:"kind"`
	Endpoint  string `json:"endpoint"`
	TokenRef  string `json:"token_ref"`
	BudgetID  string `json:"budget_id"`
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
	for _, recipe := range s.Recipes {
		if err := recipe.Validate(); err != nil {
			return err
		}
	}
	return uniqueSorted(s.SecretVersions, func(v SecretVersionRef) string { return v.Name })
}

func (s EnvironmentInputsSnapshot) Fingerprint() (string, error) {
	copy := s
	copy.Digest = ""
	copy.SecretVersions = append([]SecretVersionRef(nil), s.SecretVersions...)
	sort.Slice(copy.SecretVersions, func(i, j int) bool { return copy.SecretVersions[i].Name < copy.SecretVersions[j].Name })
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
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
