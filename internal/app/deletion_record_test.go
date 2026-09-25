package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// deletionTestSocket is the exact server testDeleteEnvironment's $TMUX names.
const (
	deletionTestSocket    = "/tmp/projmux-test/isolated"
	deletionTestServerPID = "1234"
	deletionTestActorPane = "%42"
)

// deletionActorRunner answers only the actor confirmation query, the way the
// ambient Pane on the delete's own server would.
type deletionActorRunner struct {
	calls [][]string
	row   []string
	err   error
}

func newDeletionActorRunner(paneUID string) *deletionActorRunner {
	return &deletionActorRunner{row: []string{deletionTestSocket, deletionTestServerPID, deletionTestActorPane, "7001", paneUID}}
}

func (r *deletionActorRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.err != nil {
		return nil, r.err
	}
	return []byte(strings.Join(r.row, tmuxRowSep) + "\n"), nil
}

// deletionActorEnv is testDeleteEnvironment plus an ambient Pane.
func deletionActorEnv(paneID string) func(string) string {
	env := map[string]string{"TMUX": deletionTestSocket + "," + deletionTestServerPID + ",0"}
	if paneID != "" {
		env["TMUX_PANE"] = paneID
	}
	return func(key string) string { return env[key] }
}

// activateDeletionActorPane gives pan-alpha-codex a complete activation at
// the ambient runtime id.
func activateDeletionActorPane(pane *coremetadata.Pane) {
	pane.Status.Activation = coremetadata.PaneActivation{
		Generation: "gen-codex", AgentUID: "agt-alpha-codex", OperationID: "op-codex",
		StartedAt: resourceFixtureClock, RuntimeID: deletionTestActorPane,
	}
}

// seedDeletionActorRegistry makes pan-alpha-codex the live managed Pane of
// agt-alpha-codex at deletionTestActorPane, and adds a second live Agent
// (agt-alpha-claude with pan-alpha-claude) in the same Window.
func seedDeletionActorRegistry(t *testing.T, store *fakeResourceStore) {
	t.Helper()
	registry := &store.registry
	for i := range registry.Panes {
		if registry.Panes[i].Metadata.UID == "pan-alpha-codex" {
			activateDeletionActorPane(&registry.Panes[i])
		}
	}
	registry.Agents = append(registry.Agents, coremetadata.Agent{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{UID: "agt-alpha-claude", Name: "claude", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"}},
		Spec: coremetadata.AgentSpec{Provider: "claude"},
		Status: coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: "pan-alpha-claude",
			LastTransitionAt: resourceFixtureClock},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pan-alpha-claude", Name: "claude-pane", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agt-alpha-claude"}},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/alpha"},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindAgent, Name: "claude", UID: "agt-alpha-claude"},
		coremetadata.NameReservation{Scope: "prj-alpha", Kind: coremetadata.KindPane, Name: "claude-pane", UID: "pan-alpha-claude"},
	)
	*registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("deletion actor fixture is invalid: %v", err)
	}
}

// bindDeletionRecords points store's state root at a fresh temp dir and
// returns the records path.
func bindDeletionRecords(t *testing.T, store *resourceStore) string {
	t.Helper()
	dir := t.TempDir()
	store.stateDir = func() (string, error) { return dir, nil }
	return filepath.Join(dir, deletionRecordsFile)
}

func readDeletionRecords(t *testing.T, path string) []DeletionRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read deletion records: %v", err)
	}
	var out []DeletionRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}
		var record DeletionRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("deletion record %q is not JSON: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func onlyDeletionRecord(t *testing.T, path string) DeletionRecord {
	t.Helper()
	records := readDeletionRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("deletion records = %d lines, want exactly 1: %+v", len(records), records)
	}
	record := records[0]
	if record.SchemaVersion != 1 || record.At.IsZero() || record.At.Location() != time.UTC || record.OperationID == "" {
		t.Fatalf("deletion record header = %+v", record)
	}
	return record
}

func affectedUIDs(record DeletionRecord) []string {
	out := make([]string, 0, len(record.Affected))
	for _, affected := range record.Affected {
		if affected.Action != "deleted" {
			panic("unexpected affected action " + affected.Action)
		}
		out = append(out, affected.Kind+"/"+affected.UID)
	}
	return out
}

