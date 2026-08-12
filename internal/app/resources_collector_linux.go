//go:build linux

package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coreresources "github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/integrations/procfsresources"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type linuxResourceCollector struct {
	tmux         *inttmux.Client
	procfs       procfsresources.Collector
	projectRoots func() ([]string, error)
}

func newPlatformResourceCollector() resourceSnapshotCollector {
	var once sync.Once
	var roots []string
	var rootsErr error
	return linuxResourceCollector{
		tmux:   inttmux.NewClient(inttmux.ExecRunner{}),
		procfs: procfsresources.Collector{},
		projectRoots: func() ([]string, error) {
			once.Do(func() {
				switcher := newSwitchCommand()
				inputs, err := switcher.projectDiscoveryInputs(false)
				if err != nil {
					rootsErr = err
					return
				}
				roots, rootsErr = candidates.DiscoverProjectRoots(inputs)
			})
			return roots, rootsErr
		},
	}
}

func (c linuxResourceCollector) CollectResourceSnapshot(ctx context.Context, previous *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
	inventory, err := c.tmux.ListResourcePanes(ctx)
	if err != nil {
		return coreresources.Snapshot{}, coreresources.Sample{}, err
	}
	projectRoots, err := c.projectRoots()
	if err != nil {
		return coreresources.Snapshot{}, coreresources.Sample{}, fmt.Errorf("discover resource project roots: %w", err)
	}
	inventory = coreresources.ResolveProjectAnchors(inventory, projectRoots)
	snapshot, current := c.procfs.Collect(ctx, inventory, previous)
	return snapshot, current, nil
}
