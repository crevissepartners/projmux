package metadata

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestV4ToV5PreservesTmuxGolden(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/registry-v4-destination-closure.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var source Registry
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	before := source.Clone()
	got, migrated, report, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
	if err != nil || !migrated || report.FromVersion != 4 || report.ToVersion != 5 || report.RepairCount() != 0 {
		t.Fatalf("migration %v %v %v", migrated, report, err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source, before) {
		t.Fatal("source mutated")
	}
	for i := range got.Panes {
		if got.Panes[i].Spec.Runtime.Kind != RuntimeTmux {
			t.Fatal("tmux kind missing")
		}
	}
	check := got.Clone()
	check.SchemaVersion = 4
	for i := range check.Panes {
		check.Panes[i].Spec.Runtime = PaneRuntimeSpec{}
	}
	if mustJSON(t, check) != mustJSON(t, source) {
		t.Fatal("v4 identity, ownership, recipe or activation changed")
	}
	assertV5Golden(t, "registry-v5-tmux.golden.json", mustJSON(t, got)+"\n")
}

func TestV4ToV5PreservesActivatedTmuxGolden(t *testing.T) {
	t.Parallel()
	_, source, _, _, _ := terminationFixture(t)
	source.SchemaVersion = 4
	for i := range source.Panes {
		source.Panes[i].Status.Activation.RuntimeID = []string{"%10", "%11"}[i]
	}
	before := mustJSON(t, source)
	got, migrated, _, err := MigrateRegistryWithEnvironment(nil, *source, MigrationEnvironment{})
	if err != nil || !migrated {
		t.Fatalf("migration: %v %v", migrated, err)
	}
	check := got.Clone()
	check.SchemaVersion = 4
	for i := range check.Panes {
		check.Panes[i].Spec.Runtime = PaneRuntimeSpec{}
	}
	if mustJSON(t, check) != before || mustJSON(t, source) != before {
		t.Fatal("tmux activation or source changed")
	}
	assertV5Golden(t, "registry-v5-tmux-activation.golden.json", mustJSON(t, got)+"\n")
}

func assertV5Golden(t *testing.T, file, got string) {
	t.Helper()
	path := "testdata/" + file
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch:\n%s", file, got)
	}
}

func processSchemaFixture(t *testing.T) Registry {
	t.Helper()
	_, reg, _, agentUID, paneUID := terminationFixture(t)
	pane, _ := reg.Pane(paneUID)
	agent, _ := reg.Agent(agentUID)
	window, _ := reg.Window(agent.Metadata.OwnerUID())
	b := ProcessBinding{HostInstanceID: "host-one", ProjectUID: window.Metadata.OwnerUID(), WindowUID: window.Metadata.UID, AgentUID: agentUID, PaneUID: paneUID, Generation: "gen-one", OperationID: "op-one"}
	pane.Spec.Runtime.Kind = RuntimeProcess
	pane.Status.Activation = PaneActivation{Kind: RuntimeProcess, Generation: b.Generation, AgentUID: agentUID, OperationID: b.OperationID, Process: &ProcessActivation{Binding: b, HostProcess: ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "host-start"}, Child: ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "child-start"}}}
	pane.Status.ProcessSession = &ProcessSessionRecord{Provider: "codex", Binding: b, ThreadID: "thread-one", ConnectionID: "connection-one", TurnID: "turn-one", ResumeState: ProcessResumable, Pending: []ProcessRecordedControl{{ID: "control-one", Kind: "question", ConnectionID: "connection-one", SessionID: "thread-one", TurnID: "turn-one"}}}
	old := b
	old.Generation = "gen-retired"
	old.OperationID = "op-retired"
	pane.Status.ProcessSession.History = &ProcessResumeHistory{Binding: old, SessionID: "thread-one", InterruptedTurnID: "turn-retired", Expired: []ProcessRecordedControl{{ID: "control-retired", Kind: "permission", ConnectionID: "connection-retired", SessionID: "thread-one", TurnID: "turn-retired"}}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return *reg
}

