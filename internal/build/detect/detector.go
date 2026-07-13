package detect

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

type Detector struct{}

type candidate struct {
	runtime, buildpack string
	entrypoint         []string
	evidence           string
}

func (Detector) Detect(ctx context.Context, sourcePath string, config domain.BuildConfig) (application.Detection, error) {
	if err := ctx.Err(); err != nil {
		return application.Detection{}, err
	}
	config, err := domain.NormalizeConfig(config)
	if err != nil {
		return application.Detection{}, err
	}
	root, err := scopedRoot(sourcePath, config.SourceRoot)
	if err != nil {
		return application.Detection{}, err
	}
	if config.Type == domain.BuildTypeDockerfile {
		path := config.DockerfilePath
		if path == "" {
			path = "Dockerfile"
		}
		if !regularFile(filepath.Join(root, filepath.FromSlash(path))) {
			return application.Detection{}, domain.NewError(domain.CodeUserFailure, "configured Dockerfile does not exist")
		}
		return application.Detection{Runtime: "dockerfile", Backend: application.BackendDockerfile, Evidence: []string{path}}, nil
	}
	if config.Runtime != "" {
		return explicit(config.Runtime, root)
	}
	var candidates []candidate
	if regularFile(filepath.Join(root, "go.mod")) {
		candidates = append(candidates, candidate{runtime: "go", buildpack: "paketo-buildpacks/go", entrypoint: []string{"/workspace/app"}, evidence: "go.mod"})
	}
	if regularFile(filepath.Join(root, "package.json")) {
		candidates = append(candidates, candidate{runtime: "nodejs", buildpack: "paketo-buildpacks/nodejs", entrypoint: []string{"node", "index.js"}, evidence: "package.json"})
	}
	if regularFile(filepath.Join(root, "pyproject.toml")) || regularFile(filepath.Join(root, "requirements.txt")) {
		evidence := "pyproject.toml"
		if !regularFile(filepath.Join(root, evidence)) {
			evidence = "requirements.txt"
		}
		candidates = append(candidates, candidate{runtime: "python", buildpack: "paketo-buildpacks/python", entrypoint: []string{"python3", "main.py"}, evidence: evidence})
	}
	if len(candidates) == 0 && regularFile(filepath.Join(root, "Dockerfile")) {
		return application.Detection{Runtime: "dockerfile", Backend: application.BackendDockerfile, Evidence: []string{"Dockerfile"}}, nil
	}
	if len(candidates) == 0 {
		return application.Detection{}, domain.NewError(domain.CodeUserFailure, "UNSUPPORTED_PROJECT: no supported runtime marker found")
	}
	if len(candidates) > 1 {
		evidence := make([]string, 0, len(candidates))
		for _, value := range candidates {
			evidence = append(evidence, value.evidence)
		}
		sort.Strings(evidence)
		return application.Detection{}, domain.NewError(domain.CodeUserFailure, "AMBIGUOUS_PROJECT: explicit runtime or source root required: "+strings.Join(evidence, ","))
	}
	value := candidates[0]
	return application.Detection{Runtime: value.runtime, Backend: application.BackendBuildpacks, BuildpackID: value.buildpack, Entrypoint: value.entrypoint, Evidence: []string{value.evidence}}, nil
}

func explicit(runtime string, root string) (application.Detection, error) {
	switch strings.ToLower(runtime) {
	case "go", "golang":
		if !regularFile(filepath.Join(root, "go.mod")) {
			return application.Detection{}, domain.NewError(domain.CodeUserFailure, "go.mod not found for explicit Go runtime")
		}
		return application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo-buildpacks/go", Entrypoint: []string{"/workspace/app"}, Evidence: []string{"go.mod", "explicit-runtime"}}, nil
	case "node", "nodejs":
		if !regularFile(filepath.Join(root, "package.json")) {
			return application.Detection{}, domain.NewError(domain.CodeUserFailure, "package.json not found for explicit Node.js runtime")
		}
		return application.Detection{Runtime: "nodejs", Backend: application.BackendBuildpacks, BuildpackID: "paketo-buildpacks/nodejs", Entrypoint: []string{"node", "index.js"}, Evidence: []string{"package.json", "explicit-runtime"}}, nil
	case "python", "python3":
		if !regularFile(filepath.Join(root, "pyproject.toml")) && !regularFile(filepath.Join(root, "requirements.txt")) {
			return application.Detection{}, domain.NewError(domain.CodeUserFailure, "Python project marker not found")
		}
		return application.Detection{Runtime: "python", Backend: application.BackendBuildpacks, BuildpackID: "paketo-buildpacks/python", Entrypoint: []string{"python3", "main.py"}, Evidence: []string{"explicit-runtime"}}, nil
	default:
		return application.Detection{}, domain.NewError(domain.CodeUserFailure, "UNSUPPORTED_RUNTIME: "+runtime)
	}
}
func scopedRoot(source, relative string) (string, error) {
	base, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	root := base
	if relative != "" {
		root = filepath.Join(base, filepath.FromSlash(relative))
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", domain.NewError(domain.CodeInvalidArgument, "source root escapes checkout")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", domain.NewError(domain.CodeUserFailure, "source root not found")
	}
	return root, nil
}
func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

var _ application.Detector = Detector{}