// newDeletionActorDeleteCommand is a delete route run from inside
// pan-alpha-codex, with every actor check wired to pass.
func newDeletionActorDeleteCommand(t *testing.T) (*deleteCommand, *fakeResourceStore, *deletionActorRunner, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	seedDeletionActorRegistry(t, store)
	cmd := newTestDeleteCommand(store, false, false, nil)
	path := bindDeletionRecords(t, cmd.store)
	runner := newDeletionActorRunner("pan-alpha-codex")
	cmd.lookupEnv = deletionActorEnv(deletionTestActorPane)
	cmd.actorRunner = runner
	cmd.processAncestors = func() ([]int, error) { return []int{90001, 7001, 4242}, nil }
	return cmd, store, runner, path
}

// Acceptance 1: `delete agent uid:X` inside a managed Agent Pane records that
// Agent and Pane as the actor on the pane-chain basis.
func TestDeletionRecordAgentDeleteFromAgentPaneRecordsPaneChainActor(t *testing.T) {
	cmd, store, runner, path := newDeletionActorDeleteCommand(t)
	stdout, stderr, err := runRoute(t, cmd, "agent", "uid:agt-alpha-claude", "--yes")
	if err != nil || stderr != "" {
		t.Fatalf("delete agent: err=%v stderr=%q stdout=%q", err, stderr, stdout)
	}
	if _, ok := store.registry.Agent("agt-alpha-claude"); ok {
		t.Fatal("delete agent kept the Agent")
	}
	record := onlyDeletionRecord(t, path)
	if record.Operation != "delete-agent" || record.Via != "cli" || record.OperationID != "op-delete" {
		t.Fatalf("record = %+v", record)
	}
	want := DeletionActor{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Basis: "pane-chain"}
	if record.Actor != want {
		t.Fatalf("actor = %+v, want %+v", record.Actor, want)
	}
	if !reflect.DeepEqual(record.Targets, []DeletionTarget{{Kind: "Agent", UID: "agt-alpha-claude", Name: "claude"}}) {
		t.Fatalf("targets = %+v", record.Targets)
	}
	if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Agent/agt-alpha-claude", "Pane/pan-alpha-claude"}) {
		t.Fatalf("affected = %v", got)
	}
	if len(runner.calls) != 1 || !slices.Contains(runner.calls[0], "-S") || !slices.Contains(runner.calls[0], deletionTestSocket) ||
		!slices.Contains(runner.calls[0], deletionTestActorPane) {
		t.Fatalf("actor confirmation must be one display-message on the delete's own -S route: %v", runner.calls)
	}
	// Every key is present in the wire format, including an empty-free actor.
	raw, _ := os.ReadFile(path)
	for _, key := range []string{`"schemaVersion":1`, `"at":"`, `"operation":"delete-agent"`, `"operationID":"op-delete"`, `"via":"cli"`,
		`"actor":{"agentUID":"agt-alpha-codex","paneUID":"pan-alpha-codex","basis":"pane-chain"}`, `"targets":[`, `"affected":[`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("record %s lacks %s", raw, key)
		}
	}
}

// Acceptance 2: a Window delete from a shell outside any managed Pane records
// no actor, with creation's own skip token, and costs no tmux call.
func TestDeletionRecordWindowDeleteOutsideManagedPaneRecordsSkipBasis(t *testing.T) {
	for _, test := range []struct {
		name      string
		paneID    string
		wantBasis string
	}{
		{name: "unregistered ambient pane", paneID: "%99", wantBasis: creatorSkipPaneUnregistered},
		{name: "window-owned shell pane", paneID: "%31", wantBasis: creatorSkipCallerNotAgent},
		{name: "no ambient pane", paneID: "", wantBasis: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, store, runner, path := newDeletionActorDeleteCommand(t)
			for i := range store.registry.Panes {
				if store.registry.Panes[i].Metadata.UID == "pan-alpha-zsh" {
					store.registry.Panes[i].Status.Activation = coremetadata.PaneActivation{
						Generation: "gen-zsh", OperationID: "op-zsh", StartedAt: resourceFixtureClock, RuntimeID: "%31",
					}
				}
			}
			cmd.lookupEnv = deletionActorEnv(test.paneID)
			if _, stderr, err := runRoute(t, cmd, "window", "uid:win-alpha-review", "--yes"); err != nil || stderr != "" {
				t.Fatalf("delete window: err=%v stderr=%q", err, stderr)
			}
			record := onlyDeletionRecord(t, path)
			if record.Operation != "delete-window" || record.Via != "cli" {
				t.Fatalf("record = %+v", record)
			}
			if record.Actor != (DeletionActor{Basis: test.wantBasis}) {
				t.Fatalf("actor = %+v, want basis %q only", record.Actor, test.wantBasis)
			}
			if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Window/win-alpha-review", "Pane/pan-alpha-review"}) {
				t.Fatalf("affected = %v", got)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("a non-Agent ambient pane must cost no tmux call: %v", runner.calls)
			}
		})
	}
}

