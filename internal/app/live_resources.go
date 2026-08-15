package app

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/theme"
)

const liveResourcesTmuxOption = "@projmux_live_resources"

const (
	liveResourceCPUWarningAt    = 70
	liveResourceMemoryWarningAt = 75
	liveResourceCriticalAt      = 90
)

type liveResourceSeverity uint8

const (
	liveResourceUnknown liveResourceSeverity = iota
	liveResourceNormal
	liveResourceWarning
	liveResourceCritical
)

func loadLiveResourcesMode(homeDir func() (string, error), lookupEnv func(string) string) config.LiveResourcesMode {
	if !systemstatus.Supported() {
		return config.LiveResourcesOff
	}
	paths, err := configPaths(homeDir, lookupEnv)
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
	return "#{?#{==:#{" + liveResourcesTmuxOption + "},on},#[range=user|resources]#(" + bin + " internal status resources)#[norange],}"
}

func formatLiveResourcesStatus(metrics systemstatus.Metrics) string {
	return formatLiveResourcesStatusWithRoles(metrics, statusSegmentRoles)
}

func formatLiveResourcesStatusWithRoles(metrics systemstatus.Metrics, roles theme.RenderRoles) string {
	if !metrics.Available() {
		return ""
	}
	cpu := formatLiveResourceMetric("CPU", metrics.CPUPercent, liveResourceCPUWarningAt, roles)
	memory := formatLiveResourceMetric("MEM", metrics.MemoryPercent, liveResourceMemoryWarningAt, roles)
	return " " + cpu + "  " + memory
}

func classifyLiveResourceSeverity(percent *int, warningAt int) liveResourceSeverity {
	if percent == nil {
		return liveResourceUnknown
	}
	return classifyResourcePercent(float64(*percent), float64(warningAt))
}

// classifyResourcePercent is the shared statusbar/inspector threshold contract.
// Keep the critical boundary common and pass the metric-specific warning
// boundary (CPU 70, memory 75) so both surfaces assign identical semantics.
func classifyResourcePercent(percent, warningAt float64) liveResourceSeverity {
	if percent < warningAt {
		return liveResourceNormal
	}
	if percent >= liveResourceCriticalAt {
		return liveResourceCritical
	}
	return liveResourceWarning
}

func formatLiveResourceMetric(label string, percent *int, warningAt int, roles theme.RenderRoles) string {
	value := " --%"
	if percent != nil {
		value = fmt.Sprintf("%3d%%", *percent)
	}
	style := roles.StatusTextSecondary
	switch classifyLiveResourceSeverity(percent, warningAt) {
	case liveResourceWarning:
		style = roles.StateWarning
	case liveResourceCritical:
		style = roles.StateCritical + ",bold"
	}
	return fmt.Sprintf("#[fg=%s]%s %s#[default]", style, label, value)
}
