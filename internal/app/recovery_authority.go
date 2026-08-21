package app

import "github.com/crevissepartners/projmux/internal/core/controller"

// requireAutomaticRecoveryPaths is the single production gate for the
// non-L8 automatic producers whose implementation predates the authority
// ladder. L8 actions additionally pass controller.Authorize candidate by
// candidate. Keeping this function singular makes its three trigger callsites
// structurally auditable.
func requireAutomaticRecoveryPaths(names ...string) error {
	for _, name := range names {
		if err := controller.RequireAutomaticRecoveryPath(name); err != nil {
			return err
		}
	}
	return nil
}
