package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The `create window --provider` contract.
//
// The route answers two questions with one command: which Window, and what it
// opens on. Every test below fixes one of the two, and the pair the route must
// never blur is the first two: an argv that names no provider and an argv that
// names `shell` have to produce the Window this route has always produced,
// while an argv that names an Agent provider has to produce a Window holding
// that Agent and nothing else.

// windowPanes projects every Pane inside one Window in Registry order.
//
// It resolves the Agent hop on purpose: a managed Agent Pane's ownerRef names
// its Agent, so PanesOf(windowUID) alone answers "the Window's direct shell
// Panes", which is precisely the question that cannot tell an Agent-only Window
// from an empty one.
func windowPanes(store *fakeResourceStore, windowUID string) []coremetadata.Pane {
	agentOwned := map[string]bool{}
	for _, agent := range store.registry.Agents {
		if agent.Metadata.OwnerUID() == windowUID {
			agentOwned[agent.Metadata.UID] = true
		}
	}
	var panes []coremetadata.Pane
	for _, pane := range store.registry.Panes {
		owner := pane.Metadata.OwnerUID()
		if owner == windowUID || agentOwned[owner] {
			panes = append(panes, pane)
		}
	}
	return panes
}

// lastWindowOf is the Window a create just appended under a Project.
func lastWindowOf(t *testing.T, store *fakeResourceStore, projectUID string) coremetadata.Window {
	t.Helper()
	windows := store.registry.WindowsOf(projectUID)
	if len(windows) == 0 {
		t.Fatalf("project %q has no Window; registry:\n%s", projectUID, store.snapshot())
	}
	return windows[len(windows)-1]
}

// liveMirroredPaneUIDs lists the Projmux uids tmux currently mirrors inside one
// window id, which is the only way to tell "the Registry forgot this Pane" from
// "the Pane is gone from tmux too".
func liveMirroredPaneUIDs(tmux *fakeTmux, windowID string) []string {
	var uids []string
	for _, session := range tmux.sessions {
		for _, window := range session.windows {
			if window.id != windowID {
				continue
			}
			for _, pane := range window.panes {
				uids = append(uids, pane.opts[tmuxopts.PaneUID])
			}
		}
	}
	return uids
}

// TestCreateWindowWithAProviderCommitsOneAgentPaneAndNoShell is Task 2
// acceptance 1: the Window this route commits holds the Agent's managed Pane,
// that Pane is its anchor, and it has no default shell at all -- in the
// Registry and on the tmux server alike.
func TestCreateWindowWithAProviderCommitsOneAgentPaneAndNoShell(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)

	stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude")
	if err != nil {
		t.Fatalf("create window --provider claude error = %v", err)
	}

	window := lastWindowOf(t, store, "prj-alpha")
	panes := windowPanes(store, window.Metadata.UID)
	if len(panes) != 1 {
		t.Fatalf("Window Panes = %+v, want exactly one", panes)
	}
	agentPane := panes[0]
	if agentPane.Spec.Role != coremetadata.PaneRoleAgent {
		t.Fatalf("remaining Pane role = %q, want %q", agentPane.Spec.Role, coremetadata.PaneRoleAgent)
	}
	if window.Spec.AnchorPaneRef != agentPane.Metadata.UID {
		t.Fatalf("anchorPaneRef = %q, want the Agent Pane %q", window.Spec.AnchorPaneRef, agentPane.Metadata.UID)
	}
	if window.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("defaultShellPaneRef = %q, want empty on an Agent-only Window", window.Spec.DefaultShellPaneRef)
	}
	agents := store.registry.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 || agents[0].Status.PaneRef != agentPane.Metadata.UID ||
		agents[0].Spec.Provider != "claude" || agents[0].Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("Window Agents = %+v, want one running claude Agent bound to %q", agents, agentPane.Metadata.UID)
	}

	// tmux: the Window carries exactly the one mirrored Pane the Registry
	// describes. A shell left behind here is the `create agent --create-window`
	// shape this route exists to avoid.
	_, windowID := liveWindowWithUID(t, tmux, window.Metadata.UID)
	if got := liveMirroredPaneUIDs(tmux, windowID); !slices.Equal(got, []string{agentPane.Metadata.UID}) {
		t.Fatalf("live mirrored Pane uids = %v, want just the Agent Pane %q; tmux:\n%s",
			got, agentPane.Metadata.UID, tmux.state())
	}
	if !tmux.argvContains("split-window") || !tmux.argvContains("kill-pane") {
		t.Fatalf("the Agent Pane was not split off and the initial shell not killed; tmux:\n%s", tmux.state())
	}
	if got := len(launcher.plans); got != 1 || launcher.plans[0].provider != "claude" {
		t.Fatalf("provider launches = %+v, want exactly one claude launch", launcher.plans)
	}
	if !strings.HasPrefix(stdout, "window/"+window.Metadata.Name+" created") {
		t.Fatalf("stdout = %q, want the Window create line first", stdout)
	}
}

