//go:build !linux

package workspaceagent

import "errors"

func ExecuteVerifiedTofuPlan([]string) error {
	return errors.New("verified OpenTofu planning is supported only by the Linux workspace image")
}
