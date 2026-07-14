package workspaceagent

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var planDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateVerifiedApplyArguments(arguments []string) (string, string, error) {
	if len(arguments) != 2 || !planDigestPattern.MatchString(arguments[0]) {
		return "", "", errors.New("exact plan digest and path are required")
	}
	planPath := strings.TrimSpace(arguments[1])
	clean := filepath.Clean(planPath)
	if planPath == "" || clean == "." || clean != planPath || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", errors.New("plan path must be canonical and relative")
	}
	return strings.TrimPrefix(arguments[0], "sha256:"), clean, nil
}
