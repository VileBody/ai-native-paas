package architecture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecture_CommerceDoesNotImportOtherDomainInternals(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := []string{"/internal/kernel/", "/internal/source/", "/internal/build/", "/internal/runtime/", "/internal/attachments/", "/internal/agent/"}
	_ = filepath.WalkDir(filepath.Join(root, "internal", "commerce"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imp := strings.Trim(spec.Path.Value, "\"")
			for _, fragment := range forbidden {
				if strings.Contains(imp, fragment) {
					t.Errorf("%s imports %s", path, imp)
				}
			}
		}
		return nil
	})
}
func TestArchitecture_CommerceSQLHasNoCrossSchemaReferences(t *testing.T) {
	root := repositoryRoot(t)
	_ = filepath.WalkDir(filepath.Join(root, "migrations", "commerce"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(string(raw))
		for _, schema := range []string{"KERNEL.", "SOURCE.", "BUILD.", "RUNTIME.", "ATTACHMENTS.", "AGENT."} {
			if strings.Contains(upper, "REFERENCES "+schema) || strings.Contains(upper, "JOIN "+schema) {
				t.Errorf("%s references foreign schema %s", path, schema)
			}
		}
		return nil
	})
}
func TestArchitecture_CommerceContractsContainNoProviderTypes(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "pkg", "contracts", "commerce", "v1", "contracts.go"))
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	for _, token := range []string{"stripe", "paddle", "kubernetes", "gitlab", "cozystack", "openbao"} {
		if strings.Contains(lower, token) {
			t.Errorf("public contract leaks %s", token)
		}
	}
}
