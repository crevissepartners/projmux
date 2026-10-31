package app

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/version"
)

// observeCodexObserverFallbacks installs this process's journal for the native
// Codex observers an SDK create or resume started and did not find serving. The
// SDK reports to a process-wide sink because its create paths hold no journal
// and must not import the diagnostics package. Each report is written to the
// operations journal of the State directory that create used, not to the
// application's default one, as codex.observer.fallback. An invocation that must
// never append to the journal installs nothing. The returned function restores
// the previous sink.
func observeCodexObserverFallbacks(lifecycle *diagnostics.LifecycleRecorder, args []string) (restore func()) {
	if lifecycle == nil || diagnostics.JournalForbidden(args) {
		return func() {}
	}
	return mutationruntime.SetNativeObserverReportSink(recordCodexObserverFallback)
}

func recordCodexObserverFallback(report mutationruntime.NativeObserverReport) {
	if report.StateDir == "" || !filepath.IsAbs(report.StateDir) {
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return
	}
	store := diagnostics.NewStore(filepath.Join(report.StateDir, diagnostics.LogDirName, diagnostics.LogFileName))
	diagnostics.NewLifecycleRecorder(store, "sdk-observer-"+hex.EncodeToString(token[:]), version.String(), "tmux").CodexObserver().RecordFallback(report.AgentUID, report.PaneUID, report.Reason)
}
