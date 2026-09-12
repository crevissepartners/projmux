package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// A synthetic response crosses the real Client request/catalog error wrapper.
// It is not a recorded provider conversation or a live manager observation.
func syntheticDiagnosticFailure(ctx context.Context, mode string) error {
	local, peer := net.Pipe()
	client := codexappserver.NewClient(local)
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer peer.Close()
		_, _ = bufio.NewReader(peer).ReadString('\n')
		switch mode {
		case "protocol":
			_, _ = fmt.Fprintln(peer, `{broken`)
		case "transport":
		default:
			code := -32601
			if mode == "catalog" {
				code = -32602
			}
			message, _ := json.Marshal("auth=secret Cookie: secret /private/path\n\x1b[31m" + strings.Repeat("secret", 1000) + "\xff")
			_, _ = fmt.Fprintf(peer, "{\"id\":1,\"error\":{\"code\":%d,\"message\":%s}}\n", code, message)
		}
	}()
	var err error
	if mode == "catalog" {
		_, err = client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{})
	} else {
		err = client.Request(ctx, "thread/read", nil, nil)
	}
	<-done
	return fmt.Errorf("synthetic outer boundary: %w", err)
}

type diagnosticOwnedEndpoint struct {
	peer codexappserver.PeerIdentity
	mode string
}

func (e diagnosticOwnedEndpoint) PeerIdentity() codexappserver.PeerIdentity { return e.peer }
func (e diagnosticOwnedEndpoint) Close() error                              { return nil }
func (e diagnosticOwnedEndpoint) ReadLifecycleSnapshot(ctx context.Context, _ string) (codexappserver.LifecycleSnapshot, error) {
	return codexappserver.LifecycleSnapshot{}, syntheticDiagnosticFailure(ctx, e.mode)
}

func TestObserverFallbackJournalPreservesSafeFailureAndClosedStartup(t *testing.T) {
	for _, mode := range []string{"unsupported", "catalog", "protocol", "transport"} {
		t.Run(mode, func(t *testing.T) {
			failure := syntheticDiagnosticFailure(t.Context(), mode)
			expected := codexappserver.Diagnostic(failure)
			sink := newRecordingCodexLifecycleSink()
			records := &recordingCodexObserverJournal{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var startup codexObserverStartupResult
			observer := codexNativeObserver{identity: testCodexLifecycleIdentity(), sink: sink,
				open:          func(context.Context) (codexLifecycleConnection, error) { return nil, failure },
				transitions:   newCodexObserverLogJournal(records.append, nil),
				reportStartup: func(r codexObserverStartupResult) { startup = r; cancel() },
			}
			if err := observer.Run(ctx); err != nil {
				t.Fatal(err)
			}
			entries := records.snapshot()
			if len(entries) != 1 || entries[0].Failure == nil || entries[0].Failure.String() != expected.String() {
				t.Fatalf("lost observer cause: %+v", entries)
			}
			if startup.Status != codexObserverStartupFallback || startup.Reason != string(codexNativeReason(failure)) {
				t.Fatalf("startup reason changed: %+v", startup)
			}
			wire := fmt.Sprintf("%s fallback %s\n", codexObserverStartupPrefix, startup.Reason)
			if _, ok := parseCodexObserverStartupLine(wire); !ok {
				t.Fatal("old startup parser rejected fallback")
			}
			body, _ := json.Marshal(entries[0])
			if len(body) > maxCodexObserverFailureRecordBytes || strings.Contains(string(body), "secret") {
				t.Fatal("observer diagnostic leaked or exceeded bound")
			}
			// Same old reason with a different request cause must remain distinguishable.
			journal := newCodexObserverLogJournal(records.append, func() time.Time { return time.Unix(0, 0) })
			journal.RecordObserverFailure(observer.identity, codexObserverReasonUnsupported, expected)
			journal.RecordObserverFailure(observer.identity, codexObserverReasonUnsupported, codexappserver.Diagnostic(syntheticDiagnosticFailure(t.Context(), "catalog")))
		})
	}
}

func TestObserverDiagnosticRejectedAuthorityWritesZero(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		sink := &diagnosticRejectingSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), replaced: replaced}
		records := &recordingCodexObserverJournal{}
		observer := codexNativeObserver{identity: testCodexLifecycleIdentity(), sink: sink, transitions: newCodexObserverLogJournal(records.append, nil)}
		observer.setStartupFallback(codexObserverReasonUnsupported, syntheticDiagnosticFailure(t.Context(), "unsupported"))
		if len(records.snapshot()) != 0 || len(sink.authorities) != 0 {
			t.Fatal("uncommitted failure wrote diagnostic/authority")
		}
	}
}

