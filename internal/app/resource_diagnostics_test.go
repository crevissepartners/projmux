//go:build linux

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	at     string
}

func (w *resourceEventWriter) Append(event diagnostics.Event) error {
	if w.at != "" {
		event.At = w.at
	}
	w.events = append(w.events, event)
	return w.err
}

type resourceDiagnosticSemanticField struct {
	name  string
	value string
}

// resourceDiagnosticSecretViolations deliberately excludes Event.At: an
// RFC3339 timestamp can contain a PID or metric digit sequence by coincidence.
// Every non-time string in the closed diagnostic event remains in scope so an
// accidental raw path, process identifier, command, or collector error fails
// closed at the field that retained it.
func resourceDiagnosticSecretViolations(events []diagnostics.Event, secrets ...string) []string {
	var violations []string
	for index, event := range events {
		fields := []resourceDiagnosticSemanticField{
			{name: "level", value: event.Level},
			{name: "component", value: event.Component},
			{name: "event", value: event.Event},
			{name: "result", value: event.Result},
			{name: "run_id", value: event.RunID},
			{name: "version", value: event.Version},
			{name: "mux_backend", value: event.MuxBackend},
			{name: "command", value: event.Command},
			{name: "subcommand", value: event.Subcommand},
			{name: "kind", value: event.Kind},
			{name: "message", value: event.Message},
			{name: "operation", value: event.Operation},
			{name: "code", value: event.Code},
			{name: "source", value: event.Source},
			{name: "transition", value: event.Transition},
			{name: "disposition", value: event.Disposition},
			{name: "provider", value: event.Provider},
			{name: "category", value: event.Category},
			{name: "route", value: event.Route},
			{name: "ai_kind", value: event.AIKind},
			{name: "ai_result", value: event.AIResult},
			{name: "resource_result", value: event.ResourceResult},
			{name: "failure", value: event.Failure},
		}
		for _, field := range fields {
			for _, secret := range secrets {
				if secret != "" && strings.Contains(field.value, secret) {
					violations = append(violations, fmt.Sprintf("event[%d].%s contains %q", index, field.name, secret))
				}
			}
		}
	}
	return violations
}

func requireResourceDiagnosticsExcludeSecrets(t *testing.T, events []diagnostics.Event, secrets ...string) {
	t.Helper()
	if violations := resourceDiagnosticSecretViolations(events, secrets...); len(violations) != 0 {
		t.Fatalf("resource diagnostics retained secret-bearing semantic fields: %s", strings.Join(violations, "; "))
	}
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

func TestResourceDiagnosticPrivacyOracleUsesSemanticFields(t *testing.T) {
	safe := diagnostics.Event{
		At: "2026-09-01T00:00:00.100Z", Level: "info", Component: "resource",
		Event: "resource.sampler.outcome", Result: "success", RunID: "resource-run",
		Version: "0.10.0", MuxBackend: "tmux", Source: "sampler",
		ResourceResult: "partial", Failure: "sample-partial",
	}
	if violations := resourceDiagnosticSecretViolations([]diagnostics.Event{safe}, "100"); len(violations) != 0 {
		t.Fatalf("timestamp digits were treated as a semantic leak: %v", violations)
	}

	for _, test := range []struct {
		name      string
		secret    string
		inject    func(*diagnostics.Event)
		wantField string
	}{
		{name: "path", secret: "/private/project", inject: func(event *diagnostics.Event) { event.Message = "/private/project" }, wantField: "message"},
		{name: "pid", secret: "pid=4242", inject: func(event *diagnostics.Event) { event.Command = "collector pid=4242" }, wantField: "command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			leaked := safe
			test.inject(&leaked)
			violations := resourceDiagnosticSecretViolations([]diagnostics.Event{leaked}, test.secret)
			if len(violations) != 1 || !strings.Contains(violations[0], "."+test.wantField+" contains") {
				t.Fatalf("secret-bearing %s did not fail closed: %v", test.wantField, violations)
			}
		})
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
	requireResourceDiagnosticsExcludeSecrets(t, writer.events, resourceDiagnosticsPrivacySeed, "project-private", "pane-title", "uuid-session-SEED")
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
	requireResourceDiagnosticsExcludeSecrets(t, writer.events, resourceDiagnosticsPrivacySeed)
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
		writer := &resourceEventWriter{at: "2026-09-01T00:00:00.100Z"}
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
		if writer.events[0].At != "2026-09-01T00:00:00.100Z" {
			t.Fatalf("deterministic timestamp = %q, want .100 fixture", writer.events[0].At)
		}
		requireResourceDiagnosticsExcludeSecrets(t, writer.events, "/private/project", "seed-session", "seed-title", "seed-command", "/dev/pts/1", "100")
	})
}
