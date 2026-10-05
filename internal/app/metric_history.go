package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"golang.org/x/sys/unix"
)

// availableStateBytes reports the filesystem containing the state directory,
// even on first run when the directory itself has not been created yet.
func availableStateBytes(path string) (uint64, error) {
	for {
		var stat unix.Statfs_t
		err := unix.Statfs(path, &stat)
		if err == nil {
			if stat.Bsize <= 0 {
				return 0, fmt.Errorf("invalid filesystem block size: %d", stat.Bsize)
			}
			blockSize := uint64(stat.Bsize) // #nosec G115 -- positive Statfs block size checked above
			if stat.Bavail > math.MaxUint64/blockSize {
				return 0, fmt.Errorf("filesystem available bytes overflow")
			}
			return stat.Bavail * blockSize, nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, err
		}
		path = parent
	}
}

func systemMetricPoints(metrics systemstatus.Metrics, stateDir string, now time.Time) ([]usage.MetricPoint, error) {
	var points []usage.MetricPoint
	if metrics.CPUPercent != nil {
		points = append(points, usage.MetricPoint{Name: "system.cpu.percent", Value: float64(*metrics.CPUPercent), ObservedAt: now})
	}
	if metrics.MemoryPercent != nil {
		points = append(points, usage.MetricPoint{Name: "system.memory.percent", Value: float64(*metrics.MemoryPercent), ObservedAt: now})
	}
	available, err := availableStateBytes(stateDir)
	if err != nil {
		return points, fmt.Errorf("state filesystem available bytes: %w", err)
	}
	points = append(points, usage.MetricPoint{Name: "system.fs.available_bytes", Value: float64(available), ObservedAt: now})
	return points, nil
}

// liveAgentMetricPoints uses only the already resolved all-Project graph.
// Any unknown Agent or unavailable observation suppresses the provider's
// point; a Registry phase alone is never counted as runtime evidence.
func liveAgentMetricPoints(graph resourcegraph.Graph, now time.Time) []usage.MetricPoint {
	if len(graph.Unavailable) > 0 {
		return nil
	}
	counts := map[string]int{}
	unknown := map[string]bool{}
	for _, node := range graph.Agents {
		provider := node.Agent.Spec.Provider
		if provider == "" {
			continue
		}
		if _, ok := counts[provider]; !ok {
			counts[provider] = 0
		}
		switch node.Status {
		case resourcegraph.StatusLive:
			counts[provider]++
		case resourcegraph.StatusUnknown, resourcegraph.StatusMissingRoot:
			unknown[provider] = true
		}
	}
	var points []usage.MetricPoint
	for provider, count := range counts {
		if !unknown[provider] {
			points = append(points, usage.MetricPoint{Name: "agent.live.count", Value: float64(count), ObservedAt: now, Provider: provider})
		}
	}
	return points
}

func appendLiveAgentHistory(graph resourcegraph.Graph) error {
	points := liveAgentMetricPoints(graph, time.Now().UTC())
	if len(points) == 0 {
		return nil
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return err
	}
	historyDir := filepath.Join(paths.StateDir, "usage")
	if override := os.Getenv(usagecmd.StateDirEnvVar); override != "" {
		historyDir = override
	}
	return usage.NewStore(historyDir).AppendHistory(points, time.Now().UTC())
}
