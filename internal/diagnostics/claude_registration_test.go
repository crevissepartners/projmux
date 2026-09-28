package diagnostics

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func claudeRegistrationFixture() Event {
	return Event{At: "2026-09-29T00:00:00Z", Level: "error", Component: "agent", Event: claudeRegistrationEvent, Result: "error",
		Kind: "runtime", RunID: "run", Version: "1.0.0", MuxBackend: "tmux", DurationMS: 12,
		Source: string(ClaudeRegistrationSourceHelper), Code: ClaudeRegistrationProducerMismatch.Code()}
}

// TestClaudeRegistrationReasonTableIsClosedAndUnique pins the table as the one
// authority: unique reasons and codes, every reason recordable by at least one
// source, and exactly one ready reason.
func TestClaudeRegistrationReasonTableIsClosedAndUnique(t *testing.T) {
	t.Parallel()
	codes := map[string]bool{}
	ready := 0
	for _, spec := range claudeRegistrationReasonTable {
		code := spec.reason.Code()
		if codes[code] {
			t.Fatalf("duplicate code %q", code)
		}
		codes[code] = true
		if spec.reason == "" || strings.TrimSpace(string(spec.reason)) != string(spec.reason) || strings.ContainsAny(string(spec.reason), " ./") {
			t.Fatalf("reason %q is not a single closed token", spec.reason)
		}
		if spec.sources == 0 || spec.stage == 0 {
			t.Fatalf("reason %q has no source or stage", spec.reason)
		}
		if spec.stage == claudeRegistrationAdmitted {
			ready++
		}
		if (spec.stage == claudeRegistrationAdmitted || spec.stage == claudeRegistrationEnded) && spec.sources != claudeRegistrationFromHelper {
			t.Fatalf("reason %q: only the helper records Ready and its end", spec.reason)
		}
		if spec.reason.Refusal() != (spec.stage == claudeRegistrationRefused) || spec.reason.Recorded() != (spec.stage != claudeRegistrationOutside) {
			t.Fatalf("reason %q: Refusal or Recorded disagrees with the table", spec.reason)
		}
	}
	if ready != 1 {
		t.Fatalf("ready reasons = %d, want one", ready)
	}
	// The declared constants and the table are the same set, so a constant
	// cannot exist that the recorder would silently drop.
	file, err := parser.ParseFile(token.NewFileSet(), "claude_registration.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if ident, ok := value.Type.(*ast.Ident); ok && ident.Name == "ClaudeRegistrationReason" {
				for _, literal := range value.Values {
					reason, err := strconv.Unquote(literal.(*ast.BasicLit).Value)
					if err != nil {
						t.Fatal(err)
					}
					declared[reason] = true
				}
			}
		}
	}
	if len(declared) != len(claudeRegistrationReasonTable) {
		t.Fatalf("declared %d reasons, table has %d", len(declared), len(claudeRegistrationReasonTable))
	}
	for _, spec := range claudeRegistrationReasonTable {
		if !declared[string(spec.reason)] {
			t.Fatalf("table reason %q is not a declared constant", spec.reason)
		}
	}
	if ClaudeRegistrationReason("not-a-reason").Refusal() || claudeRegistrationProceedForTest.Refusal() {
		t.Fatal("a reason outside the table reports a refusal")
	}
}

// claudeRegistrationProceedForTest is the app's zero reason.
const claudeRegistrationProceedForTest ClaudeRegistrationReason = ""

