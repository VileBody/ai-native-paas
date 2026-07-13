package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

const MediaType = "application/spdx+json"

type Generator struct{}

type fileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (Generator) Generate(ctx context.Context, snapshot application.SourceSnapshot, artifact application.PublishedArtifact) (application.SBOMResult, error) {
	var files []fileRecord
	err := filepath.WalkDir(snapshot.Path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return domain.NewError(domain.CodeUserFailure, "SBOM source contains non-regular file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		relative, err := filepath.Rel(snapshot.Path, path)
		if err != nil {
			return err
		}
		files = append(files, fileRecord{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:]), Size: info.Size()})
		return nil
	})
	if err != nil {
		return application.SBOMResult{}, domain.Wrap(domain.CodePlatformFailure, "generate SBOM", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	document := map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              "build-" + snapshot.Revision.ProjectID,
		"documentNamespace": "urn:paas:sbom:" + strings.TrimPrefix(artifact.Digest, "sha256:"),
		"artifactDigest":    artifact.Digest,
		"sourceRevision":    snapshot.Revision.CommitSHA,
		"files":             files,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return application.SBOMResult{}, err
	}
	sum := sha256.Sum256(raw)
	return application.SBOMResult{Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: MediaType, Document: raw}, nil
}

var _ application.SBOMGenerator = Generator{}
