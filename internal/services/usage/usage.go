// Package usage reads saved account usage without linking provider adapters.
package usage

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

func filterSnapshotsByModels(snaps []usage.Snapshot, models []string) []usage.Snapshot {
	allowed := usageModelSet(models)
	if len(allowed) == 0 {
		return nil
	}
	out := snaps[:0:0]
	for _, s := range snaps {
		if allowed[strings.ToLower(strings.TrimSpace(s.Model))] {
			out = append(out, s)
		}
	}
	return out
}
func accountUsageSnapshots(snaps []usage.Snapshot) []usage.Snapshot {
	out := make([]usage.Snapshot, 0, len(snaps))
	for _, snapshot := range snaps {
		if snapshot.Window == usage.WindowContext {
			continue
		}
		out = append(out, snapshot)
	}
	return out
}
func FilterState(state usage.State, models []string) usage.State {
	allowed := usageModelSet(models)
	if len(allowed) == 0 {
		return usage.State{
			LastCollect: map[string]time.Time{},
			Backoff:     map[string]usage.BackoffState{},
		}
	}
	out := usage.State{
		Snapshots:   accountUsageSnapshots(filterSnapshotsByModels(state.Snapshots, models)),
		LastCollect: map[string]time.Time{},
		Backoff:     map[string]usage.BackoffState{},
	}
	for name, value := range state.LastCollect {
		if allowed[strings.ToLower(strings.TrimSpace(name))] {
			out.LastCollect[name] = value
		}
	}
	for name, value := range state.Backoff {
		if allowed[strings.ToLower(strings.TrimSpace(name))] {
			out.Backoff[name] = value
		}
	}
	return out
}
func usageModelSet(models []string) map[string]bool {
	scope := normalizeUsageModelScope(models)
	out := make(map[string]bool, len(scope))
	for _, model := range scope {
		out[model] = true
	}
	return out
}
func normalizeUsageModelScope(models []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

type UnsupportedProvider struct{ Model, Label, Reason string }
type Cache struct {
	State       usage.State
	Unsupported []UnsupportedProvider
	ModTime     time.Time
}

// Cached shares the statusbar reader. Load repairs private permissions; it
// never collects or writes snapshot contents or collection metadata.
func Cached(ctx context.Context, load func() (usage.State, error), file string, models []string, unsupported []UnsupportedProvider) (Cache, error) {
	if err := ctx.Err(); err != nil {
		return Cache{}, err
	}
	state, err := load()
	if err != nil {
		return Cache{}, err
	}
	result := Cache{State: FilterState(state, models), Unsupported: unsupported}
	if info, err := os.Stat(file); err == nil {
		result.ModTime = info.ModTime()
	}
	return result, nil
}

// ProviderScope preserves enabled provider order and default-on-read-error policy.
func ProviderScope(paths config.Paths) ([]string, []UnsupportedProvider) {
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		agents = append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	models := []string{}
	unsupported := []UnsupportedProvider{}
	for _, agent := range agents {
		p, ok := aiprovider.Lookup(string(agent))
		if !ok {
			continue
		}
		if p.UsageSupported {
			models = append(models, p.UsageModel)
		} else {
			unsupported = append(unsupported, UnsupportedProvider{Model: string(p.ID), Label: p.DisplayName, Reason: "no supported usage adapter"})
		}
	}
	return models, unsupported
}

// History retains the CLI history reader's locking, retention and permissions.
func History(ctx context.Context, dir string, filter usage.HistoryFilter) ([]usage.MetricPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	points, err := usage.NewStore(dir).ReadHistory(filter)
	if err != nil {
		return nil, err
	}
	if points == nil {
		points = []usage.MetricPoint{}
	}
	return points, nil
}