// Acceptance 3: the generated UI Pane delete records via=ui with an empty
// actor even when the ambient Pane is an Agent Pane.
func TestDeletionRecordUIRouteRecordsEmptyActorEvenInsideAgentPane(t *testing.T) {
	cmd, _, runner, path := newDeletionActorDeleteCommand(t)
	lookup := func() (activeTargetObserver, bool) {
		return activeTargetObserver{paneID: "%31", paneUID: func() string { return "pan-alpha-log" }}, true
	}
	var stdout, stderr bytes.Buffer
	if err := deleteExactPaneThroughCommand(cmd, lookup, "%31", &stdout, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("ui pane delete: err=%v stderr=%q", err, stderr.String())
	}
	record := onlyDeletionRecord(t, path)
	if record.Operation != "delete-pane" || record.Via != "ui" || record.Actor != (DeletionActor{}) {
		t.Fatalf("record = %+v", record)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("the UI route must not judge an actor: %v", runner.calls)
	}
	if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Pane/pan-alpha-log"}) {
		t.Fatalf("affected = %v", got)
	}
}

// The generated UI Window delete (`prefix &`) records via=ui with an empty
// actor and judges nothing, even when the ambient Pane is an Agent Pane whose
// CLI delete of the same Window would name that Agent.
func TestDeletionRecordUIWindowRouteRecordsEmptyActorEvenInsideAgentPane(t *testing.T) {
	// Control: the same fixture through the CLI judges the Agent Pane, so the
	// UI route's empty actor below is its own rule, not an unjudgeable fixture.
	control, _, controlRunner, controlPath := newDeletionActorDeleteCommand(t)
	if _, stderr, err := runRoute(t, control, "window", "uid:win-alpha-review", "--yes"); err != nil || stderr != "" {
		t.Fatalf("cli window delete: err=%v stderr=%q", err, stderr)
	}
	if record := onlyDeletionRecord(t, controlPath); record.Via != "cli" || record.Actor.Basis != "pane-chain" || len(controlRunner.calls) != 1 {
		t.Fatalf("control record = %+v calls = %v", record, controlRunner.calls)
	}

	cmd, store, runner, path := newDeletionActorDeleteCommand(t)
	lookup := func() (activeTargetObserver, bool) {
		return activeTargetObserver{paneID: "%31", windowUID: func() string { return "win-alpha-review" }}, true
	}
	var stdout, stderr bytes.Buffer
	if err := deleteExactWindowThroughCommand(cmd, lookup, &stdout, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("ui window delete: err=%v stderr=%q", err, stderr.String())
	}
	if _, ok := store.registry.Window("win-alpha-review"); ok {
		t.Fatal("ui window delete kept the Window")
	}
	record := onlyDeletionRecord(t, path)
	if record.Operation != "delete-window" || record.Via != "ui" || record.Actor != (DeletionActor{}) {
		t.Fatalf("record = %+v", record)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("the UI route must not judge an actor: %v", runner.calls)
	}
	if len(record.Targets) != 1 || record.Targets[0].Kind != "Window" || record.Targets[0].UID != "win-alpha-review" {
		t.Fatalf("targets = %+v", record.Targets)
	}
	if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Window/win-alpha-review", "Pane/pan-alpha-review"}) {
		t.Fatalf("affected = %v", got)
	}
}

