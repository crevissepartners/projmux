package diagnostics

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// coreMetadataStringConstants returns every string constant of typeName that
// the core metadata package declares outside tests. It reads the source rather
// than an enumerator, so a constant added without a journal row fails here.
func coreMetadataStringConstants(t *testing.T, typeName string) []string {
	t.Helper()
	dir := filepath.Join("..", "core", "metadata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read core metadata source: %v", err)
	}
	fset := token.NewFileSet()
	var values []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				declared, _ := value.Type.(*ast.Ident)
				for i, ident := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					expr := value.Values[i]
					typed := declared != nil && declared.Name == typeName
					if call, ok := expr.(*ast.CallExpr); ok && len(call.Args) == 1 {
						if fun, ok := call.Fun.(*ast.Ident); ok && fun.Name == typeName {
							expr, typed = call.Args[0], true
						}
					}
					if !typed {
						continue
					}
					literal, ok := expr.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("%s constant %s is not a string literal", typeName, ident.Name)
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("%s constant %s: %v", typeName, ident.Name, err)
					}
					values = append(values, unquoted)
				}
			}
		}
	}
	if len(values) == 0 {
		t.Fatalf("core metadata declares no %s constants", typeName)
	}
	slices.Sort(values)
	return values
}

func TestTeardownVocabularyMatchesCoreDecisionTable(t *testing.T) {
	t.Parallel()
	const prefix = "topology.teardown."
	var reasons []string
	for _, code := range teardownReasonCodes {
		reason, ok := strings.CutPrefix(string(code), prefix)
		if !ok || reason == "" {
			t.Fatalf("teardown code %q lacks the %q prefix", code, prefix)
		}
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)
	if want := coreMetadataStringConstants(t, "TeardownReason"); !slices.Equal(reasons, want) {
		t.Fatalf("teardown reason codes = %v, want every core TeardownReason exactly once: %v", reasons, want)
	}
	var decisions []string
	for _, decision := range teardownDecisions {
		decisions = append(decisions, string(decision))
	}
	slices.Sort(decisions)
	if want := coreMetadataStringConstants(t, "TeardownAction"); !slices.Equal(decisions, want) {
		t.Fatalf("teardown decisions = %v, want core TeardownAction %v", decisions, want)
	}
	var classifications []string
	for _, classification := range teardownClassifications {
		classifications = append(classifications, string(classification))
	}
	slices.Sort(classifications)
	if want := coreMetadataStringConstants(t, "TerminationClassification"); !slices.Equal(classifications, want) {
		t.Fatalf("teardown classifications = %v, want core TerminationClassification %v", classifications, want)
	}
}

func teardownFixtureEvent() Event {
	return Event{
		At: "2026-09-13T00:00:00Z", Level: "info", Component: "topology", Event: teardownDecisionEvent,
		Result: "success", RunID: "run", Version: "1.0.0", MuxBackend: "tmux",
		Decision: string(TeardownDecisionDeleteWindow), Code: string(TeardownReasonWindowTeardown),
		Classification: string(TeardownClassificationNormal), WindowUID: "win-abc234", PaneUID: "pane-xyz567",
	}
}