func TestV5ProcessRoundTripAndClone(t *testing.T) {
	t.Parallel()
	reg := processSchemaFixture(t)
	raw := mustJSON(t, reg)
	var got Registry
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, got) != raw {
		t.Fatal("process evidence lost in round trip")
	}
	clone := got.Clone()
	for i := range clone.Panes {
		if s := clone.Panes[i].Status.ProcessSession; s != nil {
			s.Pending[0].ID = "changed"
			clone.Panes[i].Status.Activation.Process.Binding.HostInstanceID = "changed"
		}
	}
	if mustJSON(t, got) != raw {
		t.Fatal("clone aliases original")
	}
	assertV5Golden(t, "registry-v5-process.golden.json", raw+"\n")
}

func TestV5ClaudeSessionAndUnknownResumeAreDistinct(t *testing.T) {
	t.Parallel()
	reg := processSchemaFixture(t)
	for i := range reg.Agents {
		reg.Agents[i].Spec.Provider = "claude"
	}
	for i := range reg.Panes {
		if s := reg.Panes[i].Status.ProcessSession; s != nil {
			s.Provider = "claude"
			s.SessionID = s.ThreadID
			s.ThreadID = ""
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	assertV5Golden(t, "registry-v5-claude.golden.json", mustJSON(t, reg)+"\n")
	for i := range reg.Panes {
		if s := reg.Panes[i].Status.ProcessSession; s != nil {
			s.ResumeState = ProcessResumeUnknown
			s.SessionID = ""
			s.ConnectionID = ""
			s.TurnID = ""
			s.Pending = nil
			s.History = nil
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	for i := range reg.Panes {
		if s := reg.Panes[i].Status.ProcessSession; s != nil {
			s.ResumeState = ProcessResumable
		}
	}
	if !errors.Is(reg.Validate(), ErrInvalidRegistry) {
		t.Fatal("unknown identity claimed resumable")
	}
}

func TestV5RejectsUnknownKindAndForeignProcessEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*Pane)
		token  string
	}{
		{"unknown-kind", func(p *Pane) { p.Spec.Runtime.Kind = "future" }, "runtime-kind-unsupported"},
		{"tmux-binding", func(p *Pane) { p.Status.Activation.RuntimeID = "%12" }, "runtime-binding-invalid"},
		{"wrong-tag", func(p *Pane) { p.Status.Activation.Kind = RuntimeTmux }, "runtime-binding-invalid"},
		{"foreign-pane", func(p *Pane) { p.Status.Activation.Process.Binding.PaneUID = "another" }, "runtime-binding-invalid"},
		{"foreign-generation", func(p *Pane) { p.Status.Activation.Process.Binding.Generation = "another" }, "runtime-binding-invalid"},
		{"missing-child-evidence", func(p *Pane) { p.Status.Activation.Process.Child.Start = "" }, "runtime-binding-invalid"},
		{"mixed-provider-identities", func(p *Pane) { p.Status.ProcessSession.SessionID = "claude-session" }, "process-session-invalid"},
		{"unknown-resume-state", func(p *Pane) { p.Status.ProcessSession.ResumeState = "automatic" }, "process-session-invalid"},
		{"foreign-control", func(p *Pane) { p.Status.ProcessSession.Pending[0].ConnectionID = "old" }, "process-session-invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := processSchemaFixture(t)
			for i := range reg.Panes {
				if reg.Panes[i].Spec.Runtime.Kind == RuntimeProcess {
					test.mutate(&reg.Panes[i])
				}
			}
			err := reg.Validate()
			if !errors.Is(err, ErrInvalidRegistry) || !strings.Contains(err.Error(), test.token) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestV5ReadBoundaryRejectsUnknownKinds(t *testing.T) {
	t.Parallel()
	for _, activationTag := range []bool{false, true} {
		reg := processSchemaFixture(t)
		for i := range reg.Panes {
			if reg.Panes[i].Spec.Runtime.Kind == RuntimeProcess {
				if activationTag {
					reg.Panes[i].Status.Activation.Kind = "future"
				} else {
					reg.Panes[i].Spec.Runtime.Kind = "future"
				}
			}
		}
		if _, _, _, err := MigrateRegistryWithEnvironment(nil, reg, MigrationEnvironment{}); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("read boundary accepted unknown kind (activation=%t): %v", activationTag, err)
		}
	}
}