// Acceptance 4 (prune agent): one via=prune line listing every pruned Agent.
func TestDeletionRecordPruneAgentRecordsEveryPrunedTarget(t *testing.T) {
	store := newPruneAgentFakeStore(t)
	for i := range store.registry.Panes {
		if store.registry.Panes[i].Metadata.UID == "pan-alpha-codex" {
			activateDeletionActorPane(&store.registry.Panes[i])
		}
	}
	cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	path := bindDeletionRecords(t, cmd.store)
	runner := newDeletionActorRunner("pan-alpha-codex")
	cmd.actor = pruneDeletionActor{
		lookupEnv:        deletionActorEnv(deletionTestActorPane),
		processAncestors: func() ([]int, error) { return []int{90001, 7001}, nil },
		runner:           runner,
		newOperationID:   func() (string, error) { return "op-prune", nil },
	}
	stdout, stderr, err := runRoute(t, cmd, "--older-than", "720h", "--no-pane", "--yes")
	if err != nil || stderr != "" {
		t.Fatalf("prune agent: err=%v stderr=%q", err, stderr)
	}
	pruned := listedPruneAgentUIDs(stdout)
	if len(pruned) < 2 {
		t.Fatalf("fixture should prune several Agents: %q", stdout)
	}
	record := onlyDeletionRecord(t, path)
	if record.Operation != "prune-agent" || record.Via != "prune" || record.OperationID != "op-prune" {
		t.Fatalf("record = %+v", record)
	}
	var targets []string
	for _, target := range record.Targets {
		if target.Kind != "Agent" {
			t.Fatalf("target kind = %q", target.Kind)
		}
		targets = append(targets, target.UID)
	}
	slices.Sort(targets)
	if !reflect.DeepEqual(targets, pruned) {
		t.Fatalf("targets = %v, pruned = %v", targets, pruned)
	}
	for _, uid := range pruned {
		if !slices.Contains(affectedUIDs(record), "Agent/"+uid) {
			t.Fatalf("affected %v lacks pruned %s", affectedUIDs(record), uid)
		}
	}
	want := DeletionActor{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Basis: "pane-chain"}
	if record.Actor != want {
		t.Fatalf("prune actor = %+v, want %+v", record.Actor, want)
	}
}

// Acceptance 4 (prune agent, server step): prune has no socket flags, so with
// no inherited $TMUX an Agent-Pane ambient is honestly unproven.
func TestDeletionRecordPruneWithoutInheritedServerIsServerUnproven(t *testing.T) {
	store := newPruneAgentFakeStore(t)
	for i := range store.registry.Panes {
		if store.registry.Panes[i].Metadata.UID == "pan-alpha-codex" {
			activateDeletionActorPane(&store.registry.Panes[i])
		}
	}
	cmd, _ := newTestPruneAgentCommand(store.store(), fixedPruneAgentClock)
	path := bindDeletionRecords(t, cmd.store)
	runner := newDeletionActorRunner("pan-alpha-codex")
	cmd.actor = pruneDeletionActor{
		lookupEnv:        func(key string) string { return map[string]string{"TMUX_PANE": deletionTestActorPane}[key] },
		processAncestors: func() ([]int, error) { return []int{90001, 7001}, nil },
		runner:           runner,
	}
	if _, stderr, err := runRoute(t, cmd, "--older-than", "720h", "--no-pane", "--yes"); err != nil || stderr != "" {
		t.Fatalf("prune agent: err=%v stderr=%q", err, stderr)
	}
	record := onlyDeletionRecord(t, path)
	if record.Actor != (DeletionActor{Basis: creatorSkipServerUnproven}) || len(runner.calls) != 0 {
		t.Fatalf("actor = %+v calls = %v", record.Actor, runner.calls)
	}
}

// Acceptance 4 (prune project): one via=prune line naming the pruned Project
// and its whole removed subtree.
func TestDeletionRecordPruneProjectRecordsPrunedProjectAndCascade(t *testing.T) {
	store := newFakeResourceStore(t)
	cmd := newTestPruneProjectCommand(store)
	path := bindDeletionRecords(t, cmd.store)
	cmd.actor = pruneDeletionActor{newOperationID: func() (string, error) { return "op-prune", nil }}
	if _, stderr, err := runRoute(t, cmd, "--missing", "--older-than", "720h", "--yes"); err != nil || stderr != "" {
		t.Fatalf("prune project: err=%v stderr=%q", err, stderr)
	}
	record := onlyDeletionRecord(t, path)
	if record.Operation != "prune-project" || record.Via != "prune" || record.Actor != (DeletionActor{}) {
		t.Fatalf("record = %+v", record)
	}
	if !reflect.DeepEqual(record.Targets, []DeletionTarget{{Kind: "Project", UID: "prj-gone", Name: "gone"}}) {
		t.Fatalf("targets = %+v", record.Targets)
	}
	if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Project/prj-gone", "Window/win-gone-main", "Pane/pan-gone-zsh"}) {
		t.Fatalf("affected = %v", got)
	}
}

