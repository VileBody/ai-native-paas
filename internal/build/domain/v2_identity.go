package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
)

// ComputeBuildV2Identity binds the tenant-scoped source identity to the exact
// commit and the only canonical representation of the explicit build spec.
func ComputeBuildV2Identity(projectID, repositoryID string, spec buildv2.BuildSpec) (string, error) {
	projectID = strings.TrimSpace(projectID)
	repositoryID = strings.TrimSpace(repositoryID)
	if projectID == "" || repositoryID == "" || len(projectID) > 256 || len(repositoryID) > 256 || strings.ContainsAny(projectID+repositoryID, "\x00\r\n") {
		return "", NewError(CodeInvalidArgument, "invalid v2 build source identity")
	}
	canonical, err := spec.Canonical()
	if err != nil {
		return "", NewError(CodeInvalidArgument, "invalid canonical build specification")
	}
	raw, err := json.Marshal(struct {
		APIVersion   string            `json:"api_version"`
		ProjectID    string            `json:"project_id"`
		RepositoryID string            `json:"repository_id"`
		Spec         buildv2.BuildSpec `json:"spec"`
	}{APIVersion: buildv2.APIVersion, ProjectID: projectID, RepositoryID: repositoryID, Spec: canonical})
	if err != nil {
		return "", Wrap(CodePlatformFailure, "encode v2 build identity", err)
	}
	sum := sha256.Sum256(raw)
	return "bldidv2_" + hex.EncodeToString(sum[:]), nil
}
