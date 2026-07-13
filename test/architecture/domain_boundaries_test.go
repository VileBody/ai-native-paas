package architecture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecture_KernelDoesNotImportSourceInternals(t *testing.T) {
	root := repositoryRootFromCWD(t)
	walkGo(t, filepath.Join(root, "internal", "kernel"), func(path, imp string) {
		if strings.Contains(imp, "/internal/source/") || strings.HasSuffix(imp, "/internal/source") {
			t.Errorf("%s imports later-domain internals %s", path, imp)
		}
	})
}

func TestArchitecture_SourceDoesNotImportKernelPersistenceOrOtherDomains(t *testing.T) {
	root := repositoryRootFromCWD(t)
	forbidden := []string{"/internal/kernel/postgres", "/internal/kernel/memory", "/internal/build/", "/internal/runtime/", "/internal/attachments/", "/internal/commerce/", "/internal/agent/"}
	walkGo(t, filepath.Join(root, "internal", "source"), func(path, imp string) {
		for _, fragment := range forbidden {
			if strings.Contains(imp, fragment) {
				t.Errorf("%s imports forbidden package %s", path, imp)
			}
		}
	})
}

func TestArchitecture_SourceSQLHasNoCrossDomainReferences(t *testing.T) {
	root := repositoryRootFromCWD(t)
	err := filepath.WalkDir(filepath.Join(root, "migrations", "source"), func(path string, d os.DirEntry, err error) error {
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
		for _, schema := range []string{"KERNEL.", "BUILD.", "RUNTIME.", "ATTACHMENTS.", "COMMERCE.", "AGENT."} {
			if strings.Contains(upper, "REFERENCES "+schema) {
				t.Errorf("%s contains cross-domain foreign key to %s", path, schema)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchitecture_BuildDoesNotImportSourceInternalsOrLaterDomains(t *testing.T) {
	root := repositoryRootFromCWD(t)
	forbidden := []string{"/internal/source/", "/internal/kernel/", "/internal/runtime/", "/internal/attachments/", "/internal/commerce/", "/internal/agent/"}
	walkGo(t, filepath.Join(root, "internal", "build"), func(path, imp string) {
		for _, fragment := range forbidden {
			if strings.Contains(imp, fragment) {
				t.Errorf("%s imports forbidden package %s", path, imp)
			}
		}
	})
}

func TestArchitecture_BuildSQLHasNoCrossDomainReferences(t *testing.T) {
	root := repositoryRootFromCWD(t)
	err := filepath.WalkDir(filepath.Join(root, "migrations", "build"), func(path string, d os.DirEntry, err error) error {
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
		for _, schema := range []string{"KERNEL.", "SOURCE.", "RUNTIME.", "ATTACHMENTS.", "COMMERCE.", "AGENT."} {
			if strings.Contains(upper, "REFERENCES "+schema) {
				t.Errorf("%s contains cross-domain foreign key to %s", path, schema)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func walkGo(t *testing.T, root string, check func(string, string)) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			check(path, strings.Trim(spec.Path.Value, `"`))
		}
		return nil
	})
}

func repositoryRootFromCWD(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestArchitecture_RuntimeDoesNotImportPreviousInternalsOrLaterDomains(t *testing.T) {
	root := repositoryRootFromCWD(t)
	forbidden := []string{"/internal/kernel/", "/internal/source/", "/internal/build/", "/internal/attachments/", "/internal/commerce/", "/internal/agent/"}
	walkGo(t, filepath.Join(root, "internal", "runtime"), func(path, imp string) {
		for _, fragment := range forbidden {
			if strings.Contains(imp, fragment) {
				t.Errorf("%s imports forbidden package %s", path, imp)
			}
		}
	})
}

func TestArchitecture_RuntimeSQLHasNoCrossDomainReferences(t *testing.T) {
	root := repositoryRootFromCWD(t)
	for _, directory := range []string{filepath.Join(root, "migrations", "runtime"), filepath.Join(root, "internal", "runtime", "postgres", "migrations")} {
		err := filepath.WalkDir(directory, func(path string, d os.DirEntry, err error) error {
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
			for _, schema := range []string{"KERNEL.", "SOURCE.", "BUILD.", "ATTACHMENTS.", "COMMERCE.", "AGENT."} {
				if strings.Contains(upper, "REFERENCES "+schema) {
					t.Errorf("%s contains cross-domain foreign key to %s", path, schema)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
