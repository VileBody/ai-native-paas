//go:build !linux

package workspaceagent

import "errors"

func HardenVerifiedSubcommand() error {
	return errors.New("verified workspace identity handoff requires Linux")
}
