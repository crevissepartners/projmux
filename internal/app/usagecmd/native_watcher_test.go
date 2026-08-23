package usagecmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
)

func TestUsageStatusSuccessfulEventBatchSkipsCollectorForSourceDecision(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()
	limitID := "codex"
	label := "General"
	cadence := int64(300)
	eventRows := []usage.Snapshot{{
		Model:     codexadapter.Name,
		Window:    usage.Window5h,
		Bucket:    "codex",
		Pct:       73,
		ResetsAt:  now.Add(time.Hour),
		UpdatedAt: now,
		Source:    usage.SourceAppServer,
		RateLimit: &usage.RateLimitMetadata{
			BucketKey: "codex", LimitID: &limitID, Label: &label,
			Slot: "primary", CadenceMinutes: &cadence,
		},
	}}
	if err := codexadapter.NewNativeEventCache(stateDir, func() time.Time { return now }).Publish(eventRows); err != nil {
		t.Fatal(err)
	}
	if err := touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherHeartbeatName), now); err != nil {
		t.Fatal(err)
	}

	collector := &stubAdapter{name: codexadapter.Name, snaps: []usage.Snapshot{{
		Model: codexadapter.Name, Window: usage.Window5h, Pct: 99,
	}}}
	registry := usage.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	managerClockCalls := 0
	manager := usage.NewManager(registry, usage.NewStore(stateDir), func() time.Time {
		managerClockCalls++
		if managerClockCalls == 1 {
			return now
		}
		// Crossing the normal throttle floor proves the collector is not even
		// reconsidered after this invocation accepted an event batch.
		return now.Add(time.Minute)
	})
	command := New(func() time.Time { return now })
	command.managerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	command.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentCodex}, nil
	}
	command.lookupEnv = func(name string) string {
		switch name {
		case StateDirEnvVar:
			return stateDir
		case "HOME":
			return stateDir
		case "XDG_CONFIG_HOME":
			return filepath.Join(stateDir, "config")
		}
		return ""
	}

	var stdout, stderr bytes.Buffer
	if err := command.RunStatus(nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if collector.collectCalls != 0 {
		t.Fatalf("collector calls = %d, want 0 after successful event batch", collector.collectCalls)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("73%")) || bytes.Contains(stdout.Bytes(), []byte("99%")) {
		t.Fatalf("status output = %q, want event value only", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	stored, _, err := usage.NewStore(filepath.Clean(stateDir)).LoadAll()
	if err != nil || len(stored) != 1 || stored[0].Pct != 73 || stored[0].Source != usage.SourceAppServer {
		t.Fatalf("stored event batch = %#v, err=%v", stored, err)
	}
}

func TestNativeWatcherLeaseAllowsExactlyOneOwnerAndCanBeReacquired(t *testing.T) {
	leasePath := nativeWatcherPath(t.TempDir(), nativeWatcherLeaseName)
	releaseFirst, acquired, err := acquireNativeWatcherLease(leasePath)
	if err != nil || !acquired {
		t.Fatalf("first lease = acquired %v err %v", acquired, err)
	}
	releaseSecond, acquired, err := acquireNativeWatcherLease(leasePath)
	if err != nil || acquired {
		t.Fatalf("second lease = acquired %v err %v, want busy without error", acquired, err)
	}
	releaseSecond()
	releaseFirst()

	releaseThird, acquired, err := acquireNativeWatcherLease(leasePath)
	if err != nil || !acquired {
		t.Fatalf("reacquired lease = acquired %v err %v", acquired, err)
	}
	releaseThird()
}

func TestNativeWatcherLifecycleStopsAtDemandExpiryAndCleansHeartbeat(t *testing.T) {
	stateDir := t.TempDir()
	if err := touchNativeWatcherMarker(
		nativeWatcherPath(stateDir, nativeWatcherDemandName), time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}

	watchStarted := make(chan struct{})
	watchStopped := make(chan error, 1)
	command := New(time.Now)
	command.lookupEnv = func(name string) string {
		if name == StateDirEnvVar {
			return stateDir
		}
		return ""
	}
	command.watcherTimings = nativeWatcherTimings{
		heartbeatEvery: 5 * time.Millisecond,
		heartbeatFresh: 50 * time.Millisecond,
		demandTTL:      25 * time.Millisecond,
		batchMaxAge:    time.Second,
		failureBackoff: time.Second,
	}
	command.watchNativeRateLimitsFn = func(ctx context.Context, _ func([]usage.Snapshot) error) error {
		close(watchStarted)
		<-ctx.Done()
		watchStopped <- ctx.Err()
		return ctx.Err()
	}

	runDone := make(chan error, 1)
	go func() { runDone <- command.runNativeWatcher() }()
	select {
	case <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("watcher did not start")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runNativeWatcher = %v, want clean demand-expiry stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop after demand expired")
	}
	if err := <-watchStopped; !errors.Is(err, context.Canceled) {
		t.Fatalf("watch stop cause = %v, want context canceled", err)
	}
	if _, err := os.Lstat(nativeWatcherPath(stateDir, nativeWatcherHeartbeatName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("heartbeat cleanup error = %v, want absent", err)
	}
	if _, err := os.Lstat(nativeWatcherPath(stateDir, nativeWatcherFailureName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure marker error = %v, want absent after expected lifecycle stop", err)
	}
}

func TestEnsureNativeWatcherHonorsLiveHeartbeatAndFailureBackoff(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()
	starts := 0
	command := New(func() time.Time { return now })
	command.executableFn = func() (string, error) { return "/test/projmux", nil }
	command.startNativeWatcherFn = func(string) error {
		starts++
		return nil
	}

	for _, marker := range []string{nativeWatcherHeartbeatName, nativeWatcherFailureName} {
		if err := touchNativeWatcherMarker(nativeWatcherPath(stateDir, marker), now); err != nil {
			t.Fatal(err)
		}
		command.ensureNativeWatcher(stateDir)
		if starts != 0 {
			t.Fatalf("starts with fresh %s = %d, want 0", marker, starts)
		}
		if err := os.Remove(nativeWatcherPath(stateDir, marker)); err != nil {
			t.Fatal(err)
		}
	}

	command.ensureNativeWatcher(stateDir)
	if starts != 1 {
		t.Fatalf("starts without heartbeat/backoff = %d, want 1", starts)
	}
	if !nativeWatcherMarkerFresh(
		nativeWatcherPath(stateDir, nativeWatcherDemandName), now, nativeWatcherDemandTTL,
	) {
		t.Fatal("ensureNativeWatcher did not refresh demand marker")
	}
}

func TestEnsureNativeWatcherNeverStartsGoTestExecutable(t *testing.T) {
	for _, executable := range []string{
		"/tmp/go-build123/b001/usagecmd.test",
		`C:\Temp\go-build123\b001\usagecmd.test.exe`,
	} {
		t.Run(filepath.Base(executable), func(t *testing.T) {
			starts := 0
			command := New(time.Now)
			command.executableFn = func() (string, error) { return executable, nil }
			command.startNativeWatcherFn = func(string) error {
				starts++
				return nil
			}

			command.ensureNativeWatcher(t.TempDir())
			if starts != 0 {
				t.Fatalf("watcher child starts = %d, want 0 for Go test executable %q", starts, executable)
			}
		})
	}
}

func TestStartNativeWatcherProcessRejectsGoTestExecutableBeforeExec(t *testing.T) {
	for _, suffix := range []string{".test", ".test.exe"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "child-started")
			t.Setenv("PROJMUX_TEST_WATCHER_MARKER", marker)
			executable := filepath.Join(root, "watcher"+suffix)
			if err := os.WriteFile(executable, []byte(
				"#!/bin/sh\n: > \"$PROJMUX_TEST_WATCHER_MARKER\"\n",
			), 0o700); err != nil {
				t.Fatal(err)
			}

			err := startNativeWatcherProcess(executable)
			if !errors.Is(err, errNativeWatcherGoTestExecutable) {
				t.Fatalf("startNativeWatcherProcess(%q) error = %v, want %v", executable, err, errNativeWatcherGoTestExecutable)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("watcher child marker error = %v, want absent", err)
			}
		})
	}
}

func TestUsageStatusGoTestBinaryHasBoundedWatcherProcessesAndIsolatedState(t *testing.T) {
	root := t.TempDir()
	for name, path := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
		"XDG_RUNTIME_DIR": filepath.Join(root, "runtime"),
		"TMUX_TMPDIR":     filepath.Join(root, "tmux"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"GOCACHE":         filepath.Join(root, "gocache"),
		StateDirEnvVar:    filepath.Join(root, "usage"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testName := strings.TrimSuffix(strings.ToLower(filepath.Base(executable)), ".exe")
	if !strings.HasSuffix(testName, ".test") {
		t.Fatalf("go test executable = %q, want .test suffix", executable)
	}

	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	if err := registry.Register(&stubAdapter{
		name: codexadapter.Name,
		snaps: []usage.Snapshot{{
			Model: codexadapter.Name, Window: usage.Window5h, Pct: 17, UpdatedAt: now,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	manager := usage.NewManager(registry, usage.NewStore(os.Getenv(StateDirEnvVar)), func() time.Time { return now })
	starts := 0
	command := New(func() time.Time { return now })
	command.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentCodex}, nil
	}
	command.managerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	command.startNativeWatcherFn = func(string) error {
		starts++
		return nil
	}

	for i := range 3 {
		var stdout, stderr bytes.Buffer
		if err := command.RunStatus(nil, &stdout, &stderr); err != nil {
			t.Fatalf("RunStatus call %d: %v", i+1, err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("RunStatus call %d stderr = %q, want empty", i+1, stderr.String())
		}
	}
	if starts != 0 {
		t.Fatalf("watcher child starts after bounded status calls = %d, want 0", starts)
	}
	if !nativeWatcherMarkerFresh(
		nativeWatcherPath(os.Getenv(StateDirEnvVar), nativeWatcherDemandName), now, nativeWatcherDemandTTL,
	) {
		t.Fatal("status did not write demand marker to isolated usage state")
	}
}
