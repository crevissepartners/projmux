//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreresources "github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/procfsresources"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const resourceDiagnosticsPrivacySeed = "pid-4242-cpu-73-mem-91-project-private-pane-title-command-uuid-session-SEED"

type resourceCollectorFunc func(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error)

func (f resourceCollectorFunc) CollectResourceSnapshot(ctx context.Context, previous *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
	return f(ctx, previous)
}

type resourceRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f resourceRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

type resourceEventWriter struct {
	events []diagnostics.Event
	err    error
}

func (w *resourceEventWriter) Append(event diagnostics.Event) error {
	w.events = append(w.events, event)
	return w.err
}

func newResourceTestLifecycle(writer diagnostics.EventWriter, collector resourceSnapshotCollector, now func() time.Time, interval time.Duration) *resourceLifecycle {
	recorder := diagnostics.NewLifecycleRecorder(writer, "resource-app-run", "0.10.0", diagnostics.MuxBackend()).Resource()
	lifecycle := newResourceLifecycle(collector, now, interval)
	lifecycle.diagnostics = recorder
	return lifecycle
}

func collectResourceTestSnapshot(lifecycle *resourceLifecycle, previous *coreresources.Sample) resourceCollectionResult {
	lifecycle.active.Add(1)
	result := lifecycle.collectSnapshot(0, previous)
	lifecycle.finishCollection(result, false)
	return result
}

func TestResourceDiagnosticsProductionGraphWiresRecorder(t *testing.T) {
	writer := &resourceEventWriter{}
	app := NewWithLifecycleDiagnostics(diagnostics.NewLifecycleRecorder(writer, "resource-production", "0.10.0", "tmux"))
	if app.resources.diagnostics == nil {
		t.Fatal("production app graph did not wire Resource Inspector diagnostics")
	}
}

func TestStatusResourcesHostSamplerHasZeroAttributionOwnership(t *testing.T) {
	writer := &resourceEventWriter{}
	app := NewWithLifecycleDiagnostics(diagnostics.NewLifecycleRecorder(writer, "status-host-only", "0.10.0", "tmux"))
	home := t.TempDir()
	app.status.homeDir = func() (string, error) { return home, nil }
	app.status.lookupEnv = func(name string) string {
		if name == "XDG_STATE_HOME" {
			return filepath.Join(home, "state")
		}
		return ""
	}
	if err := app.status.Run([]string{"resources"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 0 {
		t.Fatalf("host-only status sampler events = %#v, want zero", writer.events)
	}
}

func TestResourceLifecycleActualOutcomePrivacyHotPathAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 14, 20, 0, 0, 0, time.UTC)
	partial := resourceReadySnapshot(now)
	partial.Status = coreresources.StatusPartial
	partial.StatusReason = "partial " + resourceDiagnosticsPrivacySeed
	ready := resourceReadySnapshot(now.Add(time.Second))
	unavailable := coreresources.Snapshot{At: now.Add(2 * time.Second), Status: coreresources.StatusUnavailable, StatusReason: "cannot read " + resourceDiagnosticsPrivacySeed}
	sequence := []coreresources.Snapshot{ready, partial, partial, ready, partial, unavailable}
	index := 0
	collector := resourceCollectorFunc(func(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
		snapshot := sequence[index]
		index++
		return snapshot, coreresources.Sample{At: snapshot.At, Available: snapshot.Status != coreresources.StatusUnavailable}, nil
	})
	writer := &resourceEventWriter{}
	lifecycle := newResourceTestLifecycle(writer, collector, func() time.Time { return now.Add(3 * time.Second) }, time.Hour)

	for range sequence {
		result := collectResourceTestSnapshot(lifecycle, nil)
		if result.snapshot.Status == "" {
			t.Fatal("collection changed the Resource Inspector snapshot semantics")
		}
	}
	if len(writer.events) != 3 {
		t.Fatalf("events = %#v, want partial, recovered partial re-entry, unavailable", writer.events)
	}
	if writer.events[0].ResourceResult != "partial" || writer.events[1].ResourceResult != "partial" || writer.events[2].ResourceResult != "unavailable" {
		t.Fatalf("resource transitions = %#v", writer.events)
	}
	raw, _ := json.Marshal(writer.events)
	for _, seed := range []string{resourceDiagnosticsPrivacySeed, "project-private", "pane-title", "uuid-session-SEED"} {
		if strings.Contains(string(raw), seed) {
			t.Fatalf("operations leaked %q: %s", seed, raw)
		}
	}
}

