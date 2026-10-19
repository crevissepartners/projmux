package projmux

import (
	"context"

	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ResourcesSupported reports whether this build supports statusbar host resources.
// It needs no Client or configuration paths and does not sample metrics, update
// the CPU cache, read or write files, or contact a runtime or provider.
// Linux and macOS with cgo are supported; macOS without cgo is unsupported.
// This does not report the saved or effective statusbar resources setting.
func ResourcesSupported() bool {
	return systemstatus.Supported()
}

// SampleSystem samples the existing statusbar host CPU and memory values.
// It updates the CPU delta cache in the client's captured StateDir, using the
// same 30-second reference lifetime as the statusbar. This is not an effect-free
// read. It neither reads statusbar enablement nor collects usage history.
// An already-canceled context prevents sampling and cache writes; cancellation
// after sampling starts does not roll back the synchronous sampler's effects.
// Unsupported platforms and unavailable metrics are represented in the result,
// not as errors. Cache I/O failures retain the existing sampler's best effort
// behavior. Returned pointers belong to the caller.
func (c *Client) SampleSystem(ctx context.Context) (core.SystemSample, error) {
	return c.sampleSystem(ctx, systemstatus.Sampler{})
}

func (c *Client) sampleSystem(ctx context.Context, sampler systemstatus.Sampler) (core.SystemSample, error) {
	if err := ctx.Err(); err != nil {
		return core.SystemSample{}, err
	}
	if c.pathsErr != nil {
		return core.SystemSample{}, c.pathsErr
	}
	out := core.SystemSample{Supported: systemstatus.Supported()}
	if !out.Supported {
		return out, nil
	}
	sampler.CachePath = c.paths.LiveResourcesSampleFile()
	metrics := sampler.Sample()
	out.CPUPercent, out.MemoryPercent = metrics.CPUPercent, metrics.MemoryPercent
	return out, nil
}
