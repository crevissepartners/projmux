package runtime

import "sync/atomic"

// NativeObserverReport is what a creator knows about a native Codex observer
// it started and that is not (yet) serving: the explicit State directory the
// create wrote to, the opaque Agent and Pane identity, and one closed Reason.
// It carries no stderr, environment, path, thread id or wrapped cause.
type NativeObserverReport struct {
	StateDir, AgentUID, PaneUID, Reason string
}

// The closed reasons a creator reports. They are the reasons a start can end
// with that the creator itself saw, plus one for everything the observer
// child committed itself, whose own wording is never repeated.
const (
	NativeObserverReasonStartFailed = "observer-start-failed"
	NativeObserverReasonExited      = "observer-exited"
	NativeObserverReasonTimeout     = "observer-timeout"
	NativeObserverReasonReported    = "observer-reported"
)

// nativeObserverReportSink receives every non-serving report of this process.
// It is process-wide, like the application's Codex broker refusal journal,
// because the create paths hold no journal of their own; nil records nothing.
var nativeObserverReportSink atomic.Pointer[func(NativeObserverReport)]

// SetNativeObserverReportSink installs the process's sink and returns a
// function that restores the previous one.
func SetNativeObserverReportSink(sink func(NativeObserverReport)) (restore func()) {
	var next *func(NativeObserverReport)
	if sink != nil {
		next = &sink
	}
	previous := nativeObserverReportSink.Swap(next)
	return func() { nativeObserverReportSink.Store(previous) }
}

// nativeObserverDiagnostic maps one startup receipt to its closed reason and
// fixed notice. A ready or stale receipt has none. A receipt the child
// committed, a retry and a timeout are stated as pending or reported, never as
// a dead observer: the creator does not know more than that. The mapping is
// total over the closed reasons; any other wording becomes observer-reported.
func nativeObserverDiagnostic(result NativeObserverStartup) (reason, notice string, ok bool) {
	switch result.Status {
	case "fallback":
		reason = NativeObserverReasonReported
		if !result.Committed && (result.Reason == NativeObserverReasonStartFailed || result.Reason == NativeObserverReasonExited) {
			reason = result.Reason
			return reason, "Codex observer is not serving (" + reason + "); the Agent keeps its provider hook as its authority.", true
		}
		return reason, "Codex observer reported a fallback (" + reason + "); the Agent keeps its provider hook as its authority.", true
	case "retrying":
		reason = NativeObserverReasonReported
		if !result.Committed && result.Reason == NativeObserverReasonTimeout {
			reason = NativeObserverReasonTimeout
		}
		return reason, "Codex observer start is pending or retrying (" + reason + "); the Agent keeps its provider hook as its authority until it reports ready.", true
	}
	return "", "", false
}

// ReportNativeObserverStartup returns the fixed notice for a start that is not
// serving and hands the closed report to the process's sink, once.
func ReportNativeObserverStartup(stateDir string, id NativeObserverIdentity, result NativeObserverStartup) []string {
	reason, notice, ok := nativeObserverDiagnostic(result)
	if !ok {
		return nil
	}
	if sink := nativeObserverReportSink.Load(); sink != nil {
		(*sink)(NativeObserverReport{StateDir: stateDir, AgentUID: id.AgentUID, PaneUID: id.PaneUID, Reason: reason})
	}
	return []string{notice}
}
