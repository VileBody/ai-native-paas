//go:build !linux

package workspaceagent

import "errors"

func ExecuteVerifiedTofuApply(arguments []string) error {
	if _, _, err := validateVerifiedApplyArguments(arguments); err != nil {
		return err
	}
	return errors.New("verified OpenTofu apply is supported only by the Linux workspace image")
}