func TestTeardownDecisionEventClosedShapes(t *testing.T) {
	t.Parallel()
	base := teardownFixtureEvent()
	if _, err := sanitizeEvent(base, ""); err != nil {
		t.Fatal(err)
	}
	for _, decision := range teardownDecisions {
		for _, code := range teardownReasonCodes {
			event := base
			event.Decision, event.Code = string(decision), string(code)
			event.Classification, event.WindowUID, event.PaneUID = "", "", ""
			if _, err := sanitizeEvent(event, ""); err != nil {
				t.Fatalf("%s/%s: %v", decision, code, err)
			}
		}
	}
	for _, classification := range teardownClassifications {
		event := base
		event.Classification = string(classification)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s: %v", classification, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown decision":       func(e *Event) { e.Decision = "delete-project" },
		"missing decision":       func(e *Event) { e.Decision = "" },
		"unknown code":           func(e *Event) { e.Code = "topology.teardown.private-reason" },
		"bare reason":            func(e *Event) { e.Code = "window-teardown" },
		"topology agent code":    func(e *Event) { e.Code = string(TopologyAgentSessionRefMissing) },
		"runtime code":           func(e *Event) { e.Code = string(CodeTmuxApplyFailed) },
		"unknown classification": func(e *Event) { e.Classification = "exit 0" },
		"pane handle window":     func(e *Event) { e.WindowUID = "%9" },
		"window handle":          func(e *Event) { e.WindowUID = "@4" },
		"session handle":         func(e *Event) { e.PaneUID = "$1" },
		"pane uid as window":     func(e *Event) { e.WindowUID = "pane-xyz567" },
		"window uid as pane":     func(e *Event) { e.PaneUID = "win-abc234" },
		"legacy pane prefix":     func(e *Event) { e.PaneUID = "pan-alpha" },
		"empty uid body":         func(e *Event) { e.WindowUID = "win-" },
		"uppercase uid":          func(e *Event) { e.WindowUID = "win-ABC" },
		"path uid":               func(e *Event) { e.PaneUID = "pane-/tmp/sock" },
		"session name uid":       func(e *Event) { e.WindowUID = "win-work alpha" },
		"oversized uid":          func(e *Event) { e.WindowUID = "win-" + strings.Repeat("a", maxTeardownUIDLength) },
		"message":                func(e *Event) { e.Message = "socket /tmp/tmux-1000/projmux" },
		"command":                func(e *Event) { e.Command = "tmux" },
		"operation":              func(e *Event) { e.Operation = string(OperationSessionKill) },
		"source":                 func(e *Event) { e.Source = "manual" },
		"provider":               func(e *Event) { e.Provider = "codex" },
		"item count":             func(e *Event) { e.ItemCount = intPointer(1) },
		"topology count":         func(e *Event) { e.SkippedCount = intPointer(0) },
		"error level":            func(e *Event) { e.Level, e.Result, e.Kind = "error", "error", "runtime" },
		"started":                func(e *Event) { e.Result = "started" },
		"wrong component":        func(e *Event) { e.Component = "runtime" },
	} {
		t.Run(name, func(t *testing.T) {
			event := base
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}

	topology := fixtureEvent("run")
	topology.Command, topology.Subcommand = "", ""
	topology.Event, topology.Component = "topology.outcome", "topology"
	topology.ResumedCount, topology.SkippedCount = intPointer(0), intPointer(0)
	skipped := topology
	skipped.Event, skipped.Code = "topology.agent.skipped", string(TopologyAgentSessionRefMissing)
	skipped.ResumedCount, skipped.SkippedCount, skipped.ItemCount = nil, nil, intPointer(1)
	for name, family := range map[string]Event{
		"command.outcome": fixtureEvent("run"), "topology.outcome": topology, "topology.agent.skipped": skipped,
	} {
		if _, err := sanitizeEvent(family, ""); err != nil {
			t.Fatalf("%s base rejected: %v", name, err)
		}
		for field, mutate := range map[string]func(*Event){
			"decision":       func(e *Event) { e.Decision = string(TeardownDecisionRetain) },
			"classification": func(e *Event) { e.Classification = string(TeardownClassificationKilled) },
			"window_uid":     func(e *Event) { e.WindowUID = "win-abc234" },
			"pane_uid":       func(e *Event) { e.PaneUID = "pane-xyz567" },
		} {
			event := family
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("%s accepted teardown field %s", name, field)
			}
		}
	}

	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"%9", "@4", "$1", "socket", "session", "cwd", "argv", "message"} {
		if strings.Contains(string(data), prohibited) {
			t.Fatalf("teardown record carries %q: %s", prohibited, data)
		}
	}
}

func TestTeardownRecorderAppendsWithoutOutcomeOwnership(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "teardown-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC) }
	recorder := owner.Teardown()
	recorder.Record(TeardownDecisionRecord{
		Decision: TeardownDecisionDeleteWindow, Reason: TeardownReasonWindowTeardown,
		Classification: TeardownClassificationNormal, WindowUID: "win-abc234", PaneUID: "pane-xyz567",
	})
	// Closed-vocabulary violations drop the record; malformed UIDs are omitted.
	recorder.Record(TeardownDecisionRecord{Decision: "delete-project", Reason: TeardownReasonWindowTeardown})
	recorder.Record(TeardownDecisionRecord{Decision: TeardownDecisionRetain, Reason: "topology.teardown.private"})
	recorder.Record(TeardownDecisionRecord{Decision: TeardownDecisionRetain, Reason: TeardownReasonAwaitingPaneExit, Classification: "exit 0"})
	recorder.Record(TeardownDecisionRecord{
		Decision: TeardownDecisionRefuse, Reason: TeardownReasonStaleGeneration,
		Classification: TeardownClassificationUnknown, WindowUID: "@4", PaneUID: "pan-alpha-codex",
	})
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	first := events[0]
	if first.Event != teardownDecisionEvent || first.Component != "topology" || first.RunID != "teardown-run" ||
		first.Decision != "delete-window" || first.Code != "topology.teardown.window-teardown" ||
		first.Classification != "normal" || first.WindowUID != "win-abc234" || first.PaneUID != "pane-xyz567" ||
		first.At != "2026-09-13T01:02:03Z" {
		t.Fatalf("recorded = %+v", first)
	}
	if second := events[1]; second.Decision != "refuse" || second.WindowUID != "" || second.PaneUID != "" {
		t.Fatalf("malformed UIDs were projected: %+v", second)
	}
	if owner.RecordedOutcome() {
		t.Fatal("teardown decisions claimed the top-level outcome")
	}

	var absent *LifecycleRecorder
	absent.Teardown().Record(TeardownDecisionRecord{Decision: TeardownDecisionRetain, Reason: TeardownReasonAwaitingPaneExit})
	(&TeardownRecorder{}).Record(TeardownDecisionRecord{Decision: TeardownDecisionRetain, Reason: TeardownReasonAwaitingPaneExit})
	failing := &topologyFailWriter{}
	failingOwner := NewLifecycleRecorder(failing, "run", "1.0.0", "tmux")
	failingOwner.Teardown().Record(TeardownDecisionRecord{Decision: TeardownDecisionRetain, Reason: TeardownReasonAwaitingPaneExit})
	if failing.calls != 1 || failingOwner.RecordedOutcome() {
		t.Fatalf("failing writer calls=%d outcome=%t", failing.calls, failingOwner.RecordedOutcome())
	}
}