// TestClaudeRegistrationValidatorAcceptsExactlyTheTable pins that every table
// reason is accepted with each source it allows, in the stage's level and
// result, and that nothing else passes.
func TestClaudeRegistrationValidatorAcceptsExactlyTheTable(t *testing.T) {
	t.Parallel()
	for _, spec := range claudeRegistrationReasonTable {
		if spec.stage == claudeRegistrationOutside {
			// Never journaled: rejected at every level and from every source.
			for _, source := range []string{"hook", "helper"} {
				for _, shape := range [][3]string{{"error", "error", "runtime"}, {"info", "success", ""}} {
					event := claudeRegistrationFixture()
					event.Source, event.Code = source, spec.reason.Code()
					event.Level, event.Result, event.Kind = shape[0], shape[1], shape[2]
					if _, err := sanitizeEvent(event, ""); err == nil {
						t.Fatalf("unrecordable %s accepted from %s as %v", spec.reason, source, shape)
					}
				}
			}
			continue
		}
		for _, source := range []ClaudeRegistrationSource{ClaudeRegistrationSourceHook, ClaudeRegistrationSourceHelper} {
			event := claudeRegistrationFixture()
			event.Source, event.Code = string(source), spec.reason.Code()
			if spec.stage != claudeRegistrationRefused {
				event.Level, event.Result, event.Kind = "info", "success", ""
			}
			_, err := sanitizeEvent(event, "")
			if allowed := spec.sources.allows(source); allowed != (err == nil) {
				t.Fatalf("%s from %s: allowed=%v err=%v", spec.reason, source, allowed, err)
			}
			if !spec.sources.allows(source) {
				continue
			}
			flipped := event
			if spec.stage == claudeRegistrationRefused {
				flipped.Level, flipped.Result, flipped.Kind = "info", "success", ""
			} else {
				flipped.Level, flipped.Result, flipped.Kind = "error", "error", "runtime"
			}
			if _, err := sanitizeEvent(flipped, ""); err == nil {
				t.Fatalf("%s accepted the other stage's level and result", spec.reason)
			}
		}
	}
	for name, mutate := range map[string]func(*Event){
		"with uids":      func(e *Event) { e.AgentUID, e.PaneUID = "agent-abc234", "pane-xyz567" },
		"agent uid only": func(e *Event) { e.AgentUID = "agent-abc234" },
		"zero duration":  func(e *Event) { e.DurationMS = 0 },
		"hook source":    func(e *Event) { e.Source, e.Code = "hook", ClaudeRegistrationHookInputUnreadable.Code() },
		"shared reason":  func(e *Event) { e.Source, e.Code = "hook", ClaudeRegistrationRegistryUnreadable.Code() },
		"helper shared":  func(e *Event) { e.Code = ClaudeRegistrationRegistryUnreadable.Code() },
		"ready": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "info", "success", "", ClaudeRegistrationReady.Code()
		},
		"ended not current": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Code = "info", "success", "", ClaudeRegistrationEndedNotCurrent.Code()
		},
	} {
		event := claudeRegistrationFixture()
		mutate(&event)
		if _, err := sanitizeEvent(event, ""); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"unknown reason":      func(e *Event) { e.Code = "claude.registration.nope" },
		"bare reason":         func(e *Event) { e.Code = string(ClaudeRegistrationProducerMismatch) },
		"foreign prefix":      func(e *Event) { e.Code = "registry.lock.timeout" },
		"no code":             func(e *Event) { e.Code = "" },
		"prefix only":         func(e *Event) { e.Code = claudeRegistrationCodePrefix },
		"no source":           func(e *Event) { e.Source = "" },
		"unknown source":      func(e *Event) { e.Source = "supervisor" },
		"hook-only reason":    func(e *Event) { e.Code = ClaudeRegistrationHookArguments.Code() },
		"helper-only on hook": func(e *Event) { e.Source = "hook" },
		"ready from hook": func(e *Event) {
			e.Level, e.Result, e.Kind, e.Source, e.Code = "info", "success", "", "hook", ClaudeRegistrationReady.Code()
		},
		"error without kind": func(e *Event) { e.Kind = "" },
		"usage kind":         func(e *Event) { e.Kind = "usage" },
		"started":            func(e *Event) { e.Result = "started" },
		"warn":               func(e *Event) { e.Level = "warn" },
		"component":          func(e *Event) { e.Component = "cli" },
		"message":            func(e *Event) { e.Message = "claude registration refused" },
		"command":            func(e *Event) { e.Command = "claude-endpoint-helper" },
		"operation":          func(e *Event) { e.Operation = string(OperationSessionCreate) },
		"window uid":         func(e *Event) { e.WindowUID = "win-abc234" },
		"tmux pane handle":   func(e *Event) { e.PaneUID = "%4" },
		"agent uid shape":    func(e *Event) { e.AgentUID = "agt-alpha" },
		"decision":           func(e *Event) { e.Decision = string(TeardownDecisionRetain) },
		"classification":     func(e *Event) { e.Classification = string(TeardownClassificationNormal) },
		"wait":               func(e *Event) { e.WaitMS = registryLockMS(1) },
		"lock hold":          func(e *Event) { e.LockHeldMS = registryLockMS(1) },
		"create phase":       func(e *Event) { e.PhaseGuardMS = registryLockMS(1) },
		"count":              func(e *Event) { e.ItemCount = intPointer(1) },
		"provider":           func(e *Event) { e.Provider = string(ProviderClaude) },
		"ai failure":         func(e *Event) { e.Failure = string(AIFailureRoute) },
		"resource result":    func(e *Event) { e.ResourceResult = string(ResourceResultError) },
	} {
		t.Run(name, func(t *testing.T) {
			event := claudeRegistrationFixture()
			mutate(&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("accepted %+v", event)
			}
		})
	}
}

// TestClaudeRegistrationMarksAreRejectedOnEveryOtherFamily pins that no other
// family accepts this family's sources or codes. The family adds no Event
// field, so its sources and codes are its only new marks.
func TestClaudeRegistrationMarksAreRejectedOnEveryOtherFamily(t *testing.T) {
	t.Parallel()
	fixtures := otherFamilyFixtures()
	fixtures["registry lock"] = registryLockFixture()
	for name, base := range fixtures {
		if _, err := sanitizeEvent(base, ""); err != nil {
			t.Fatalf("%s fixture rejected: %v", name, err)
		}
		marks := map[string]func(*Event){}
		for _, source := range []ClaudeRegistrationSource{ClaudeRegistrationSourceHook, ClaudeRegistrationSourceHelper} {
			marks["source "+string(source)] = func(e *Event) { e.Source = string(source) }
		}
		for _, spec := range claudeRegistrationReasonTable {
			marks["code "+string(spec.reason)] = func(e *Event) { e.Code = spec.reason.Code() }
		}
		for _, mark := range slices.Sorted(maps.Keys(marks)) {
			event := base
			marks[mark](&event)
			if _, err := sanitizeEvent(event, ""); err == nil {
				t.Fatalf("%s accepted %s", name, mark)
			}
		}
	}
}

