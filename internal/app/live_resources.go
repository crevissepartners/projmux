package app

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
)

const liveResourcesTmuxOption = "@projmux_live_resources"

func loadLiveResourcesMode(homeDir func() (string, error), lookupEnv func(string) string) config.LiveResourcesMode {
	if !systemstatus.Supported() {
		return config.LiveResourcesOff
	}
	paths, err := pickerBackendConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return config.LiveResourcesOff
	}
	mode, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile())
	if err != nil {
		return config.LiveResourcesOff
	}
	return mode
}

func statusbarLiveResourcesSegment(bin string) string {
	return "#{?#{==:#{" + liveResourcesTmuxOption + "},on},#(" + bin + " status resources),}"
}

func formatLiveResourcesStatus(metrics systemstatus.Metrics) string {
	if !metrics.Available() {
		return ""
	}
	cpu := "--"
	if metrics.CPUPercent != nil {
		cpu = fmt.Sprintf("%d", *metrics.CPUPercent)
	}
	memory := "--"
	if metrics.MemoryPercent != nil {
		memory = fmt.Sprintf("%d", *metrics.MemoryPercent)
	}
	return fmt.Sprintf(" #[fg=%s]CPU %s%%  MEM %s%%#[default]", statusSegmentRoles.StatusTextSecondary, cpu, memory)
}
