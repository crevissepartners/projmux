package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// paneWriteHarness is a hook whose Pane is known and whose tmux writes fail.
// That is the exact shape the track found in the field: attribution succeeded,
// the reflection writes did not land, and the record still said `state`.
type paneWriteHarness struct {
	cmd      *aiCommand
	logPath  string
	attempts int
}

func newPaneWriteHarness(t *testing.T, writesFail bool) *paneWriteHarness {
	t.Helper()
	home := t.TempDir()
	stateHome := filepath.Join(home, "state")
	h := &paneWriteHarness{
		logPath: filepath.Join(stateHome, "projmux", aiIngestLogName),
	}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_STATE_HOME":
			return stateHome
		case "TMUX_PANE":
			return "%7"
		default:
			return ""
		}
	}
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		if name == "tmux" && len(args) > 0 && args[0] == "set-option" {
			h.attempts++
			if writesFail {
				// The transport text a broken route actually produces. None of
				// it may reach a record.
				return errors.New("exit status 1: no server running on /tmp/tmux-1000/default")
			}
		}
		return nil
	}
	h.cmd = cmd
	return h
}

func (h *paneWriteHarness) ingest(t *testing.T, route, payload string) error {
	t.Helper()
	h.cmd.stdin = strings.NewReader(payload)
	var stdout, stderr bytes.Buffer
	return h.cmd.runIngest([]string{route}, &stdout, &stderr)
}

func (h *paneWriteHarness) records(t *testing.T) []aiIngestLogEntry {
	t.Helper()
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		t.Fatalf("read %s: %v", h.logPath, err)
	}
	var entries []aiIngestLogEntry
	for _, line := range nonEmptyLines(string(data)) {
		var entry aiIngestLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode record %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// Acceptance 1: a reflection write that failed is not recorded as a delivery.
// Both spellings of "the hook did its job" are covered -- the state event that
// applies a status, and the quiet event whose only writes are the routing index
// markAIHookPane lays down.
func TestHookRecordRefusesToReportADeliveryThePaneWritesMissed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{
			name:    "state event",
			payload: `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`,
		},
		{
			name:    "quiet event",
			payload: `{"hook_event_name":"PreToolUse","session_id":"s-1","cwd":"/repo"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPaneWriteHarness(t, true)
			_ = h.ingest(t, "claude-hook", tc.payload)

			records := h.records(t)
			if len(records) == 0 {
				t.Fatal("no record was written at all")
			}
			last := records[len(records)-1]
			if last.Result != "error" {
				t.Fatalf("Result = %q, want error; the writes never landed", last.Result)
			}
			if last.Reason != aiPaneWriteReasonUnavailable {
				t.Fatalf("Reason = %q, want %q", last.Reason, aiPaneWriteReasonUnavailable)
			}
			if last.Pane != "%7" {
				t.Fatalf("Pane = %q, want %%7; attribution still succeeded", last.Pane)
			}
		})
	}
}

// Acceptance 4: nothing changes when the writes land. Same result word, same
// empty reason, same number of attempted writes.
func TestHookRecordIsUnchangedWhenThePaneWritesLand(t *testing.T) {
	failing := newPaneWriteHarness(t, true)
	_ = failing.ingest(t, "claude-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`)

	h := newPaneWriteHarness(t, false)
	if err := h.ingest(t, "claude-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	records := h.records(t)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Result != "state" || records[0].Reason != "" {
		t.Fatalf("record = %+v, want a plain state record", records[0])
	}
	if h.attempts != failing.attempts {
		t.Fatalf("attempted writes = %d with a live route and %d with a broken one; the sequence of attempts must not change", h.attempts, failing.attempts)
	}
}

