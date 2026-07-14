package workspaceagent

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	planDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitSHAPattern  = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

func validateVerifiedApplyArguments(arguments []string) (string, string, error) {
	if len(arguments) != 2 || !planDigestPattern.MatchString(arguments[0]) {
		return "", "", errors.New("exact plan digest and path are required")
	}
	planPath, err := validateReceiptPlanPath(arguments[1])
	if err != nil {
		return "", "", err
	}
	return strings.TrimPrefix(arguments[0], "sha256:"), planPath, nil
}

func validateReceiptPlanPath(value string) (string, error) {
	planPath := strings.TrimSpace(value)
	clean := filepath.Clean(planPath)
	if planPath == "" || clean == "." || clean != planPath || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("plan path must be canonical and relative")
	}
	return clean, nil
}

func validateVerifiedPlanArguments(arguments []string) (string, string, error) {
	if len(arguments) != 2 || !commitSHAPattern.MatchString(arguments[1]) {
		return "", "", errors.New("canonical plan path and exact source revision are required")
	}
	planPath, err := validateReceiptPlanPath(arguments[0])
	if err != nil {
		return "", "", err
	}
	return planPath, arguments[1], nil
}
