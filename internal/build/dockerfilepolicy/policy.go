// Package dockerfilepolicy validates immutable base-image references before an
// untrusted Dockerfile is handed to the disposable execution backend.
package dockerfilepolicy

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

const maxDockerfileBytes = 1 << 20

func Validate(contextRoot, definitionPath string) error {
	contextRoot = filepath.Clean(contextRoot)
	definitionPath = filepath.FromSlash(strings.TrimSpace(definitionPath))
	if contextRoot == "." || !filepath.IsAbs(contextRoot) || definitionPath == "" || filepath.IsAbs(definitionPath) {
		return domain.NewError(domain.CodeInvalidArgument, "invalid Dockerfile policy path")
	}
	target := filepath.Join(contextRoot, definitionPath)
	relative, err := filepath.Rel(contextRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile escapes build context")
	}
	resolvedRoot, err := filepath.EvalSymlinks(contextRoot)
	if err != nil {
		return domain.Wrap(domain.CodeUserFailure, "resolve build context", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return domain.Wrap(domain.CodeUserFailure, "resolve Dockerfile", err)
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) || containsSymlink(contextRoot, relative) {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile symlink is not allowed")
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDockerfileBytes {
		return domain.NewError(domain.CodeUserFailure, "Dockerfile is missing, empty or oversized")
	}
	raw, err := os.ReadFile(resolvedTarget)
	if err != nil {
		return domain.Wrap(domain.CodeUserFailure, "read Dockerfile", err)
	}
	return validateInstructions(string(raw))
}

func validateInstructions(document string) error {
	scanner := bufio.NewScanner(strings.NewReader(document))
	scanner.Buffer(make([]byte, 64<<10), maxDockerfileBytes)
	stages := map[string]struct{}{}
	foundFrom := false
	logical := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if logical != "" {
			line = logical + " " + line
			logical = ""
		}
		if strings.HasSuffix(line, "\\") {
			logical = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
			continue
		}
		if err := validateInstruction(line, stages, &foundFrom); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return domain.Wrap(domain.CodeUserFailure, "parse Dockerfile", err)
	}
	if logical != "" {
		if err := validateInstruction(logical, stages, &foundFrom); err != nil {
			return err
		}
	}
	if !foundFrom {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile has no immutable FROM instruction")
	}
	return nil
}

func validateInstruction(line string, stages map[string]struct{}, foundFrom *bool) error {
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
		return nil
	}
	*foundFrom = true
	index := 1
	for index < len(fields) && strings.HasPrefix(fields[index], "--") {
		index++
	}
	if index >= len(fields) {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile FROM has no base")
	}
	base := fields[index]
	if strings.Contains(base, "$") {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile base ARG must be resolved to an immutable digest")
	}
	_, stageReference := stages[strings.ToLower(base)]
	if !strings.EqualFold(base, "scratch") && !stageReference && !immutableReference(base) {
		return domain.NewError(domain.CodePolicyRejected, "Dockerfile external base must use an immutable sha256 digest")
	}
	if index+2 < len(fields) && strings.EqualFold(fields[index+1], "AS") {
		alias := strings.ToLower(strings.TrimSpace(fields[index+2]))
		if alias == "" || strings.ContainsAny(alias, "/:@$") {
			return domain.NewError(domain.CodePolicyRejected, "Dockerfile stage alias is invalid")
		}
		stages[alias] = struct{}{}
	}
	return nil
}

func immutableReference(reference string) bool {
	at := strings.LastIndex(reference, "@")
	if at <= 0 || at == len(reference)-1 {
		return false
	}
	digest := reference[at+1:]
	return buildv1.ValidDigest(digest) && !strings.ContainsAny(reference[:at], " \t\r\n")
}

func containsSymlink(root, relative string) bool {
	current := root
	for _, segment := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
