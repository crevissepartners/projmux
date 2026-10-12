package runtime

import (
	"fmt"
)

// RuntimeMutationAbsentTargetConverged is the sentinel an absence-effect Guard
// returns when its own fresh read already proved the exact target absent.
func RuntimeMutationAbsentTargetConverged(target string) error {
	return fmt.Errorf("exact target %s already absent: %w", target, ErrRuntimeMutationEffectConverged)
}
