package architecture_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var boundedContexts = []string{"kernel", "source", "build", "runtime", "attachments", "commerce", "agent"}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestArchitecture_NoCrossDomainInternalImports(t *testing.T) {
	root := repositoryRoot(t)
	importPattern := regexp.MustCompile(`github\.com/keir-research/ai-native-paas/internal/([a-z0-9_-]+)`)
	for _, owner := range boundedContexts {
		directory := filepath.Join(root, "internal", owner)
		info, err := os.Stat(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() {
			t.Fatalf("stat %s: %v", directory, err)
		}
		err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "support.go" {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, match := range importPattern.FindAllStringSubmatch(string(content), -1) {
				if match[1] != owner {
					t.Errorf("%s domain imports internal %s package in %s", owner, match[1], path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchitecture_NoCrossSchemaSQLReferences(t *testing.T) {
	root := repositoryRoot(t)
	kernelAdapter := filepath.Join(root, "adapters", "postgres", "kernel")
	forbidden := []string{"source.", "build.", "runtime.", "attachments.", "commerce.", "agent."}
	err := filepath.WalkDir(kernelAdapter, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".sql")) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(content))
		for _, schema := range forbidden {
			if strings.Contains(lower, schema) {
				t.Errorf("kernel adapter references foreign schema %q in %s", schema, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchitecture_NoDomainsBeyondCurrentIterationExist(t *testing.T) {
	root := repositoryRoot(t)
	current := map[string]bool{"kernel": true, "source": true, "build": true, "runtime": true, "attachments": true, "commerce": true, "agent": true}
	for _, owner := range boundedContexts {
		if current[owner] {
			continue
		}
		directory := filepath.Join(root, "internal", owner)
		if _, err := os.Stat(directory); os.IsNotExist(err) {
			continue
		}
		t.Errorf("future bounded context %s exists before its iteration", owner)
	}
}

func TestArchitecture_DomainUsesInjectedClock(t *testing.T) {
	root := repositoryRoot(t)
	directory := filepath.Join(root, "internal", "kernel")
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "runtime.go" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			if strings.Contains(scanner.Text(), "time.Now(") {
				t.Errorf("direct wall clock access in %s:%d", path, line)
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchitecture_MigrationsContainNoDestructiveStatements(t *testing.T) {
	root := repositoryRoot(t)
	directory := filepath.Join(root, "adapters", "postgres", "kernel", "migrations")
	destructive := regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|SCHEMA|COLUMN)|TRUNCATE|DELETE\s+FROM)\b`)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if match := destructive.Find(content); match != nil {
			t.Errorf("destructive migration statement %q found in %s", match, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchitecture_BuildDomainUsesInjectedClock(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{
		filepath.Join(root, "internal", "build", "domain"),
		filepath.Join(root, "internal", "build", "application"),
	} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "time.Now(") {
				t.Errorf("direct wall clock access in build domain/application: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchitecture_BuildMigrationsContainNoDestructiveStatements(t *testing.T) {
	root := repositoryRoot(t)
	destructive := regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|SCHEMA|COLUMN)|TRUNCATE|DELETE\s+FROM)\b`)
	for _, directory := range []string{
		filepath.Join(root, "migrations", "build"),
		filepath.Join(root, "internal", "build", "postgres", "migrations"),
	} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if match := destructive.Find(content); match != nil {
				t.Errorf("destructive build migration statement %q found in %s", match, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchitecture_RuntimeDomainUsesInjectedClock(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{filepath.Join(root, "internal", "runtime", "domain"), filepath.Join(root, "internal", "runtime", "application"), filepath.Join(root, "internal", "runtime", "operator")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "support.go" {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(content), "time.Now(") {
				t.Errorf("direct wall clock access in runtime domain/application/operator: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchitecture_RuntimeMigrationsContainNoDestructiveStatements(t *testing.T) {
	root := repositoryRoot(t)
	destructive := regexp.MustCompile(`(?i)\b(DROP\s+(TABLE|SCHEMA|COLUMN)|TRUNCATE|DELETE\s+FROM)\b`)
	for _, directory := range []string{filepath.Join(root, "migrations", "runtime"), filepath.Join(root, "internal", "runtime", "postgres", "migrations")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if match := destructive.Find(content); match != nil {
				t.Errorf("destructive runtime migration statement %q found in %s", match, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
