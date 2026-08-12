package diagnostics

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type readOnlyStoreStub struct {
	result ReadResult
	err    error
}

func (s readOnlyStoreStub) ReadOnly() (ReadResult, error) { return s.result, s.err }

func TestReadRuntimeHealthTypedProjection(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Event: "lifecycle.outcome", Result: "error", Operation: string(OperationSessionSwitch), Code: string(CodeSessionSwitchFailed)},
		{Event: "lifecycle.outcome", Result: "error", Operation: string(OperationTmuxApply), Code: string(CodeTmuxApplySocketUnreachable)},
		{Event: "lifecycle.outcome", Result: "error", Operation: string(OperationTmuxApply), Code: string(CodeTmuxApplyReloadFailed)},
	}
	health, err := ReadRuntimeHealth(readOnlyStoreStub{result: ReadResult{Events: events, Malformed: 2, Truncated: true}})
	if err != nil {
		t.Fatal(err)
	}
	if health.MuxBackend != "tmux" || health.Socket != RuntimeHealthy || health.Apply != RuntimeError || health.Malformed != 2 || !health.Truncated {
		t.Fatalf("health = %#v", health)
	}
	wantCodes := []Code{CodeSessionSwitchFailed, CodeTmuxApplySocketUnreachable, CodeTmuxApplyReloadFailed}
	if !reflect.DeepEqual(health.RecentFailureCodes, wantCodes) {
		t.Fatalf("failure codes = %#v, want %#v", health.RecentFailureCodes, wantCodes)
	}
}

func TestReadRuntimeHealthMissingAndFailureRemainReadOnly(t *testing.T) {
	t.Parallel()
	health, err := ReadRuntimeHealth(readOnlyStoreStub{result: ReadResult{Missing: true}})
	if err != nil || !health.Missing || health.Socket != RuntimeUnknown || health.Apply != RuntimeUnknown {
		t.Fatalf("missing health = %#v err=%v", health, err)
	}
	want := errors.New("permission denied")
	if _, err := ReadRuntimeHealth(readOnlyStoreStub{err: want}); !errors.Is(err, want) {
		t.Fatalf("ReadRuntimeHealth error = %v, want %v", err, want)
	}
}

func TestReadRuntimeHealthLatestEventWinsAndFailureCodesAreBounded(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Event: "lifecycle.outcome", Result: "success", Operation: string(OperationTmuxApply)},
		{Event: "lifecycle.outcome", Result: "error", Operation: string(OperationTmuxApply), Code: string(CodeTmuxApplySocketUnreachable)},
	}
	for range recentFailureLimit + 5 {
		events = append(events, Event{Event: "lifecycle.outcome", Result: "error", Operation: string(OperationSessionKill), Code: string(CodeSessionKillFailed)})
	}
	events = append(events, Event{Event: "lifecycle.outcome", Result: "success", Operation: string(OperationTmuxApply)})
	health, err := ReadRuntimeHealth(readOnlyStoreStub{result: ReadResult{Events: events}})
	if err != nil {
		t.Fatal(err)
	}
	if health.Socket != RuntimeHealthy || health.Apply != RuntimeHealthy {
		t.Fatalf("latest health = %#v, want healthy", health)
	}
	if len(health.RecentFailureCodes) != recentFailureLimit {
		t.Fatalf("recent codes = %d, want %d", len(health.RecentFailureCodes), recentFailureLimit)
	}
	for _, code := range health.RecentFailureCodes {
		if code != CodeSessionKillFailed {
			t.Fatalf("bounded tail retained stale code %q", code)
		}
	}
}

func TestReadRuntimeHealthRealStoreDoesNotMutateSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"at":"2026-08-13T00:00:00Z","level":"error","component":"runtime","event":"lifecycle.outcome","result":"error","duration_ms":1,"run_id":"safe","version":"0.10.0","mux_backend":"tmux","kind":"runtime","operation":"session.switch","code":"session.switch.failed"}` + "\ncorrupt\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	health, err := ReadRuntimeHealth(NewStore(path))
	if err != nil {
		t.Fatal(err)
	}
	afterBody, _ := os.ReadFile(path)
	after, _ := os.Stat(path)
	if !bytes.Equal(afterBody, body) || before.Mode().Perm() != after.Mode().Perm() || after.Mode().Perm() != 0o644 {
		t.Fatalf("read-only seam mutated source: mode %o -> %o body_equal=%v", before.Mode().Perm(), after.Mode().Perm(), bytes.Equal(afterBody, body))
	}
	if health.Malformed != 1 || !reflect.DeepEqual(health.RecentFailureCodes, []Code{CodeSessionSwitchFailed}) {
		t.Fatalf("health = %#v", health)
	}
}