// TestCreateWindowWithoutAProviderAndWithShellAreTheSameShellWindow is Task 2
// acceptance 2 and the compatibility half of the flag: the two argvs that do
// not name an Agent must not be able to produce two different Windows, and
// neither may reach the provider launcher at all.
func TestCreateWindowWithoutAProviderAndWithShellAreTheSameShellWindow(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "flag omitted", args: []string{"window", "--project", "alpha"}},
		{name: "shell spelled out", args: []string{"window", "--project", "alpha", "--provider", "shell"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)

			if _, _, err := runRoute(t, create, test.args...); err != nil {
				t.Fatalf("create window error = %v", err)
			}
			window := lastWindowOf(t, store, "prj-alpha")
			panes := windowPanes(store, window.Metadata.UID)
			if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell {
				t.Fatalf("Window Panes = %+v, want exactly one shell Pane", panes)
			}
			if window.Spec.AnchorPaneRef != panes[0].Metadata.UID || window.Spec.DefaultShellPaneRef != panes[0].Metadata.UID {
				t.Fatalf("Window refs = anchor %q shell %q, want both on %q",
					window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef, panes[0].Metadata.UID)
			}
			if got := store.registry.AgentsOf(window.Metadata.UID); len(got) != 0 {
				t.Fatalf("a shell Window opened an Agent: %+v", got)
			}
			if len(launcher.plans) != 0 {
				t.Fatalf("a shell Window reached the provider launcher: %+v", launcher.plans)
			}
			if tmux.argvContains("split-window") || tmux.argvContains("kill-pane") {
				t.Fatalf("a shell Window split or killed a Pane; tmux:\n%s", tmux.state())
			}
		})
	}
}

// TestCreateWindowRefusesNonProviderSurfacesFromArgvAlone is Task 2 acceptance
// 3: the value set is `shell` plus the canonical Agent providers, the picker
// adapters are named as the selection surfaces they are, and every refusal
// lands before the store is opened.
func TestCreateWindowRefusesNonProviderSurfacesFromArgvAlone(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value string
		want        []string
	}{
		{
			name: "split picker adapter", value: "selective",
			want: []string{`create window: "selective" is an interactive picker, not a provider`,
				"`projmux create window --provider`"},
		},
		{
			name: "resume picker adapter", value: "resume",
			want: []string{`create window: "resume" is an interactive picker, not a provider`,
				"`projmux create window --provider`"},
		},
		{
			name: "unknown value", value: "nosuchprovider",
			want: []string{`create window: unknown provider "nosuchprovider"`, "accepted providers:"},
		},
		{
			name: "explicit empty value", value: "",
			want: []string{"create window requires --provider"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", test.value)
			if err == nil {
				t.Fatalf("--provider %q was accepted", test.value)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal = %q, missing %q", err, want)
				}
			}
			if stdout != "" || store.writes != 0 || tmux.windowCount() != 0 || tmux.paneCount() != 0 {
				t.Fatalf("an argv refusal wrote something: stdout=%q registry writes=%d windows=%d panes=%d",
					stdout, store.writes, tmux.windowCount(), tmux.paneCount())
			}
			if len(launcher.plans) != 0 {
				t.Fatalf("an argv refusal reached the provider launcher: %+v", launcher.plans)
			}
		})
	}
}

