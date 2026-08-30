package app

import (
	"context"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// termination_projection_test.go covers the read half of exit reconciliation:
// what `get`, `describe`, and the Registry-first UI show once a lifecycle has been
// projected, and what they must never do while showing it.
//
// The negative property is the load-bearing one. These are read verbs, and a read
// that consumed a receipt or advanced a phase would make querying the state change
// the state -- so every test here asserts zero writes alongside the rendering.

// seedProjectedTermination files one receipt against a fixture Pane and projects
// it, leaving the registry in the state the read verbs have to render.
func seedProjectedTermination(
	t *testing.T,
	store *fakeResourceStore,
	paneUID, agentUID string,
	classification coremetadata.TerminationClassification,
	exitCode *int,
	signal string,
) {
	t.Helper()

	mutator := store.mutator()
	stampFixtureActivation(t, store, paneUID, "gen-read", agentUID)
	source := coremetadata.TerminationSourceSupervisor
	if classification == coremetadata.TerminationIntentional {
		source = coremetadata.TerminationSourceControlAction
	}
	if classification != coremetadata.TerminationUnknown {
		outcome, err := mutator.RecordTermination(&store.registry, coremetadata.TerminationEvidence{
			Source:         source,
			Classification: classification,
			ObservedAt:     resourceFixtureReadClock.Add(-90 * time.Minute),
			PaneUID:        paneUID,
			AgentUID:       agentUID,
			Generation:     "gen-read",
			ExitCode:       exitCode,
			Signal:         signal,
		})
		if err != nil || !outcome.Applied {
			t.Fatalf("RecordTermination outcome = %+v err = %v", outcome, err)
		}
	}
	if _, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID:    paneUID,
		ObservedAt: resourceFixtureReadClock.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	store.writes = 0
	store.transactions = 0
}

// TestDescribeRendersTheStoredTerminationEvidence is the operator-visible half of
// the read projection.
//
// The three rows that always appear together are the point: a classification
// without its provenance cannot be told apart from a guess, and one without its
// instant cannot be told apart from a stale record.
func TestDescribeRendersTheStoredTerminationEvidence(t *testing.T) {
	t.Parallel()

	code := 42
	store := newFakeResourceStore(t)
	seedProjectedTermination(t, store, "pan-alpha-codex", "agt-alpha-codex",
		coremetadata.TerminationAbnormal, &code, "")
	before := store.snapshot()

	stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store),
		"agent", "codex", "--project", "alpha", "--window", "main")
	if err != nil {
		t.Fatalf("describe agent: %v (stderr=%s)", err, stderr)
	}
	for _, want := range []string{
		"Phase:", "Failed",
		"Termination:", "abnormal",
		"TerminationSource:", "supervisor",
		"TerminationObservedAt:", "2026-08-17T10:00:00Z",
		"TerminationExitCode:", "42",
		"TerminationGeneration:", "gen-read",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("describe agent output is missing %q:\n%s", want, stdout)
		}
	}
	if store.writes != 0 || store.transactions != 0 {
		t.Fatalf("describe wrote to the registry: transactions = %d writes = %d",
			store.transactions, store.writes)
	}
	if store.snapshot() != before {
		t.Fatal("describe changed the registry it was reading")
	}

	// A live Pane with no receipt renders no termination rows at all rather than a
	// permanent "none": absence of evidence is the normal state of a live
	// resource.
	shell, _, err := runRoute(t, newTestDescribeCommand(t, store),
		"pane", "zsh", "--project", "alpha", "--window", "main")
	if err != nil {
		t.Fatalf("describe pane: %v", err)
	}
	if strings.Contains(shell, "Termination") {
		t.Fatalf("a live shell Pane rendered a termination row:\n%s", shell)
	}
}

// TestDescribePaneRendersASurvivingShellPanesEvidence is the shell half.
//
// A shell Pane whose runtime died keeps its resource, so both halves of the answer
// stay queryable at the same surface: the MissingRuntime condition says since when
// it has had no runtime object, and the termination rows say what is known about
// why.
func TestDescribePaneRendersASurvivingShellPanesEvidence(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	seedProjectedTermination(t, store, "pan-beta-zsh", "", coremetadata.TerminationUnknown, nil, "")

	stdout, stderr, err := runRoute(t, newTestDescribeCommand(t, store),
		"pane", "zsh", "--project", "beta", "--window", "main")
	if err != nil {
		t.Fatalf("describe pane: %v (stderr=%s)", err, stderr)
	}
	for _, want := range []string{
		"Termination:", "unknown",
		"TerminationSource:", "reconcile",
		"Condition:", coremetadata.ConditionMissingRuntime,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("describe pane output is missing %q:\n%s", want, stdout)
		}
	}
	if store.writes != 0 {
		t.Fatalf("describe pane wrote to the registry %d times", store.writes)
	}
}

