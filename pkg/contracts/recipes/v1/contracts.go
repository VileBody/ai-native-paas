// Package v1 defines signed provider-neutral recipe contracts.
package v1

import (
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
)

const APIVersion = "recipes.platform.example.com/v1"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type LifecycleCapabilities struct {
	Plan     bool `json:"plan"`
	Apply    bool `json:"apply"`
	Discover bool `json:"discover"`
	Update   bool `json:"update"`
	Retain   bool `json:"retain"`
	Purge    bool `json:"purge"`
	Backup   bool `json:"backup"`
	Restore  bool `json:"restore"`
}

type ArtifactPin struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

func (a ArtifactPin) Validate() error {
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Kind) == "" || strings.TrimSpace(a.Reference) == "" || !digest.MatchString(a.Digest) || strings.Contains(strings.ToLower(a.Reference), ":latest") {
		return errors.New("invalid recipe artifact pin")
	}
	return nil
}

type PermissionScope string

const (
	PermissionNamespaced PermissionScope = "NAMESPACED"
	PermissionCluster    PermissionScope = "CLUSTER"
)

type ResourcePermission struct {
	APIGroup string          `json:"api_group"`
	Kind     string          `json:"kind"`
	Scope    PermissionScope `json:"scope"`
}

func (p ResourcePermission) Validate() error {
	if strings.TrimSpace(p.Kind) == "" || (p.Scope != PermissionNamespaced && p.Scope != PermissionCluster) {
		return errors.New("invalid recipe resource permission")
	}
	return nil
}

type LifecycleProcedures struct {
	HealthCheck string `json:"health_check"`
	Backup      string `json:"backup"`
	Restore     string `json:"restore"`
	Upgrade     string `json:"upgrade"`
	Removal     string `json:"removal"`
	Retention   string `json:"retention"`
}

func (p LifecycleProcedures) Complete() bool {
	return strings.TrimSpace(p.HealthCheck) != "" && strings.TrimSpace(p.Backup) != "" && strings.TrimSpace(p.Restore) != "" && strings.TrimSpace(p.Upgrade) != "" && strings.TrimSpace(p.Removal) != "" && strings.TrimSpace(p.Retention) != ""
}

type RecipeVersion struct {
	RecipeID        string                `json:"recipe_id"`
	Version         string                `json:"version"`
	Kind            string                `json:"kind"`
	Driver          string                `json:"driver"`
	ContentDigest   string                `json:"content_digest"`
	SignatureDigest string                `json:"signature_digest"`
	SchemaDigest    string                `json:"schema_digest"`
	Capabilities    LifecycleCapabilities `json:"capabilities"`
	Stateful        bool                  `json:"stateful"`
	Artifacts       []ArtifactPin         `json:"artifacts"`
	Permissions     []ResourcePermission  `json:"permissions"`
	Procedures      LifecycleProcedures   `json:"procedures"`
	SigningKeyID    string                `json:"signing_key_id"`
	Signature       string                `json:"signature"`
}

func (r RecipeVersion) Validate() error {
	if strings.TrimSpace(r.RecipeID) == "" || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(r.Version) || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Driver) == "" || !digest.MatchString(r.ContentDigest) || !digest.MatchString(r.SignatureDigest) || !digest.MatchString(r.SchemaDigest) || !r.Capabilities.Plan || !r.Capabilities.Apply || !r.Capabilities.Discover || len(r.Artifacts) == 0 || len(r.Permissions) == 0 || strings.TrimSpace(r.SigningKeyID) == "" {
		return errors.New("invalid signed recipe")
	}
	if signature, err := base64.RawStdEncoding.DecodeString(r.Signature); err != nil || len(signature) != 64 {
		return errors.New("invalid recipe signature encoding")
	}
	if r.Stateful && !r.Procedures.Complete() {
		return errors.New("stateful recipe lifecycle procedures are incomplete")
	}
	for _, artifact := range r.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
	}
	for _, permission := range r.Permissions {
		if err := permission.Validate(); err != nil {
			return err
		}
	}
	if !sortedUnique(r.Artifacts, func(v ArtifactPin) string { return v.Name }) || !sortedUnique(r.Permissions, func(v ResourcePermission) string { return v.APIGroup + "/" + v.Kind + "/" + string(v.Scope) }) {
		return errors.New("recipe artifacts and permissions must be unique and sorted")
	}
	return nil
}

type RecipeLock struct {
	RecipeID        string        `json:"recipe_id"`
	Version         string        `json:"version"`
	ContentDigest   string        `json:"content_digest"`
	SignatureDigest string        `json:"signature_digest"`
	Artifacts       []ArtifactPin `json:"artifacts"`
}

func (l RecipeLock) Validate() error {
	if strings.TrimSpace(l.RecipeID) == "" || strings.TrimSpace(l.Version) == "" || !digest.MatchString(l.ContentDigest) || !digest.MatchString(l.SignatureDigest) || len(l.Artifacts) == 0 {
		return errors.New("invalid recipe lock")
	}
	for _, artifact := range l.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func sortedUnique[T any](values []T, key func(T) string) bool {
	keys := make([]string, len(values))
	for index, value := range values {
		keys[index] = strings.TrimSpace(key(value))
		if keys[index] == "" || (index > 0 && keys[index] <= keys[index-1]) {
			return false
		}
	}
	return sort.StringsAreSorted(keys)
}

type RecipeInvocation struct {
	RecipeID       string         `json:"recipe_id"`
	Version        string         `json:"version"`
	Action         string         `json:"action"`
	Parameters     map[string]any `json:"parameters"`
	Target         string         `json:"target"`
	IdempotencyKey string         `json:"idempotency_key"`
}
