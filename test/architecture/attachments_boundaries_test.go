package architecture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func TestArchitecture_AttachmentsDoesNotImportOtherDomainInternals(t *testing.T) {
	root := repositoryRootFromCWD(t)
	forbidden := []string{"/internal/kernel/", "/internal/source/", "/internal/build/", "/internal/runtime/", "/internal/commerce/", "/internal/agent/"}
	_ = filepath.WalkDir(filepath.Join(root, "internal", "attachments"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value := strings.Trim(spec.Path.Value, `"`)
			for _, fragment := range forbidden {
				if strings.Contains(value, fragment) {
					t.Errorf("%s imports forbidden domain internal %s", path, value)
				}
			}
		}
		return nil
	})
}

func TestArchitecture_AttachmentsSQLHasNoCrossDomainReferences(t *testing.T) {
	root := repositoryRootFromCWD(t)
	for _, directory := range []string{filepath.Join(root, "migrations", "attachments"), filepath.Join(root, "internal", "attachments", "postgres", "migrations")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".sql") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			upper := strings.ToUpper(string(raw))
			for _, schema := range []string{"KERNEL.", "SOURCE.", "BUILD.", "RUNTIME.", "COMMERCE.", "AGENT."} {
				if strings.Contains(upper, "REFERENCES "+schema) || strings.Contains(upper, "JOIN "+schema) {
					t.Errorf("%s crosses domain boundary to %s", path, schema)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchitecture_AttachmentSnapshotContractHasNoSecretValueFields(t *testing.T) {
	typeOf := reflect.TypeOf(attachmentsv1.AttachmentSnapshot{})
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		name := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
		for _, forbidden := range []string{"password", "token", "credential_value", "secret_value", "private_key", "database_url"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("forbidden secret-bearing snapshot field %s", field.Name)
			}
		}
	}
}

func TestArchitecture_AttachmentsMigrationsHaveNoSecretValueColumns(t *testing.T) {
	root := repositoryRootFromCWD(t)
	raw, err := os.ReadFile(filepath.Join(root, "migrations", "attachments", "001_attachments.sql"))
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"secret_value", "plaintext", "password text", "token text", "private_key", "database_url text"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("attachments schema can persist secret material: %q", forbidden)
		}
	}
}

func TestArchitecture_ProductionDoesNotUseUnboundBuildSecretResolver(t *testing.T) {
	root := repositoryRootFromCWD(t)
	legacyCall := ".ResolveBuildSecretRefs("
	for _, directory := range []string{"adapters", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if path == filepath.Join(root, "internal", "attachments", "application", "service.go") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(raw), legacyCall) {
				t.Errorf("%s uses the project-unbound v1 build secret resolver", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
