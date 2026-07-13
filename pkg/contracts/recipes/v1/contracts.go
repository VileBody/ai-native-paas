// Package v1 defines signed provider-neutral recipe contracts.
package v1

import (
	"errors"
	"regexp"
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

type RecipeVersion struct {
	RecipeID        string                `json:"recipe_id"`
	Version         string                `json:"version"`
	Kind            string                `json:"kind"`
	Driver          string                `json:"driver"`
	ContentDigest   string                `json:"content_digest"`
	SignatureDigest string                `json:"signature_digest"`
	SchemaDigest    string                `json:"schema_digest"`
	Capabilities    LifecycleCapabilities `json:"capabilities"`
}

func (r RecipeVersion) Validate() error {
	if strings.TrimSpace(r.RecipeID) == "" || strings.TrimSpace(r.Version) == "" || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Driver) == "" || !digest.MatchString(r.ContentDigest) || !digest.MatchString(r.SignatureDigest) || !digest.MatchString(r.SchemaDigest) || !r.Capabilities.Plan || !r.Capabilities.Apply || !r.Capabilities.Discover {
		return errors.New("invalid signed recipe")
	}
	return nil
}

type RecipeInvocation struct {
	RecipeID       string         `json:"recipe_id"`
	Version        string         `json:"version"`
	Action         string         `json:"action"`
	Parameters     map[string]any `json:"parameters"`
	Target         string         `json:"target"`
	IdempotencyKey string         `json:"idempotency_key"`
}