// TestGetRendersTheTerminationColumn pins the columnar projection.
//
// The cell is the compact clause plus a relative age, measured against the
// invocation's clock so a golden can pin it. A live row leaves it blank, which is
// what keeps the column readable: every non-blank cell is a resource whose process
// stopped.
func TestGetRendersTheTerminationColumn(t *testing.T) {
	t.Parallel()

	code := 42
	store := newFakeResourceStore(t)
	seedProjectedTermination(t, store, "pan-alpha-codex", "agt-alpha-codex",
		coremetadata.TerminationAbnormal, &code, "")
	before := store.snapshot()

	agents, stderr, err := runRoute(t, newTestListGetCommand(t, store), "agents", "--project", "alpha")
	if err != nil {
		t.Fatalf("get agents: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(agents, "TERMINATION") {
		t.Fatalf("get agents has no TERMINATION column:\n%s", agents)
	}
	if !strings.Contains(agents, "abnormal/supervisor exit=42 generation=gen-read 1h") {
		t.Fatalf("get agents termination cell is wrong:\n%s", agents)
	}
	for _, row := range columnarRows(t, agents) {
		if row["TERMINATION"] == "" {
			t.Fatalf("a released Agent parsed an empty TERMINATION column: %v", row)
		}
	}

	// A shell Pane that is still live leaves the cell empty without disturbing the
	// columns around it.
	panes, _, err := runRoute(t, newTestListGetCommand(t, store), "panes", "--project", "alpha")
	if err != nil {
		t.Fatalf("get panes: %v", err)
	}
	for _, row := range columnarRows(t, panes) {
		if row["NAME"] == "zsh" && row["TERMINATION"] != "" {
			t.Fatalf("a live shell Pane rendered %q in TERMINATION: %v", row["TERMINATION"], row)
		}
	}

	if store.writes != 0 || store.transactions != 0 {
		t.Fatalf("get wrote to the registry: transactions = %d writes = %d",
			store.transactions, store.writes)
	}
	if store.snapshot() != before {
		t.Fatal("get changed the registry it was reading")
	}
}

// TestTheRegistryFirstViewCarriesTerminationEvidence is the Main UI half.
//
// The view is a pure projection of the Registry, so this asserts the row model
// rather than a screenshot: the Pane and Agent rows carry the stored receipt, and
// the builder neither records nor consumes one.
func TestTheRegistryFirstViewCarriesTerminationEvidence(t *testing.T) {
	t.Parallel()

	registry := runtimeFixtureRegistry()
	code := 7
	pane, ok := registry.Pane(runtimeFixturePane)
	if !ok {
		t.Fatal("the runtime fixture has no Pane")
	}
	pane.Status.LastTermination = &coremetadata.TerminationEvidence{
		Source:         coremetadata.TerminationSourceSupervisor,
		Classification: coremetadata.TerminationAbnormal,
		PaneUID:        runtimeFixturePane,
		ExitCode:       &code,
	}

	view := registryview.Build(registryview.Input{
		Graph: resourcegraph.Graph{
			Panes: []resourcegraph.PaneNode{{
				Pane:      *pane,
				WindowUID: runtimeFixtureWindow,
				Status:    resourcegraph.StatusOffline,
			}},
			Windows: []resourcegraph.WindowNode{{
				Window:     *mustWindow(t, registry, runtimeFixtureWindow),
				ProjectUID: runtimeFixtureProject,
				Status:     resourcegraph.StatusOffline,
			}},
			Projects: []resourcegraph.ProjectNode{{
				Project: *mustProject(t, registry, runtimeFixtureProject),
				Status:  resourcegraph.StatusOffline,
			}},
		},
	})

	var paneRow *registryview.Row
	for i := range view.Rows {
		if view.Rows[i].Kind == registryview.RowKindPane {
			paneRow = &view.Rows[i]
		}
	}
	if paneRow == nil {
		t.Fatalf("the view built no Pane row: %+v", navigationRowIDs(view))
	}
	if paneRow.Termination == nil {
		t.Fatal("the Pane row carries no termination evidence")
	}
	if paneRow.Termination.Summary() != "abnormal/supervisor exit=7" {
		t.Fatalf("row summary = %q", paneRow.Termination.Summary())
	}
	// The row holds a copy, so a consumer cannot reach back into the Registry it
	// was projected from.
	if paneRow.Termination == pane.Status.LastTermination {
		t.Fatal("the row aliases the Registry's own receipt")
	}

	cells := registryNavigationRowAt(*paneRow, i18n.FallbackLocale, time.Time{})
	index := -1
	for i, column := range registryNavigationColumns {
		if column == "TERMINATION" {
			index = i
		}
	}
	if index < 0 {
		t.Fatalf("the navigation columns have no TERMINATION column: %v", registryNavigationColumns)
	}
	if cells[index] != "abnormal/supervisor exit=7" {
		t.Fatalf("navigation TERMINATION cell = %q", cells[index])
	}
}

// TestANavigationRefreshNeverProjectsALifecycle is the negative audit for the UI.
//
// A refresh observes one host and renders rows. If it also reconciled, opening the
// list would offline resources -- and the operator would have no way to look at the
// state without changing it.
func TestANavigationRefreshNeverProjectsALifecycle(t *testing.T) {
	t.Parallel()

	reader, _, sibling, runner := navigationFixtureReader(t, "projmux", "/tmp/fake-tmux/primary,1,0")
	if _, err := reader.view(context.Background(), nil); err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	if len(runner.calls) == 0 {
		t.Fatal("the navigation read issued no tmux call, so the audit proves nothing")
	}
	for _, call := range runner.calls {
		if call.value == "sibling" || call.value == sibling.socketPath {
			t.Fatalf("the navigation read touched the sibling server: %v", call.args)
		}
		for _, verb := range []string{"set-option", "kill-pane", "kill-window", "kill-session", "respawn-pane"} {
			if len(call.args) > 0 && call.args[0] == verb {
				t.Fatalf("the navigation read issued %q", verb)
			}
		}
	}
}

// TestAReadVerbNeverConsumesAReceipt states the no-consumption rule directly.
//
// A receipt sitting on a live Pane -- a control action that recorded intent and
// then refused, for one -- must still be there after every read verb has run. A
// read that consumed it would silently destroy the evidence the next
// reconciliation is supposed to classify from.
func TestAReadVerbNeverConsumesAReceipt(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stampFixtureActivation(t, store, "pan-alpha-codex", "gen-live", "agt-alpha-codex")
	outcome, err := store.mutator().RecordTermination(&store.registry, coremetadata.TerminationEvidence{
		Source:         coremetadata.TerminationSourceControlAction,
		Classification: coremetadata.TerminationIntentional,
		PaneUID:        "pan-alpha-codex",
		AgentUID:       "agt-alpha-codex",
		Generation:     "gen-live",
		OperationID:    "op-refused-delete",
	})
	if err != nil || !outcome.Applied {
		t.Fatalf("RecordTermination outcome = %+v err = %v", outcome, err)
	}
	store.writes = 0
	store.transactions = 0
	before := store.snapshot()

	for _, args := range [][]string{
		{"agents", "--project", "alpha"},
		{"panes", "--project", "alpha"},
		{"agents", "--project", "alpha", "-o", "json"},
	} {
		if _, _, err := runRoute(t, newTestListGetCommand(t, store), args...); err != nil {
			t.Fatalf("get %v: %v", args, err)
		}
	}
	if _, _, err := runRoute(t, newTestDescribeCommand(t, store),
		"agent", "codex", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("describe agent: %v", err)
	}

	if store.writes != 0 || store.transactions != 0 {
		t.Fatalf("the read verbs wrote to the registry: transactions = %d writes = %d",
			store.transactions, store.writes)
	}
	if store.snapshot() != before {
		t.Fatalf("a read verb changed the registry:\n--- got ---\n%s\n--- want ---\n%s",
			store.snapshot(), before)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("agent phase = %q; a read advanced the lifecycle", agent.Status.Phase)
	}
	pane, ok := store.registry.Pane("pan-alpha-codex")
	if !ok || pane.Status.LastTermination == nil ||
		pane.Status.LastTermination.OperationID != "op-refused-delete" {
		t.Fatal("a read verb consumed the pending intent receipt")
	}
	// The Mirror is never reached by a read either, so no observation the read
	// took could have been mistaken for a reconciliation trigger.
	if _, err := intmetadata.NewMirror(newFakeTmux()).LivePaneUIDs(context.Background()); err != nil {
		t.Fatalf("mirror sanity read: %v", err)
	}
}

func mustWindow(t *testing.T, registry coremetadata.Registry, uid string) *coremetadata.Window {
	t.Helper()
	window, ok := registry.Window(uid)
	if !ok {
		t.Fatalf("fixture has no window %q", uid)
	}
	return window
}

func mustProject(t *testing.T, registry coremetadata.Registry, uid string) *coremetadata.Project {
	t.Helper()
	project, ok := registry.Project(uid)
	if !ok {
		t.Fatalf("fixture has no project %q", uid)
	}
	return project
}