// TestCreateWindowProviderFailureLeavesNoWindowAndNoPane is Task 2 acceptance
// 4, and the difference from `create agent --create-window`.
//
// Both authorities are checked, because either one alone would pass for the
// wrong reason: a rolled-back Registry beside a live tmux window is a leak, and
// a killed tmux window beside a committed Window row is drift. The two
// injections are the two ends of the operation -- a launch that cannot be built
// at all, and a split that tmux refuses after the Window is already live.
func TestCreateWindowProviderFailureLeavesNoWindowAndNoPane(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		inject func(*fakeTmux, *fakeAgentLauncher)
	}{
		{
			name: "provider launch cannot be built",
			inject: func(_ *fakeTmux, launcher *fakeAgentLauncher) {
				launcher.planErr = errors.New("claude: executable file not found in $PATH")
			},
		},
		{
			name: "tmux refuses the Agent split",
			inject: func(tmux *fakeTmux, _ *fakeAgentLauncher) {
				tmux.fail = []string{"split-window"}
				tmux.failMessage = "split-window: no space for new pane"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)
			windowsBefore := len(store.registry.WindowsOf("prj-alpha"))
			panesBefore := paneUIDsByWindow(store)
			agentsBefore := len(store.registry.Agents)
			test.inject(tmux, launcher)

			stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude")
			if err == nil {
				t.Fatal("a refused Agent returned ordinary success")
			}
			if stdout != "" {
				t.Fatalf("a refused Agent wrote success output %q", stdout)
			}
			if got := len(store.registry.WindowsOf("prj-alpha")); got != windowsBefore {
				t.Fatalf("Windows = %d, want the %d this Project had before; registry:\n%s",
					got, windowsBefore, store.snapshot())
			}
			if got := paneUIDsByWindow(store); !panesEqual(got, panesBefore) {
				t.Fatalf("Panes by Window = %v, want the pre-create set %v; registry:\n%s",
					got, panesBefore, store.snapshot())
			}
			if got := len(store.registry.Agents); got != agentsBefore {
				t.Fatalf("Agents = %d, want the %d the fixture had before; registry:\n%s",
					got, agentsBefore, store.snapshot())
			}
			for _, session := range tmux.sessions {
				for _, window := range session.windows {
					if window.opts[tmuxopts.WindowUID] != "" {
						t.Fatalf("a refused Agent left the mirrored tmux window %s alive; tmux:\n%s",
							window.id, tmux.state())
					}
				}
			}
		})
	}
}

// panesEqual compares two Window->Pane-uid projections.
func panesEqual(got, want map[string][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for window, uids := range want {
		if !slices.Equal(got[window], uids) {
			return false
		}
	}
	return true
}

// TestCreateWindowProviderPayloadIsTheAgentsInitialTask is Task 2 acceptance 5
// and the naming half of Epic decision 9.
//
// The payload changes meaning with --provider -- initial Agent task instead of
// the initial Pane's command -- but it must not change what it always also fed:
// the stored command the Window and its first Pane derive from it. The shell
// Pane is therefore launched with no payload while the derivation stays put.
func TestCreateWindowProviderPayloadIsTheAgentsInitialTask(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, create,
		"window", "--project", "alpha", "--provider", "claude", "--", "review this branch"); err != nil {
		t.Fatalf("create window --provider claude -- <payload> error = %v", err)
	}

	if len(launcher.plans) != 1 || !slices.Equal(launcher.plans[0].payload, []string{"review this branch"}) {
		t.Fatalf("provider launches = %+v, want one carrying the payload as the initial task", launcher.plans)
	}
	window := lastWindowOf(t, store, "prj-alpha")
	agents := store.registry.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 || agents[0].Status.Activation.State != coremetadata.ActivationAcknowledged {
		t.Fatalf("Window Agents = %+v, want one Agent whose initial task was acknowledged", agents)
	}
	// The payload never became the shell Pane's supervised child. That Pane is
	// gone by now, so the evidence is the argv tmux was actually given.
	for _, call := range tmux.calls {
		if !slices.Contains(call, "new-window") {
			continue
		}
		if strings.Contains(strings.Join(call, " "), "review this branch") {
			t.Fatalf("the Agent's initial task was launched as the initial Pane's command: %q", call)
		}
	}
	// An Agent's initial task is not durable state. allocateWindow stores the
	// payload as the initial Pane's command -- the derivation --provider must not
	// change -- so the retirement of that Pane is what keeps the prompt out of
	// the Registry, and only a byte scan of the committed file proves it.
	raw, marshalErr := json.Marshal(store.registry)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(raw), "review this branch") {
		t.Fatalf("the Agent's initial task persisted in the Registry: %s", raw)
	}
}

