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

// failEveryPaneWrite and failOnlyMarkerWrites are the two shapes the field
// produced. The second is the live one: the routed reflection lands while the
// unrouted markers do not.
func failEveryPaneWrite(string) bool { return true }

func failOnlyMarkerWrites(option string) bool {
	switch option {
	case aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption,
		attentionAckOption, attentionFocusArmedOption:
		return false
	}
	return true
}

func newPaneWriteHarness(t *testing.T, fails func(option string) bool) *paneWriteHarness {
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
			option := args[len(args)-1]
			if len(args) >= 2 && args[len(args)-2] != "-t" && strings.HasPrefix(args[len(args)-2], "@") {
				option = args[len(args)-2]
			}
			if fails != nil && fails(option) {
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

// Acceptance 1: a write that failed is not recorded as a delivery. The three
// rows are the three shapes an operator meets. The last one is the live shape:
// the reflection lands through a working route while the markers, written
// without it, do not -- and the record used to say `state` anyway.
func TestHookRecordRefusesToReportADeliveryThePaneWritesMissed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fails   func(string) bool
		payload string
		reason  string
	}{
		{
			name:    "state event, nothing lands",
			fails:   failEveryPaneWrite,
			payload: `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`,
			reason:  aiPaneWriteReasonUnavailable,
		},
		{
			name:    "quiet event, only markers were ever written",
			fails:   failEveryPaneWrite,
			payload: `{"hook_event_name":"PreToolUse","session_id":"s-1","cwd":"/repo"}`,
			reason:  aiPaneWriteReasonMarkerUnavailable,
		},
		{
			name:    "state event, reflection lands and markers do not",
			fails:   failOnlyMarkerWrites,
			payload: `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`,
			reason:  aiPaneWriteReasonMarkerUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPaneWriteHarness(t, tc.fails)
			_ = h.ingest(t, "claude-hook", tc.payload)

			records := h.records(t)
			if len(records) == 0 {
				t.Fatal("no record was written at all")
			}
			last := records[len(records)-1]
			if last.Result != "error" {
				t.Fatalf("Result = %q, want error; a write never landed", last.Result)
			}
			if string(last.Reason) != tc.reason {
				t.Fatalf("Reason = %q, want %q", last.Reason, tc.reason)
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
	failing := newPaneWriteHarness(t, failEveryPaneWrite)
	_ = failing.ingest(t, "claude-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/repo"}`)

	h := newPaneWriteHarness(t, nil)
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
	for _, token := range []string{aiPaneWriteReasonUnavailable, aiPaneWriteReasonMarkerUnavailable} {
		for _, forbidden := range []string{"exit status", "/", "tmux-", "no server"} {
			if strings.Contains(token, forbidden) {
				t.Fatalf("token %q carries the opaque fragment %q", token, forbidden)
			}
		}
	}

	h := newPaneWriteHarness(t, failEveryPaneWrite)
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
	h := newPaneWriteHarness(t, failEveryPaneWrite)
	h.cmd.noteAIPaneMarkerWriteFailure(errAIPaneWriteUnavailable)
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