// TestClaudeRegistrationRecorderWritesTheClosedShape records one record per
// stage and pins the serialized keys to the family's allowlist.
func TestClaudeRegistrationRecorderWritesTheClosedShape(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "claude-registration-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC) }
	recorder := owner.ClaudeRegistration()
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHook, Reason: ClaudeRegistrationPaneBindingMismatch, Duration: 1500 * time.Microsecond})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHelper, Reason: ClaudeRegistrationReady, Duration: 2 * time.Second,
		AgentUID: "agent-abc234", PaneUID: "pane-xyz567"})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHelper, Reason: ClaudeRegistrationEndedNotCurrent, Duration: -time.Second,
		AgentUID: "agent-abc234", PaneUID: "%4"})
	// Every record outside the closed set is dropped.
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHook, Reason: ClaudeRegistrationReady})
	recorder.Record(ClaudeRegistrationRecord{Source: "supervisor", Reason: ClaudeRegistrationPaneBindingMismatch})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHelper, Reason: "invented"})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHelper})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHook, Reason: ClaudeRegistrationUnmanagedSession})
	events, err := store.Read()
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	want := []Event{
		{Level: "error", Result: "error", Kind: "runtime", Source: "hook", Code: "claude.registration.pane-binding-mismatch", DurationMS: 1},
		{Level: "info", Result: "success", Source: "helper", Code: "claude.registration.ready", DurationMS: 2000, AgentUID: "agent-abc234", PaneUID: "pane-xyz567"},
		{Level: "info", Result: "success", Source: "helper", Code: "claude.registration.ended-not-current", DurationMS: 0, AgentUID: "agent-abc234"},
	}
	for i, got := range events {
		w := want[i]
		if got.Event != claudeRegistrationEvent || got.Component != "agent" || got.RunID != "claude-registration-run" ||
			got.Level != w.Level || got.Result != w.Result || got.Kind != w.Kind || got.Source != w.Source || got.Code != w.Code ||
			got.DurationMS != w.DurationMS || got.AgentUID != w.AgentUID || got.PaneUID != w.PaneUID || got.Message != "" || got.Command != "" {
			t.Fatalf("record %d = %+v, want %+v", i, got, w)
		}
		line, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]any
		if err := json.Unmarshal(line, &keys); err != nil {
			t.Fatal(err)
		}
		allowed := []string{"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "kind", "source", "code", "agent_uid", "pane_uid"}
		for key := range keys {
			if !slices.Contains(allowed, key) {
				t.Fatalf("record %d carries %q outside the allowlist", i, key)
			}
		}
	}
	if owner.RecordedOutcome() {
		t.Fatal("claude registration record claimed the command outcome")
	}
}

// TestClaudeRegistrationRecorderFailureIsSilent pins best effort: a journal
// that refuses every append costs one attempt per record, and a nil recorder
// is a no-op.
func TestClaudeRegistrationRecorderFailureIsSilent(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{err: errors.New("fixture journal unavailable")}
	recorder := NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").ClaudeRegistration()
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHook, Reason: ClaudeRegistrationHelperUnconfirmed})
	recorder.Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHelper, Reason: ClaudeRegistrationReady})
	if got := writer.snapshot(); len(got) != 2 {
		t.Fatalf("appends = %d, want one attempt per record", len(got))
	}
	var owner *LifecycleRecorder
	if owner.ClaudeRegistration() != nil {
		t.Fatal("nil lifecycle recorder produced a claude registration recorder")
	}
	owner.ClaudeRegistration().Record(ClaudeRegistrationRecord{Source: ClaudeRegistrationSourceHook, Reason: ClaudeRegistrationHookArguments})
}

// TestClaudeRegistrationRoutesClassifyWithoutAnOutcome pins the attribution of
// the two internal routes: their argv classifies to the route word, never as a
// state change, so a nil result writes no command.outcome.
func TestClaudeRegistrationRoutesClassifyWithoutAnOutcome(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"claude-endpoint-register", "claude-endpoint-helper"} {
		class := Classify([]string{"internal", route})
		if class != (CommandClass{Command: route}) {
			t.Fatalf("Classify(internal %s) = %#v", route, class)
		}
		if recorded := classifyRecorded([]string{route}); recorded.Command != route {
			t.Fatalf("reader rejects command %q", route)
		}
		store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
		if err := RecordOutcome(store, []string{"internal", route}, "run", "1.0.0", "tmux", time.Now(), nil, false, false); err != nil {
			t.Fatal(err)
		}
		if events, err := store.Read(); err != nil || len(events) != 0 {
			t.Fatalf("internal %s success wrote %+v (err %v)", route, events, err)
		}
	}
}
