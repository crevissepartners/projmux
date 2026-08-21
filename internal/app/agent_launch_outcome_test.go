package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestAgentLaunchOutcomeTableIsClosedAndPrintable(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"pre-runtime failure":     true,
		"created+acknowledged":    true,
		"created+unconfirmed":     true,
		"delayed acknowledgement": true,
	}
	rows := agentLaunchOutcomeTable
	if len(rows) != len(want) {
		t.Fatalf("outcome rows = %d, want %d: %#v", len(rows), len(want), rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !want[row.Outcome] || seen[row.Outcome] {
			t.Fatalf("outcome %q is outside the closed set or duplicated", row.Outcome)
		}
		seen[row.Outcome] = true
		for name, value := range map[string]string{
			"outcome": row.Outcome, "rc": row.RC, "stdout": row.Stdout,
			"resources": row.Resources, "activation": row.Activation, "diagnostic": row.Diagnostic,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s row has an empty %s cell: %#v", row.Outcome, name, row)
			}
		}
		printed := fmt.Sprintf("%s | rc=%s | stdout=%s | resources=%s | activation=%s | diagnostic=%s",
			row.Outcome, row.RC, row.Stdout, row.Resources, row.Activation, row.Diagnostic)
		for _, value := range []string{row.Outcome, row.RC, row.Stdout, row.Resources, row.Activation, row.Diagnostic} {
			if !strings.Contains(printed, value) {
				t.Fatalf("printed row %q omitted %q", printed, value)
			}
		}
	}
}

func TestDelayedProviderActivationReturnsValidatedPaneHandle(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		t.Run(provider, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)
			// Installed Claude can spend roughly three seconds reaching the
			// initial-task hook before the provider's own delayed acknowledgement.
			// Model evidence arriving beyond the old five-second command bound.
			launcher.activationDelay = 6 * time.Second

			stdout, _, err := runRoute(t, create,
				"agent", "--provider", provider, "--project", "uid:prj-alpha",
				"--window", "uid:win-alpha-review", "-o", "pane-id", "--", "initial task")
			if err != nil {
				t.Fatalf("delayed acknowledged create: %v", err)
			}
			if len(launcher.activationTimeouts) != 1 || launcher.activationTimeouts[0] != agentActivationConfirmationDeadline {
				t.Fatalf("activation deadline = %v, want %v", launcher.activationTimeouts, agentActivationConfirmationDeadline)
			}
			if launcher.activationDelay <= 5*time.Second || launcher.activationDelay >= agentActivationConfirmationDeadline {
				t.Fatalf("fixture delay %v is not beyond the old deadline and within the corrected bounded deadline", launcher.activationDelay)
			}
			if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
				t.Fatalf("stdout = %q, want one exact %%N line", stdout)
			}
			agent := agentNamed(t, store, "win-alpha-review", provider)
			if agent.Status.Activation.State != coremetadata.ActivationAcknowledged ||
				agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderHook) {
				t.Fatalf("activation = %+v", agent.Status.Activation)
			}
		})
	}
}