// TestCreateWindowProviderProjectionsNameTheRemainingPane is Task 2 acceptance
// 7: every existing projection of this route stays coherent on the provider
// branch, and the receipt records the Agent the operation actually opened.
func TestCreateWindowProviderProjectionsNameTheRemainingPane(t *testing.T) {
	t.Parallel()

	t.Run("pane-id names the Agent Pane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude", "-o", "pane-id")
		if err != nil {
			t.Fatalf("create window -o pane-id error = %v", err)
		}
		window := lastWindowOf(t, store, "prj-alpha")
		panes := windowPanes(store, window.Metadata.UID)
		if len(panes) != 1 {
			t.Fatalf("Window Panes = %+v, want exactly one", panes)
		}
		want := livePaneWithUID(t, tmux, panes[0].Metadata.UID)
		if strings.TrimSpace(stdout) != want {
			t.Fatalf("-o pane-id = %q, want the Agent Pane handle %q", stdout, want)
		}
	})

	t.Run("receipt records the Window and the Agent", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)

		stdout, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude", "-o", "receipt")
		if err != nil {
			t.Fatalf("create window -o receipt error = %v", err)
		}
		var receipt struct {
			Operation   string `json:"operation"`
			Target      struct{ Kind, UID string }
			Cardinality struct {
				Windows int `json:"windows"`
				Agents  int `json:"agents"`
				Panes   int `json:"panes"`
			} `json:"cardinality"`
			AffectedUIDs []struct {
				Kind, UID, Action string
			} `json:"affectedUids"`
			SelectedWindowUIDs []string `json:"selectedWindowUids"`
		}
		if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
			t.Fatalf("receipt is not JSON: %v\n%s", err, stdout)
		}
		window := lastWindowOf(t, store, "prj-alpha")
		agents := store.registry.AgentsOf(window.Metadata.UID)
		if len(agents) != 1 {
			t.Fatalf("Window Agents = %+v, want exactly one", agents)
		}
		if receipt.Operation != "create.window" || receipt.Target.UID != window.Metadata.UID {
			t.Fatalf("receipt operation/target = %q/%q, want create.window on %q",
				receipt.Operation, receipt.Target.UID, window.Metadata.UID)
		}
		if receipt.Cardinality.Windows != 1 || receipt.Cardinality.Agents != 1 {
			t.Fatalf("receipt cardinality = %+v, want one Window and one Agent", receipt.Cardinality)
		}
		if !slices.Equal(receipt.SelectedWindowUIDs, []string{window.Metadata.UID}) {
			t.Fatalf("receipt selected Windows = %v, want just %q", receipt.SelectedWindowUIDs, window.Metadata.UID)
		}
		found := false
		for _, affected := range receipt.AffectedUIDs {
			if affected.Kind == "agent" && affected.UID == agents[0].Metadata.UID && affected.Action == "created" {
				found = true
			}
		}
		if !found {
			t.Fatalf("receipt affected uids = %+v, want a created agent row for %q",
				receipt.AffectedUIDs, agents[0].Metadata.UID)
		}
	})
}

