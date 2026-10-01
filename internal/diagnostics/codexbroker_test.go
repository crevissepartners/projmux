package diagnostics

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestCodexBrokerRefusalRecordsOneClosedTokenRecord(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	owner := NewLifecycleRecorder(store, "broker-refusal-run", "1.0.0", "tmux")
	owner.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	recorder := owner.CodexBroker()
	recorder.RecordRefusal(CodexBrokerRefusal{Role: CodexBrokerRoleProbe, Operation: CodexBrokerOperationEnsure, Reason: "host-unavailable", DialStage: "discovery"})
	recorder.RecordRefusal(CodexBrokerRefusal{Role: CodexBrokerRoleObserver, Operation: CodexBrokerOperationLifecycleRead, Reason: "drain-required"})
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	want := []Event{
		{At: "2026-10-01T12:00:00Z", Level: "info", Component: "codex-broker", Event: "codex.broker.refusal", Result: "success",
			RunID: "broker-refusal-run", Version: "1.0.0", MuxBackend: "tmux",
			Source: "probe", Operation: "ensure", Code: "host-unavailable", DialStage: "discovery"},
		{At: "2026-10-01T12:00:00Z", Level: "info", Component: "codex-broker", Event: "codex.broker.refusal", Result: "success",
			RunID: "broker-refusal-run", Version: "1.0.0", MuxBackend: "tmux",
			Source: "observer", Operation: "lifecycle-read", Code: "drain-required"},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", events, want)
	}
	if owner.RecordedOutcome() {
		t.Fatal("refusal record claimed the command outcome")
	}
	var nilRecorder *CodexBrokerRecorder
	nilRecorder.RecordRefusal(CodexBrokerRefusal{Role: CodexBrokerRoleProbe, Operation: CodexBrokerOperationEnsure, Reason: "host-unavailable"})
	if (*LifecycleRecorder)(nil).CodexBroker() != nil {
		t.Fatal("nil lifecycle recorder returned a codex broker recorder")
	}
}

// Every value the record carries comes from a closed set; anything else --
// a path, a thread id, a UID, free text -- drops the whole record.
func TestCodexBrokerRefusalDropsEveryValueOutsideItsClosedSet(t *testing.T) {
	t.Parallel()
	valid := CodexBrokerRefusal{Role: CodexBrokerRoleObserver, Operation: CodexBrokerOperationBind, Reason: "binding-exists"}
	for name, refusal := range map[string]CodexBrokerRefusal{
		"none reason":            {Role: valid.Role, Operation: valid.Operation, Reason: "none"},
		"empty reason":           {Role: valid.Role, Operation: valid.Operation},
		"error text reason":      {Role: valid.Role, Operation: valid.Operation, Reason: "codex broker refused: binding-exists"},
		"path reason":            {Role: valid.Role, Operation: valid.Operation, Reason: "/tmp/state/codex-broker.sock"},
		"thread id reason":       {Role: valid.Role, Operation: valid.Operation, Reason: "019a0b3c-thread"},
		"unknown role":           {Role: "cli", Operation: valid.Operation, Reason: valid.Reason},
		"empty role":             {Operation: valid.Operation, Reason: valid.Reason},
		"unknown operation":      {Role: valid.Role, Operation: "agent-uid-abc", Reason: valid.Reason},
		"probe control write":    {Role: CodexBrokerRoleProbe, Operation: CodexBrokerOperationTurnStart, Reason: valid.Reason},
		"unknown dial stage":     {Role: valid.Role, Operation: CodexBrokerOperationEnsure, Reason: "host-unavailable", DialStage: "connect"},
		"dial stage off ensure":  {Role: valid.Role, Operation: CodexBrokerOperationBind, Reason: valid.Reason, DialStage: "dial"},
		"runtime id dial stage ": {Role: valid.Role, Operation: CodexBrokerOperationEnsure, Reason: "host-unavailable", DialStage: "runtime-4f2a"},
	} {
		writer := &recordingEventWriter{}
		NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").CodexBroker().RecordRefusal(refusal)
		if got := writer.snapshot(); len(got) != 0 {
			t.Fatalf("%s recorded %+v", name, got)
		}
	}
	writer := &recordingEventWriter{}
	NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").CodexBroker().RecordRefusal(valid)
	if got := writer.snapshot(); len(got) != 1 {
		t.Fatalf("valid refusal records = %d, want 1", len(got))
	}
}

