//go:build !linux

package workspaceagent

import "errors"

func ExecuteVerifiedTofuPlan(arguments []string) error {
	if _, _, err := validateVerifiedPlanArguments(arguments); err != nil {
		return err
	}
	return errors.New("verified OpenTofu planning is supported only by the Linux workspace image")
}
