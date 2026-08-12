//go:build !linux

package app

import (
	"context"
	"runtime"
	"time"

	coreresources "github.com/crevissepartners/projmux/internal/core/resources"
)

type unsupportedResourceCollector struct{}

func (unsupportedResourceCollector) CollectResourceSnapshot(_ context.Context, _ *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
	now := time.Now()
	sample := coreresources.Sample{At: now, Available: false, UnavailableReason: "platform:" + runtime.GOOS}
	return coreresources.Snapshot{At: now, Status: coreresources.StatusUnavailable, StatusReason: sample.UnavailableReason}, sample, nil
}

func newPlatformResourceCollector() resourceSnapshotCollector { return unsupportedResourceCollector{} }
