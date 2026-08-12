//go:build linux

package app

import inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"

func newPlatformResourceCollector() resourceSnapshotCollector {
	return inttmux.NewClient(inttmux.ExecRunner{})
}
