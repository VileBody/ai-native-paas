package domain_test

import (
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func revision(sha string) sourcev1.SourceRevision {
	return sourcev1.SourceRevision{ProjectID: "prj-1", RepositoryID: "repo-1", Branch: "main", CommitSHA: sha}
}
func digest(ch string) string { return "sha256:" + repeat(ch, 64) }
func repeat(v string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += v
	}
	return out
}

func TestBuildIdentity_SameInputsProduceSameIdentity(t *testing.T) {
	configA := domain.BuildConfig{Type: domain.BuildTypeAuto, BuildEnv: map[string]string{"B": "2", "A": "1"}, BuildSecretRef: []string{"npm", "pip", "npm"}}
	configB := domain.BuildConfig{Type: domain.BuildTypeAuto, BuildEnv: map[string]string{"A": "1", "B": "2"}, BuildSecretRef: []string{"pip", "npm"}}
	a, err := domain.ComputeBuildIdentity(revision(repeat("a", 40)), configA, digest("b"), digest("c"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.ComputeBuildIdentity(revision(repeat("a", 40)), configB, digest("b"), digest("c"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("a=%s b=%s", a, b)
	}
}
func TestBuildIdentity_CommitChangeProducesNewIdentity(t *testing.T) {
	a, _ := domain.ComputeBuildIdentity(revision(repeat("a", 40)), domain.BuildConfig{}, digest("b"), digest("c"), "v1")
	b, _ := domain.ComputeBuildIdentity(revision(repeat("d", 40)), domain.BuildConfig{}, digest("b"), digest("c"), "v1")
	if a == b {
		t.Fatal("commit change did not change identity")
	}
}
func TestBuildIdentity_BuilderDigestChangeProducesNewIdentity(t *testing.T) {
	a, _ := domain.ComputeBuildIdentity(revision(repeat("a", 40)), domain.BuildConfig{}, digest("b"), digest("c"), "v1")
	b, _ := domain.ComputeBuildIdentity(revision(repeat("a", 40)), domain.BuildConfig{}, digest("d"), digest("c"), "v1")
	if a == b {
		t.Fatal("builder digest change did not change identity")
	}
}
func TestBuildConfig_RejectsSecretValuesInPlainEnvironment(t *testing.T) {
	_, err := domain.NormalizeConfig(domain.BuildConfig{BuildEnv: map[string]string{"API_TOKEN": "secret"}})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildConfig_RejectsTraversalBeforeCleaning(t *testing.T) {
	for _, value := range []string{"../secret", "a/../../secret", `..\\secret`, "/absolute", `C:\\absolute`} {
		_, err := domain.NormalizeConfig(domain.BuildConfig{SourceRoot: value})
		if !domain.HasCode(err, domain.CodeInvalidArgument) {
			t.Errorf("source root %q err=%v", value, err)
		}
	}
}

func TestBuildConfig_ClonesMutableInputs(t *testing.T) {
	env := map[string]string{"MODE": "release"}
	command := []string{"go", "build"}
	refs := []string{"registry"}
	normalized, err := domain.NormalizeConfig(domain.BuildConfig{BuildEnv: env, BuildCommand: command, BuildSecretRef: refs})
	if err != nil {
		t.Fatal(err)
	}
	env["MODE"] = "tampered"
	command[0] = "rm"
	refs[0] = "tampered"
	if normalized.BuildEnv["MODE"] != "release" || normalized.BuildCommand[0] != "go" || normalized.BuildSecretRef[0] != "registry" {
		t.Fatalf("normalized config aliases caller memory: %+v", normalized)
	}
}

func TestBuildConfig_RejectsInvalidEnvironmentKeyAndNUL(t *testing.T) {
	for _, config := range []domain.BuildConfig{
		{BuildEnv: map[string]string{" BAD ": "x"}},
		{BuildEnv: map[string]string{"BAD-KEY": "x"}},
		{BuildEnv: map[string]string{"GOOD": "x\x00y"}},
	} {
		if _, err := domain.NormalizeConfig(config); !domain.HasCode(err, domain.CodeInvalidArgument) {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	}
}

func FuzzBuildConfig_PathNormalizationCannotEscape(f *testing.F) {
	for _, seed := range []string{"", ".", "src", "../x", "a/../../b", `..\\x`, "/tmp/x", `C:\\x`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		config, err := domain.NormalizeConfig(domain.BuildConfig{SourceRoot: value})
		if err != nil {
			return
		}
		if strings.HasPrefix(config.SourceRoot, "/") || config.SourceRoot == ".." || strings.HasPrefix(config.SourceRoot, "../") || strings.Contains(config.SourceRoot, "\\") {
			t.Fatalf("unsafe normalized root %q from %q", config.SourceRoot, value)
		}
	})
}

func TestBuildInput_SourceRootIsCanonicalAndCannotConflict(t *testing.T) {
	source := revision(repeat("a", 40))
	source.SourceRoot = "apps/api"
	normalizedSource, normalizedConfig, err := domain.NormalizeBuildInput(source, domain.BuildConfig{SourceRoot: "apps/api"})
	if err != nil {
		t.Fatal(err)
	}
	if normalizedSource.SourceRoot != "apps/api" || normalizedConfig.SourceRoot != "" {
		t.Fatalf("source=%+v config=%+v", normalizedSource, normalizedConfig)
	}
	_, _, err = domain.NormalizeBuildInput(source, domain.BuildConfig{SourceRoot: "apps/web"})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("conflicting roots err=%v", err)
	}
}