func TestActivationAcknowledgementWinsTheTimeoutRace(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	launcher.activationAcknowledged = false
	launcher.activationBeforeReturn = func() {
		if _, err := create.store.update(func(registry *coremetadata.Registry) error {
			for i := range registry.Agents {
				agent := &registry.Agents[i]
				if agent.Metadata.OwnerRef.UID == "win-alpha-review" && agent.Metadata.Name == "codex" {
					_, err := create.store.mutator().SetAgentActivation(registry, agent.Metadata.UID,
						coremetadata.ActivationAcknowledged, string(coremetadata.InteractionSourceProviderHook), "")
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("provider acknowledgement commit: %v", err)
		}
	}

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "uid:prj-alpha",
		"--window", "uid:win-alpha-review", "-o", "pane-id", "--", "initial task")
	if err != nil {
		t.Fatalf("timeout race returned failure after acknowledgement: %v", err)
	}
	if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
		t.Fatalf("stdout = %q, want exact acknowledged Pane handle", stdout)
	}
	committed := agentNamed(t, store, "win-alpha-review", "codex")
	if committed.Status.Activation.State != coremetadata.ActivationAcknowledged || committed.Status.Activation.Reason != "" {
		t.Fatalf("timeout writer downgraded acknowledgement: %+v", committed.Status.Activation)
	}
}

func TestLateActivationRefinesOnlyTheSameBindingGeneration(t *testing.T) {
	t.Parallel()
	h := newSessionRefHarness(t, aiModeCodex)
	for i := range h.registry.Agents {
		if h.registry.Agents[i].Metadata.UID == h.agentUID {
			h.registry.Agents[i].Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationUnconfirmed}
		}
	}
	mutator := coremetadata.Mutator{Now: func() time.Time { return sessionRefObservedAt }}
	if _, err := mutator.RecordPaneActivation(h.registry, h.paneUID, coremetadata.PaneActivationOptions{
		Generation: "gen-replacement", AgentUID: h.agentUID, OperationID: "op-replacement",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ObservePaneActivationRuntime(h.registry, h.paneUID, "gen-replacement", "%7"); err != nil {
		t.Fatal(err)
	}

	// The hook inherited the generation of the process that was replaced. Even
	// though the durable Pane uid, Agent uid and raw %N are all reused, that
	// evidence cannot acknowledge the replacement.
	if _, managed, err := h.cmd.persistManagedAgentInteraction("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook)); err != nil || !managed {
		t.Fatalf("stale hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationUnconfirmed {
		t.Fatalf("stale generation refined replacement to %q", got)
	}

	// RuntimeID is part of the same exact binding guard.
	baseEnv := h.cmd.lookupEnv
	h.cmd.lookupEnv = func(name string) string {
		switch name {
		case internalActivationPaneUIDEnv:
			return h.paneUID
		case internalActivationGenerationEnv:
			return "gen-replacement"
		default:
			return baseEnv(name)
		}
	}
	pane, _ := h.registry.Pane(h.paneUID)
	pane.Status.Activation.RuntimeID = "%99"
	if _, managed, err := h.cmd.persistManagedAgentInteraction("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook)); err != nil || !managed {
		t.Fatalf("wrong-runtime hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationUnconfirmed {
		t.Fatalf("wrong runtime handle refined activation to %q", got)
	}

	pane.Status.Activation.RuntimeID = "%7"
	if _, managed, err := h.cmd.persistManagedAgentInteraction("%7", coremetadata.InteractionInProgress,
		string(coremetadata.InteractionSourceProviderHook)); err != nil || !managed {
		t.Fatalf("exact hook projection managed=%t err=%v", managed, err)
	}
	if got := h.agent(t).Status.Activation.State; got != coremetadata.ActivationAcknowledged {
		t.Fatalf("same exact binding did not refine activation: %q", got)
	}
}

func TestRoadmapWorkerLaunchUsesExactOwnerPane(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	window := seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	owner := newFakeTmuxPane(tmux.mint("%"))
	owner.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	window.panes = append(window.panes, owner)
	create, _ := newTestAgentCreateCommand(t, store, tmux)

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex",
		"--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "--pane", "uid:pan-alpha-codex",
		"-o", "pane-id", "--", "Phase 16")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^%[0-9]+\n$`).MatchString(stdout) {
		t.Fatalf("stdout = %q", stdout)
	}
	found := false
	for _, call := range tmux.calls {
		if len(call) > 0 && call[0] == "split-window" {
			found = true
			if got := flagValue(call, "-t"); got != owner.id {
				t.Fatalf("split anchor = %q, want current owner Pane %q", got, owner.id)
			}
		}
	}
	if !found {
		t.Fatal("worker launch issued no split")
	}

	for _, test := range []struct {
		name string
		pane string
	}{
		{name: "missing", pane: "uid:pane-missing"},
		{name: "cross-window", pane: "uid:pan-alpha-review"},
		{name: "foreign-project", pane: "uid:pan-beta-zsh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)
			before, writes, panes := store.snapshot(), store.writes, tmux.paneCount()
			out, _, err := runRoute(t, create,
				"agent", "--provider", "codex", "--project", "uid:prj-alpha",
				"--window", "uid:win-alpha-main", "--pane", test.pane, "-o", "pane-id", "--", "Phase 16")
			if err == nil || out != "" {
				t.Fatalf("invalid exact anchor stdout/error = %q / %v", out, err)
			}
			if store.snapshot() != before || store.writes != writes || tmux.paneCount() != panes || tmux.argvContains("split-window") || tmux.argvContains("new-window") {
				t.Fatalf("invalid exact anchor mutated state: writes=%d panes=%d registry=%s", store.writes-writes, tmux.paneCount()-panes, store.snapshot())
			}
		})
	}
}

func TestWindowOnlyCreateNeverGuessesAroundADeadPrimaryAnchor(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	// The Window is live, but only the Agent-owned alternate Pane is live. Its
	// primary shell Pane remains in Registry and has no runtime binding.
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-codex")
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	before, panes := store.snapshot(), tmux.paneCount()

	stdout, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id")
	if err == nil || stdout != "" {
		t.Fatalf("dead-primary create stdout/error = %q / %v", stdout, err)
	}
	if store.snapshot() != before || store.writes != 0 || tmux.paneCount() != panes || tmux.argvContains("split-window") {
		raw, _ := json.Marshal(store.registry)
		t.Fatalf("dead primary guessed alternate: writes=%d panes=%d registry=%s", store.writes, tmux.paneCount(), raw)
	}
}