// The canonical `unregister project` and the deprecated `delete project` each
// write one line with their own spelling and a minted operation id.
func TestDeletionRecordProjectUnregisterSpellings(t *testing.T) {
	for _, test := range []struct {
		verb string
		want string
	}{
		{verb: "unregister", want: "unregister-project"},
		{verb: "delete", want: "delete-project"},
	} {
		t.Run(test.verb, func(t *testing.T) {
			cmd, _, _, path := newDeletionActorDeleteCommand(t)
			var err error
			if test.verb == "delete" {
				_, _, err = runRoute(t, cmd, "project", "uid:prj-gone", "--yes")
			} else {
				_, _, err = runRoute(t, &unregisterCommand{delete: cmd}, "project", "uid:prj-gone", "--yes")
			}
			if err != nil {
				t.Fatal(err)
			}
			record := onlyDeletionRecord(t, path)
			if record.Operation != test.want || record.Via != "cli" || record.OperationID != "op-delete" {
				t.Fatalf("record = %+v", record)
			}
			if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Project/prj-gone", "Window/win-gone-main", "Pane/pan-gone-zsh"}) {
				t.Fatalf("affected = %v", got)
			}
		})
	}
}

// Acceptance 5: dry-run, refusal, declined confirmation, and a failed commit
// write no line.
func TestDeletionRecordNoLineWithoutACommittedDeletion(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*deleteCommand)
		args  []string
	}{
		{name: "dry-run", args: []string{"agent", "uid:agt-alpha-claude", "--dry-run"}},
		{name: "project dry-run", args: []string{"project", "uid:prj-gone", "--dry-run"}},
		{name: "refused without --yes", args: []string{"window", "uid:win-alpha-review"}},
		{name: "declined confirmation", args: []string{"agent", "uid:agt-alpha-claude"}, setup: func(cmd *deleteCommand) {
			cmd.confirm.interactive = func() bool { return true }
		}},
		{name: "failed commit", args: []string{"agent", "uid:agt-alpha-claude", "--yes"}, setup: func(cmd *deleteCommand) {
			update, calls := cmd.store.update, 0
			cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
				calls++
				if calls == 2 {
					return coremetadata.Registry{}, errors.New("disk full")
				}
				return update(fn)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _, path := newDeletionActorDeleteCommand(t)
			if test.setup != nil {
				test.setup(cmd)
			}
			_, _, _ = runRoute(t, cmd, test.args...)
			if records := readDeletionRecords(t, path); len(records) != 0 {
				t.Fatalf("%s wrote %d deletion records", test.name, len(records))
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s created the records file: %v", test.name, err)
			}
		})
	}

	t.Run("prune listing and empty prune", func(t *testing.T) {
		store := newFakeResourceStore(t)
		cmd := newTestPruneProjectCommand(store)
		path := bindDeletionRecords(t, cmd.store)
		if _, _, err := runRoute(t, cmd, "--missing", "--older-than", "720h"); err != nil {
			t.Fatal(err)
		}
		store.dirs["/srv/gone"] = true
		if _, _, err := runRoute(t, cmd, "--missing", "--older-than", "720h", "--yes"); err != nil {
			t.Fatal(err)
		}
		if records := readDeletionRecords(t, path); len(records) != 0 {
			t.Fatalf("prune without a committed deletion wrote %d records", len(records))
		}
	})
}

// Acceptance 5 (supervisor path): the termination journal's append is the
// supervisor's process-exit write and never produces a deletion record.
func TestDeletionRecordTerminationJournalWritesNoDeletionLine(t *testing.T) {
	dir := t.TempDir()
	journal := terminationJournal{path: filepath.Join(dir, terminationJournalFile)}
	if err := journal.append(coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal,
		PaneUID: "pan-alpha-codex", Generation: "gen-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, deletionRecordsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("termination journal append created a deletion record: %v", err)
	}
}

// Acceptance 6: an unwritable record never fails or changes the delete; it
// costs exactly one stderr line.
func TestDeletionRecordWriteFailureKeepsDeleteAndPrintsOneLine(t *testing.T) {
	control, _, _, _ := newDeletionActorDeleteCommand(t)
	wantStdout, _, err := runRoute(t, control, "window", "uid:win-alpha-review", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		stateDir  func(t *testing.T) (string, error)
		wantToken string
	}{
		{name: "state dir is a file", wantToken: "state-dir-unavailable", stateDir: func(t *testing.T) (string, error) {
			file := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(file, "projmux"), nil
		}},
		{name: "state dir unresolvable", wantToken: "state-dir-unavailable", stateDir: func(*testing.T) (string, error) {
			return "", errors.New("no home")
		}},
		{name: "records path is a directory", wantToken: "append-failed", stateDir: func(t *testing.T) (string, error) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, deletionRecordsFile), 0o700); err != nil {
				t.Fatal(err)
			}
			return dir, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, store, _, _ := newDeletionActorDeleteCommand(t)
			dir, dirErr := test.stateDir(t)
			cmd.store.stateDir = func() (string, error) { return dir, dirErr }
			stdout, stderr, err := runRoute(t, cmd, "window", "uid:win-alpha-review", "--yes")
			if err != nil {
				t.Fatalf("a record failure failed the delete: %v", err)
			}
			if _, ok := store.registry.Window("win-alpha-review"); ok {
				t.Fatal("the delete did not commit")
			}
			if stdout != wantStdout {
				t.Fatalf("stdout changed:\n%s\nwant:\n%s", stdout, wantStdout)
			}
			if stderr != "deletion not recorded: "+test.wantToken+"\n" {
				t.Fatalf("stderr = %q", stderr)
			}
		})
	}
}