func TestObserverSessionFailurePreservesTerminalReasonAfterAuthorityRejection(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		name := "current"
		if replaced {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			sink := &diagnosticRejectingSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), replaced: replaced}
			records := &recordingCodexObserverJournal{}
			var results []codexObserverStartupResult
			observer := codexNativeObserver{identity: testCodexLifecycleIdentity(), sink: sink,
				transitions:   newCodexObserverLogJournal(records.append, nil),
				reportStartup: func(result codexObserverStartupResult) { results = append(results, result) },
			}
			observer.setSessionStartupFallback(syntheticDiagnosticFailure(t.Context(), "catalog"))
			want := codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "unsupported"}
			if replaced {
				want = codexObserverStartupResult{Status: codexObserverStartupStale}
			}
			if len(results) != 1 || results[0] != want {
				t.Errorf("session-construction failure lost terminal result: got %+v, want %+v", results, want)
			}
			if len(records.snapshot()) != 0 || len(sink.authorities) != 0 {
				t.Fatal("uncommitted session failure wrote diagnostic/authority")
			}
		})
	}
}

func TestAIIngestTextPreservesBoundedRecoveryDiagnosticsAndLegacyRecords(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_recovery_diagnostic.json.golden")
	if err != nil {
		t.Fatal(err)
	}
	var entry aiIngestLogEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	entry.At = "2026-09-13T00:00:00Z"
	legacy := entry
	legacy.Failure, legacy.Recovery = nil, nil
	const oldText = "2026-09-13T00:00:00Z codex-observer observer.fallback provider-hook pane=%9 thread=thread-1 reason=unsupported"
	if got := formatAIIngestLogEntry(legacy); got != oldText {
		t.Fatalf("old journal record changed: %s", got)
	}
	recovery, _ := json.Marshal(entry.Recovery)
	want := oldText + " failure=" + entry.Failure.String() + " recovery=" + string(recovery)
	if got := formatAIIngestLogEntry(entry); got != want {
		t.Errorf("public journal text lost safe diagnostics: %s", got)
	}
	for _, unsafe := range []string{"prompt-secret auth=secret Cookie: secret /private/path pid=12345", "\x00\n\x1b\xff", strings.Repeat("secret", 100000)} {
		entry.Failure = &codexappserver.FailureDiagnostic{Method: unsafe, Cause: unsafe}
		entry.Recovery = &codexappserver.RecoveryDiagnostic{Evidence: &codexappserver.ManagerEvidence{Status: unsafe, Backend: unsafe, Result: unsafe, Agreement: unsafe, Version: unsafe}, Ownership: codexappserver.ManagerOwnership(unsafe), Refusal: codexappserver.NativeActionRefusal(unsafe), Operator: codexappserver.OperatorRecovery(unsafe)}
		got := formatAIIngestLogEntry(entry)
		recovery, _ := json.Marshal(entry.Recovery)
		want := oldText + " failure=" + entry.Failure.String() + " recovery=" + string(recovery)
		if got != want || len(got)-len(oldText) > 947 || len(got) > maxCodexObserverFailureRecordBytes || strings.ContainsAny(got, "\x00\n\x1b\xff") || strings.Contains(got, "secret") || strings.Contains(got, "12345") {
			t.Errorf("public journal text lost bounded safe projection")
		}
	}
}

type diagnosticRejectingSink struct {
	*recordingCodexLifecycleSink
	replaced bool
}