func TestResourceLifecycleCollectionErrorAndAppendFailurePreserveResult(t *testing.T) {
	seededErr := errors.New("collector " + resourceDiagnosticsPrivacySeed)
	collector := resourceCollectorFunc(func(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
		return coreresources.Snapshot{}, coreresources.Sample{}, seededErr
	})
	writer := &resourceEventWriter{err: errors.New("append denied")}
	lifecycle := newResourceTestLifecycle(writer, collector, time.Now, time.Hour)
	result := collectResourceTestSnapshot(lifecycle, nil)
	if result.snapshot.Status != coreresources.StatusUnavailable || result.snapshot.StatusReason != "collection-error" {
		t.Fatalf("snapshot = %#v, want historical unavailable collection-error UI result", result.snapshot)
	}
	if len(writer.events) != 1 || writer.events[0].ResourceResult != "error" || writer.events[0].Failure != "collection-failed" || writer.events[0].Message != "" {
		t.Fatalf("events = %#v", writer.events)
	}
	raw, _ := json.Marshal(writer.events)
	if strings.Contains(string(raw), resourceDiagnosticsPrivacySeed) {
		t.Fatalf("arbitrary collector error leaked: %s", raw)
	}
}

func TestResourceLifecycleScanBudgetIsActualContextDeadlineAndBounded(t *testing.T) {
	collector := resourceCollectorFunc(func(ctx context.Context, _ *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
		<-ctx.Done()
		return coreresources.Snapshot{}, coreresources.Sample{}, ctx.Err()
	})
	writer := &resourceEventWriter{}
	lifecycle := newResourceTestLifecycle(writer, collector, time.Now, 10*time.Millisecond)
	lifecycle.scanBudget = 10 * time.Millisecond
	for range 5 {
		result := collectResourceTestSnapshot(lifecycle, nil)
		if result.snapshot.Status != coreresources.StatusUnavailable || result.snapshot.StatusReason != "scan-budget-exceeded" {
			t.Fatalf("snapshot = %#v, want unavailable budget boundary", result.snapshot)
		}
	}
	if len(writer.events) != 1 || writer.events[0].ResourceResult != "scan-budget-exceeded" || writer.events[0].Failure != "scan-budget-exceeded" {
		t.Fatalf("budget events = %#v, want one persistent transition", writer.events)
	}
}

func TestResourceLifecycleFreshCommitPrecedesStaleRecovery(t *testing.T) {
	now := time.Date(2026, 8, 14, 20, 0, 10, 0, time.UTC)
	old := now.Add(-5 * time.Second)
	fresh := resourceReadySnapshot(now)
	collector := resourceCollectorFunc(func(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
		return fresh, coreresources.Sample{At: fresh.At, Available: true}, nil
	})
	writer := &resourceEventWriter{}
	lifecycle := newResourceTestLifecycle(writer, collector, func() time.Time { return now }, 2*time.Second)
	lifecycle.lastCompleteAt = old
	lifecycle.diagnostics.Record(diagnostics.ResourceSourceRefresh, diagnostics.ResourceResultStale, diagnostics.ResourceFailureSampleStale, old)
	if len(writer.events) != 1 {
		t.Fatalf("initial stale events = %#v", writer.events)
	}
	lifecycle.afterStateCommit = func() {
		lifecycle.mu.Lock()
		committedAt := lifecycle.lastCompleteAt
		committedSample := lifecycle.previous
		lifecycle.mu.Unlock()
		if !committedAt.Equal(fresh.At) || committedSample == nil || !committedSample.At.Equal(fresh.At) {
			t.Fatalf("fresh state was not committed before diagnostics recovery: at=%v sample=%#v", committedAt, committedSample)
		}
		// Model a stale request classified before the fresh commit but delayed
		// until the former recovery-before-commit race window. Recovery must not
		// have happened yet, so this remains coalesced.
		lifecycle.diagnostics.Record(diagnostics.ResourceSourceRefresh, diagnostics.ResourceResultStale, diagnostics.ResourceFailureSampleStale, old)
		if len(writer.events) != 1 {
			t.Fatalf("fresh completion reopened stale before commit: %#v", writer.events)
		}
	}
	collectResourceTestSnapshot(lifecycle, nil)
	lifecycle.afterStateCommit = nil

	lifecycle.recordStaleIfCurrent(old)
	if len(writer.events) != 1 {
		t.Fatalf("committed fresh state accepted old stale transition: %#v", writer.events)
	}
	lifecycle.mu.Lock()
	lifecycle.lastCompleteAt = old
	lifecycle.mu.Unlock()
	lifecycle.recordStaleIfCurrent(old)
	if len(writer.events) != 2 {
		t.Fatalf("stale recovery did not permit later re-entry: %#v", writer.events)
	}
}