// Acceptance 2: the reason is a vocabulary token. The transport error carries an
// exit status and a socket path, and neither may survive into a record.
func TestPaneWriteFailureReasonIsAClosedTokenCarryingNoOpaqueValue(t *testing.T) {
	transport := errors.New("exit status 1: no server running on /tmp/tmux-1000/default")
	classified := classifyAIPaneWrite(transport)
	if !errors.Is(classified, errAIPaneWriteUnavailable) {
		t.Fatalf("classifyAIPaneWrite(%v) = %v, want the vocabulary token", transport, classified)
	}
	if classified.Error() != aiPaneWriteReasonUnavailable {
		t.Fatalf("token = %q, want %q", classified.Error(), aiPaneWriteReasonUnavailable)
	}
	for _, forbidden := range []string{"exit status", "/", "tmux-", "no server"} {
		if strings.Contains(aiPaneWriteReasonUnavailable, forbidden) {
			t.Fatalf("token %q carries the opaque fragment %q", aiPaneWriteReasonUnavailable, forbidden)
		}
	}

	h := newPaneWriteHarness(t, true)
	_ = h.ingest(t, "claude-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`)
	raw, err := os.ReadFile(h.logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, forbidden := range []string{"exit status", "/tmp/tmux-", "no server running"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("log leaked the opaque fragment %q:\n%s", forbidden, raw)
		}
	}
}

// The ledger belongs to the hook invocation that filled it. The observer writes
// into the same file from a process that lives for hours, so its records must
// never inherit a colour from somewhere else.
func TestObserverRecordsAreNotColouredByAReflectionWriteFailure(t *testing.T) {
	h := newPaneWriteHarness(t, true)
	h.cmd.noteAIPaneWriteFailure(errAIPaneWriteUnavailable)
	h.cmd.appendAIIngestLog(aiIngestLogEntry{
		Source: aiIngestCodexObserverSource, Event: "connected", Result: "provider-control-plane", Pane: "%7",
	})

	records := h.records(t)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Result != "provider-control-plane" || records[0].Reason != "" {
		t.Fatalf("observer record = %+v, want it untouched", records[0])
	}
}

// Acceptance 3: the three sources below hold no write whose error is discarded.
// This is the check that has to keep working after the fix, because the failure
// it guards against is invisible by construction: a discarded error produces a
// record that says the hook succeeded.
//
// Baseline at the time this test was written: 47 discarded writes, `ai.go` 36
// and `ai_ingest.go` 11.
//
// The scan list is exactly the three sources this change owns, and the name says
// so. It is deliberately not the package: the package-wide equivalent reads every
// non-test file and belongs to the recurrence gate, which owns that list. Green
// here is a statement about these three files and nothing wider -- a test whose
// name outruns what it reads is the same false surface this change removes.
func TestHookIngestSourcesNeverDiscardAReflectionWriteError(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	dir := filepath.Dir(thisFile)

	// Every helper below returns an error a caller is expected to carry. `run`
	// is the raw transport the discarded writes used to spell inline.
	discardable := map[string]bool{
		"run":               true,
		"setAIPaneOption":   true,
		"clearAIPaneOption": true,
	}

	writeCall := func(call *ast.CallExpr) (string, bool) {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !discardable[selector.Sel.Name] {
			return "", false
		}
		if receiver, ok := selector.X.(*ast.Ident); !ok || receiver.Name != "c" {
			return "", false
		}
		if selector.Sel.Name != "run" {
			return selector.Sel.Name, true
		}
		if len(call.Args) == 0 {
			return "", false
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || value != "tmux" {
			return "", false
		}
		return "run(tmux)", true
	}

	fset := token.NewFileSet()
	for _, name := range []string{"ai.go", "ai_ingest.go", "ai_pane_write.go"} {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var call *ast.CallExpr
			switch stmt := node.(type) {
			case *ast.AssignStmt:
				if len(stmt.Rhs) != 1 {
					return true
				}
				blank := false
				for _, target := range stmt.Lhs {
					if ident, ok := target.(*ast.Ident); ok && ident.Name == "_" {
						blank = true
					}
				}
				if !blank {
					return true
				}
				call, _ = stmt.Rhs[0].(*ast.CallExpr)
			case *ast.ExprStmt:
				call, _ = stmt.X.(*ast.CallExpr)
			default:
				return true
			}
			if call == nil {
				return true
			}
			if verb, ok := writeCall(call); ok {
				t.Errorf("%s:%d discards the error of %s; a reflection write that fails must be carried, not dropped",
					name, fset.Position(call.Pos()).Line, verb)
			}
			return true
		})
	}
}
