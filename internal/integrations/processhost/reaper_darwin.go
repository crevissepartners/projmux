package processhost

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

func prepareReaper() error { return nil }

func reapGroup(group int, grace time.Duration) error {
	// macOS reparents orphan descendants to launchd, which reaps them. Wait for
	// the exact process group to disappear rather than counting a kill as proof.
	deadline := time.Now().Add(grace)
	for {
		err := syscall.Kill(-group, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("owned group %d still exists", group)
		}
		time.Sleep(time.Millisecond)
	}
}