func TestResourceLifecycleStalePollingTransitionIsBounded(t *testing.T) {
	now := time.Date(2026, 8, 14, 20, 0, 10, 0, time.UTC)
	writer := &resourceEventWriter{}
	lifecycle := newResourceTestLifecycle(writer, &sequenceResourceCollector{snapshots: []coreresources.Snapshot{resourceReadySnapshot(now)}}, func() time.Time { return now }, 2*time.Second)
	lifecycle.collecting = true
	lifecycle.lastCompleteAt = now.Add(-5 * time.Second)
	for range 100 {
		lifecycle.requestAutomatic()
	}
	if len(writer.events) != 1 || writer.events[0].ResourceResult != "stale" || writer.events[0].Source != "refresh" {
		t.Fatalf("stale events = %#v, want one transition", writer.events)
	}
}

func TestLinuxResourceCollectorActualInventoryProjectAndPartialSeams(t *testing.T) {
	t.Run("inventory error", func(t *testing.T) {
		writer := &resourceEventWriter{}
		collector := linuxResourceCollector{
			tmux: inttmux.NewClient(resourceRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("tmux " + resourceDiagnosticsPrivacySeed)
			})),
			projectRoots: func() ([]string, error) { return nil, nil },
		}
		result := collectResourceTestSnapshot(newResourceTestLifecycle(writer, collector, time.Now, time.Hour), nil)
		if result.snapshot.Status != coreresources.StatusUnavailable || len(writer.events) != 1 || writer.events[0].Source != "tmux-inventory" || writer.events[0].Failure != "inventory-failed" {
			t.Fatalf("result=%#v events=%#v", result, writer.events)
		}
	})

	row := strings.Join([]string{"/tmp/private.sock", "$1", "seed-session", "@1", "seed-window", "%1", "100", "/dev/pts/1", "/private/project", "", "seed-label", "codex", "seed-topic", "seed-command", "seed-title"}, "\x1f") + "\n"
	t.Run("project discovery error", func(t *testing.T) {
		writer := &resourceEventWriter{}
		collector := linuxResourceCollector{
			tmux:         inttmux.NewClient(resourceRunnerFunc(func(context.Context, string, ...string) ([]byte, error) { return []byte(row), nil })),
			projectRoots: func() ([]string, error) { return nil, errors.New("discover " + resourceDiagnosticsPrivacySeed) },
		}
		_ = collectResourceTestSnapshot(newResourceTestLifecycle(writer, collector, time.Now, time.Hour), nil)
		if len(writer.events) != 1 || writer.events[0].Source != "project-discovery" || writer.events[0].Failure != "project-discovery-failed" {
			t.Fatalf("events = %#v", writer.events)
		}
	})

	t.Run("procfs partial", func(t *testing.T) {
		root := filepath.Join("..", "integrations", "procfsresources", "testdata", "basic")
		writer := &resourceEventWriter{}
		collector := linuxResourceCollector{
			tmux:         inttmux.NewClient(resourceRunnerFunc(func(context.Context, string, ...string) ([]byte, error) { return []byte(row), nil })),
			projectRoots: func() ([]string, error) { return []string{"/private/project"}, nil },
			procfs: procfsresources.Collector{Root: root, ReadFile: func(path string) ([]byte, error) {
				if strings.HasSuffix(path, filepath.Join("101", "stat")) {
					return nil, fs.ErrPermission
				}
				return os.ReadFile(path)
			}},
		}
		result := collectResourceTestSnapshot(newResourceTestLifecycle(writer, collector, time.Now, time.Hour), nil)
		if result.snapshot.Status != coreresources.StatusPartial || len(writer.events) != 1 || writer.events[0].ResourceResult != "partial" || writer.events[0].Failure != "sample-partial" {
			t.Fatalf("snapshot=%#v events=%#v", result.snapshot, writer.events)
		}
		raw, _ := json.Marshal(writer.events)
		for _, forbidden := range []string{"/private/project", "seed-session", "seed-title", "seed-command", "/dev/pts/1", "100"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("actual collector event leaked %q: %s", forbidden, raw)
			}
		}
	})
}