func (s *diagnosticRejectingSink) SetAuthority(codexLifecycleIdentity, string, string, string) error {
	if s.replaced {
		s.setCurrent(false)
	}
	return fmt.Errorf("synthetic authority rejection")
}

func TestRecoveryDiagnosticGoldenInventoryAndBounds(t *testing.T) {
	health := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "0.151.0", codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
	health.ManagerEvidence = &codexappserver.ManagerEvidence{Status: "running", Backend: "pid", Result: "observed", Agreement: "contradictory", Version: "0.154.0"}
	health.ManagerOwnership = codexappserver.ManagerUnknown
	health.NativeRefusal = codexappserver.NativeActionRefusalEvidenceContradictory
	health.OperatorRecovery = codexappserver.OperatorRecoveryInspectProcessOwnership
	failure := codexappserver.WithHealthDiagnostic(syntheticDiagnosticFailure(t.Context(), "catalog"), health)
	records := &recordingCodexObserverJournal{}
	observer := codexNativeObserver{identity: testCodexLifecycleIdentity(), sink: newRecordingCodexLifecycleSink(), transitions: newCodexObserverLogJournal(records.append, nil)}
	observer.setStartupFallback(codexObserverReasonUnsupported, failure)
	raw, err := json.MarshalIndent(records.snapshot()[0], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/codex_recovery_diagnostic.json.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw)+"\n" != string(expected) {
		t.Fatalf("diagnostic golden differs: %s", raw)
	}
	var text bytes.Buffer
	writeDoctorAppServerText(&text, &health)
	if text.Len() > 4096 || !strings.Contains(text.String(), "agreement contradictory") || !strings.Contains(text.String(), health.OperatorRecovery.Guidance()) {
		t.Fatal("doctor projection lost evidence or exceeded bound")
	}
	for _, tt := range []struct {
		value  any
		fields []string
	}{
		{codexappserver.FailureDiagnostic{}, []string{"Method", "RPCCode", "Cause"}},
		{codexappserver.ManagerEvidence{}, []string{"Status", "Backend", "Result", "Agreement", "Version"}},
		{codexappserver.RecoveryDiagnostic{}, []string{"Evidence", "Ownership", "Refusal", "Operator"}},
	} {
		typ := reflect.TypeOf(tt.value)
		var fields []string
		for i := range typ.NumField() {
			fields = append(fields, typ.Field(i).Name)
		}
		if !reflect.DeepEqual(fields, tt.fields) {
			t.Fatalf("diagnostic field inventory drift: %v", fields)
		}
	}
	oversizedRecords := &recordingCodexObserverJournal{}
	oversized := testCodexLifecycleIdentity()
	oversized.ThreadID = strings.Repeat("x", maxCodexObserverFailureRecordBytes)
	newCodexObserverLogJournal(oversizedRecords.append, nil).RecordObserverFailure(oversized, codexObserverReasonUnsupported, codexappserver.Diagnostic(failure))
	if len(oversizedRecords.snapshot()) != 0 {
		t.Fatal("oversized enriched journal row was emitted")
	}
	for _, unsafe := range []string{"prompt-secret auth=secret Cookie: secret /private/path", "secret/0.154.0", "codex-cli/codex_cli_rs/0.154.0", "\x00\n\x1b\xff", strings.Repeat("secret", 100000)} {
		d := codexappserver.RecoveryDiagnostic{Evidence: &codexappserver.ManagerEvidence{Status: unsafe, Backend: unsafe, Result: unsafe, Agreement: unsafe, Version: unsafe}, Ownership: codexappserver.ManagerOwnership(unsafe), Refusal: codexappserver.NativeActionRefusal(unsafe), Operator: codexappserver.OperatorRecovery(unsafe)}
		raw, err := json.Marshal(d)
		if err != nil || len(raw) > codexappserver.MaxRecoveryDiagnosticBytes || strings.Contains(string(raw), "secret") {
			t.Fatal("unsafe recovery projection")
		}
		h := codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, unsafe, codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true)
		raw, _ = json.Marshal(h)
		if len(raw) > 4096 || strings.Contains(string(raw), "secret") {
			t.Fatal("provider version leaked through health")
		}
	}
}