// TestCreateWindowProviderRefusesAPromptedCodexCreate states the one Agent
// shape this route cannot open the way `create agent` opens it.
//
// A prompted Codex create is native-required there: it binds an exact
// app-server thread to the Agent and refuses rather than fall back. This route
// has no native lane, so accepting the same argv would hand back a plain
// interactive Agent with no thread binding -- an Agent `agent turn` cannot
// drive. A payload-free Codex create is plain on both routes and stays allowed.
func TestCreateWindowProviderRefusesAPromptedCodexCreate(t *testing.T) {
	t.Parallel()

	t.Run("prompted codex is refused before any write", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)

		_, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "codex", "--", "do the thing")
		if err == nil {
			t.Fatal("a prompted codex create window was accepted")
		}
		for _, want := range []string{
			"create window --provider codex does not accept a payload",
			"projmux create agent --provider codex --create-window",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal = %q, missing %q", err, want)
			}
		}
		if store.writes != 0 || tmux.windowCount() != 0 {
			t.Fatalf("the refusal wrote something: registry writes=%d tmux windows=%d", store.writes, tmux.windowCount())
		}
	})

	t.Run("payload-free codex opens the plain lane", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, launcher := newTestAgentCreateCommand(t, store, tmux)

		if _, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "codex"); err != nil {
			t.Fatalf("payload-free create window --provider codex error = %v", err)
		}
		window := lastWindowOf(t, store, "prj-alpha")
		panes := windowPanes(store, window.Metadata.UID)
		if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleAgent {
			t.Fatalf("Window Panes = %+v, want exactly one Agent Pane", panes)
		}
		if len(launcher.plans) != 1 || launcher.plans[0].provider != "codex" || len(launcher.plans[0].payload) != 0 {
			t.Fatalf("provider launches = %+v, want one payload-free codex launch", launcher.plans)
		}
	})
}

// TestCreateWindowProviderHonoursTheEnabledAgentsGate keeps the Settings gate
// on this spelling too: naming a provider through a different command does not
// re-enable one the operator switched off, and the refusal costs nothing.
func TestCreateWindowProviderHonoursTheEnabledAgentsGate(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	launcher.disabled["claude"] = true

	_, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude")
	if err == nil {
		t.Fatal("a disabled provider was launched")
	}
	if !strings.Contains(err.Error(), "claude is disabled") {
		t.Fatalf("refusal = %q, want the Settings gate's wording", err)
	}
	if store.writes != 0 || tmux.windowCount() != 0 || len(launcher.plans) != 0 {
		t.Fatalf("a disabled provider wrote something: registry writes=%d tmux windows=%d plans=%+v",
			store.writes, tmux.windowCount(), launcher.plans)
	}
}

// TestCreateWindowRejectsTheAgentTuningFlagsItDoesNotOwn keeps the two routes
// distinct. `create window --provider` is the convenience path for a Window
// holding one Agent; the complete Agent path with model, effort, extra writable
// roots and fan-out is `create agent --create-window`, and a flag registered on
// both would make that boundary a matter of documentation rather than of argv.
func TestCreateWindowRejectsTheAgentTuningFlagsItDoesNotOwn(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--model", "--effort", "--add-dir", "--cwd", "--placement", "--window", "--create-window", "--cwd-from"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestAgentCreateCommand(t, store, tmux)

			_, _, err := runRoute(t, create, "window", "--project", "alpha", "--provider", "claude", flag, "x")
			if err == nil {
				t.Fatalf("create window accepted %s", flag)
			}
			if store.writes != 0 || tmux.windowCount() != 0 {
				t.Fatalf("%s wrote something: registry writes=%d tmux windows=%d", flag, store.writes, tmux.windowCount())
			}
		})
	}
	// --interactive-only takes no operand, so it is spelled on its own.
	store := newFakeResourceStore(t)
	create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, create,
		"window", "--project", "alpha", "--provider", "codex", "--interactive-only"); err == nil {
		t.Fatal("create window accepted --interactive-only")
	}
}