func TestCodexBrokerRefusalAppendFailureIsIgnored(t *testing.T) {
	t.Parallel()
	writer := &recordingEventWriter{err: errors.New("fixture journal unavailable")}
	NewLifecycleRecorder(writer, "run", "1.0.0", "tmux").CodexBroker().RecordRefusal(
		CodexBrokerRefusal{Role: CodexBrokerRoleProbe, Operation: CodexBrokerOperationEnsure, Reason: "host-unavailable"})
	if got := writer.snapshot(); len(got) != 1 {
		t.Fatalf("appends = %d, want one attempt", len(got))
	}
}

func TestCodexBrokerRefusalFieldsAreRejectedOnEveryOtherFamilyAndExtrasOnItsOwn(t *testing.T) {
	t.Parallel()
	refusal := Event{At: "2026-10-01T12:00:00Z", Level: "info", Component: "codex-broker", Event: "codex.broker.refusal", Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux", Source: "probe", Operation: "ensure", Code: "host-unavailable", DialStage: "dial"}
	if _, err := sanitizeEvent(refusal, ""); err != nil {
		t.Fatalf("refusal rejected: %v", err)
	}
	base := Event{At: refusal.At, Level: "info", Component: "cli", Event: "command.outcome", Result: "success",
		RunID: "run", Version: "1.0.0", MuxBackend: "tmux"}
	withStage := base
	withStage.DialStage = "dial"
	withComponent := base
	withComponent.Component = "codex-broker"
	withMessage := refusal
	withMessage.Message = "codex broker refused: host-unavailable"
	withAgent := refusal
	withAgent.AgentUID = "agent-abc234"
	withKind := refusal
	withKind.Kind = "runtime"
	asError := refusal
	asError.Level, asError.Result = "error", "error"
	otherComponent := refusal
	otherComponent.Component = "agent"
	for name, event := range map[string]Event{"dial stage": withStage, "component": withComponent, "message": withMessage,
		"agent uid": withAgent, "kind": withKind, "error level": asError, "other component": otherComponent} {
		if _, err := sanitizeEvent(event, ""); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The record never feeds Doctor's recent-error window or the support report's
// error tail: a refusal received is not a command failure.
func TestCodexBrokerRefusalStaysOutOfRuntimeHealthErrors(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	NewLifecycleRecorder(store, "run", "1.0.0", "tmux").CodexBroker().RecordRefusal(
		CodexBrokerRefusal{Role: CodexBrokerRoleObserver, Operation: CodexBrokerOperationLifecycleRead, Reason: "lifecycle-retry"})
	health, err := ReadRuntimeHealth(store)
	if err != nil || health.RecentErrorCount != 0 || len(health.RecentFailureCodes) != 0 {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

// The closed reason set is the broker's own: every Refusal the broker package
// declares except `none`, and nothing else.
func TestCodexBrokerRefusalReasonsMatchTheBrokerRefusalConstants(t *testing.T) {
	t.Parallel()
	declared := declaredStringConstants(t, filepath.Join("..", "integrations", "agents", "codexbroker", "fence.go"), "Refusal")
	stages := declaredStringConstants(t, filepath.Join("..", "integrations", "agents", "codexbroker", "client.go"), "DialStage")
	declared = slices.DeleteFunc(declared, func(value string) bool { return value == "none" })
	slices.Sort(declared)
	if len(declared) == 0 {
		t.Fatal("no broker Refusal constants found")
	}
	var recorded []string
	for value := range codexBrokerRefusals {
		recorded = append(recorded, value)
	}
	slices.Sort(recorded)
	if !slices.Equal(recorded, declared) {
		t.Fatalf("recorded reasons =\n%q\nbroker refusals =\n%q", recorded, declared)
	}
	var recordedStages []string
	for value := range codexBrokerDialStages {
		recordedStages = append(recordedStages, value)
	}
	slices.Sort(recordedStages)
	slices.Sort(stages)
	if !slices.Equal(recordedStages, stages) {
		t.Fatalf("recorded dial stages = %q, broker dial stages = %q", recordedStages, stages)
	}
}

// declaredStringConstants returns every string constant of the named type
// declared in one Go source file.
func declaredStringConstants(t *testing.T, path, typeName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var values []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != typeName {
				continue
			}
			for _, expr := range value.Values {
				literal, ok := expr.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s constant is not a string literal", typeName)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", literal.Value, err)
				}
				values = append(values, unquoted)
			}
		}
	}
	return values
}