// Acceptance 7: deleting the caller's own Agent still records it as the
// actor, judged before the commit removed its Pane, and the line is durable
// before the self-kill is queued.
func TestDeletionRecordSelfAgentDeleteJudgesActorPreCommit(t *testing.T) {
	cmd, store, _, path := newDeletionActorDeleteCommand(t)
	panes := newFixturePaneDeleteRuntime()
	panes.selfUID = "pan-alpha-codex"
	linesAtQueue := -1
	panes.queueHook = func([]paneLiveDeleteTarget) { linesAtQueue = len(readDeletionRecords(t, path)) }
	cmd.panes = panes
	if _, stderr, err := runRoute(t, cmd, "agent", "uid:agt-alpha-codex", "--yes"); err != nil || stderr != "" {
		t.Fatalf("self delete agent: err=%v stderr=%q", err, stderr)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("self delete kept the actor Pane")
	}
	if linesAtQueue != 1 {
		t.Fatalf("records at self-kill queue time = %d, want 1", linesAtQueue)
	}
	record := onlyDeletionRecord(t, path)
	want := DeletionActor{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Basis: "pane-chain"}
	if record.Actor != want {
		t.Fatalf("actor = %+v, want %+v", record.Actor, want)
	}
	if got := affectedUIDs(record); !reflect.DeepEqual(got, []string{"Agent/agt-alpha-codex", "Pane/pan-alpha-codex"}) {
		t.Fatalf("affected = %v", got)
	}
}

// The delete route's server confirmation uses creation's skip tokens.
func TestDeletionAnchorConfirmSkipTokens(t *testing.T) {
	route := testDeleteTarget
	for _, test := range []struct {
		name      string
		route     tmuxTransport
		env       func(string) string
		rewrite   func(r *deletionActorRunner)
		wantSkip  string
		wantCalls int
	}{
		{name: "confirmed", route: route, env: deletionActorEnv(deletionTestActorPane), wantCalls: 1},
		{name: "no inherited server", route: route, env: func(string) string { return "" }, wantSkip: creatorSkipServerUnproven},
		{name: "no route", env: deletionActorEnv(deletionTestActorPane), wantSkip: creatorSkipServerUnproven},
		{name: "explicit socket elsewhere", route: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/other", Source: tmuxSocketPathSource},
			env: deletionActorEnv(deletionTestActorPane), wantSkip: creatorSkipServerMismatch},
		{name: "query failed", route: route, env: deletionActorEnv(deletionTestActorPane), wantSkip: creatorSkipAnchorQueryFailed, wantCalls: 1,
			rewrite: func(r *deletionActorRunner) { r.err = errors.New("no server") }},
		{name: "other server pid", route: route, env: deletionActorEnv(deletionTestActorPane), wantSkip: creatorSkipServerMismatch, wantCalls: 1,
			rewrite: func(r *deletionActorRunner) { r.row[1] = "999" }},
		{name: "other pane uid", route: route, env: deletionActorEnv(deletionTestActorPane), wantSkip: creatorSkipAnchorPaneMismatch, wantCalls: 1,
			rewrite: func(r *deletionActorRunner) { r.row[4] = "pan-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := newDeletionActorRunner("pan-alpha-codex")
			if test.rewrite != nil {
				test.rewrite(runner)
			}
			pid, skip := deletionAnchorConfirm(runner, test.route, test.env)(context.Background(), deletionTestActorPane, "pan-alpha-codex")
			if skip != test.wantSkip || len(runner.calls) != test.wantCalls {
				t.Fatalf("skip=%q calls=%d, want %q/%d", skip, len(runner.calls), test.wantSkip, test.wantCalls)
			}
			if skip == "" && pid != 7001 {
				t.Fatalf("pid = %d", pid)
			}
		})
	}
}
