package v1_test

import (
	"testing"

	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func TestSourceRevision_ValidImmutableCommit(t *testing.T) {
	r := sourcev1.SourceRevision{ProjectID: "p", RepositoryID: "r", Branch: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceRoot: "apps/api"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestSourceRevision_RejectsMutableOrMissingRevision(t *testing.T) {
	for _, r := range []sourcev1.SourceRevision{{ProjectID: "p", RepositoryID: "r", Branch: "main", CommitSHA: "latest"}, {RepositoryID: "r", Branch: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {ProjectID: "p", RepositoryID: "r", Branch: "main", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceRoot: "../other"}} {
		if err := r.Validate(); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
