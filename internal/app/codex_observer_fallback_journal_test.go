package app

import (
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// An invocation installs the observer fallback sink exactly when it may append
// to the journal, writes to the State directory the create reported rather than
// the application's default one, and restores the previous sink afterwards.
func TestObserverFallbackJournalInstallsWhereTheJournalMayAppend(t *testing.T) {
	recorder := diagnostics.NewLifecycleRecorder(diagnostics.NewStore(filepath.Join(t.TempDir(), "invocation.jsonl")), "run", "1.0.0", "tmux")
	// report is one non-serving start a create reports; it returns what its State directory's journal holds.
	report := func() []diagnostics.Event {
		t.Helper()
		state := t.TempDir()
		notices := mutationruntime.ReportNativeObserverStartup(state, mutationruntime.NativeObserverIdentity{AgentUID: "agent-abc234", PaneUID: "pane-xyz567"}, mutationruntime.NativeObserverStartup{Status: "fallback", Reason: "observer-exited"})
		if len(notices) != 1 {
			t.Fatalf("notices = %v", notices)
		}
		events, _ := diagnostics.NewStore(filepath.Join(state, diagnostics.LogDirName, diagnostics.LogFileName)).Read()
		return events
	}
	for name, restore := range map[string]func(){
		"no recorder":    observeCodexObserverFallbacks(nil, []string{"agent", "create"}),
		"journal denied": observeCodexObserverFallbacks(recorder, []string{"doctor"}),
	} {
		if events := report(); len(events) != 0 {
			t.Fatalf("%s still wrote %+v", name, events)
		}
		restore()
	}
	restore := observeCodexObserverFallbacks(recorder, []string{"agent", "create"})
	events := report()
	restore()
	if len(events) != 1 || events[0].Event != "codex.observer.fallback" || events[0].Code != "observer-exited" || events[0].AgentUID != "agent-abc234" || events[0].PaneUID != "pane-xyz567" {
		t.Fatalf("events = %+v, want exactly one codex.observer.fallback in the reported State directory", events)
	}
	if events := report(); len(events) != 0 {
		t.Fatalf("restoring the sink left it installed: %+v", events)
	}
}
