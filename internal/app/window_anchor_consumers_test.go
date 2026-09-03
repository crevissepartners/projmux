package app

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func seedLiveAgentAnchor(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (*fakeTmuxWindow, string, string) {
	t.Helper()
	window, _ := store.registry.Window("win-alpha-main")
	window.Spec.AnchorPaneRef = "pan-alpha-codex"
	window.Spec.DefaultShellPaneRef = "pan-alpha-zsh"
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("Agent-anchor fixture Registry: %v", err)
	}
	session := seedLiveAgentPane(t, tmux, "alpha", window.Metadata.UID, "pan-alpha-zsh", "pan-alpha-codex")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	liveWindow := session.windows[len(session.windows)-1]
	return liveWindow, liveWindow.panes[0].id, liveWindow.panes[1].id
}

func mutationSplitCalls(tmux *fakeTmux) [][]string {
	var calls [][]string
	for _, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && argv[0] == "split-window" {
			calls = append(calls, argv)
		}
	}
	return calls
}

func TestWindowAnchorRootRoleLivenessOperationTableHasNoUnclassifiedCells(t *testing.T) {
	type cell struct {
		root      coremetadata.Kind
		role      coremetadata.PaneRole
		live      bool
		operation string
		outcome   string
	}
	roots := []coremetadata.Kind{coremetadata.KindProject, coremetadata.KindControlSession}
	roles := []coremetadata.PaneRole{coremetadata.PaneRoleShell, coremetadata.PaneRoleAgent}
	operations := []string{"create Pane", "create Agent", "resume Agent", "materialize", "focus"}
	var cells []cell
	for _, root := range roots {
		for _, role := range roles {
			for _, live := range []bool{false, true} {
				for _, operation := range operations {
					outcome := ""
					switch {
					case operation == "focus" && live:
						outcome = "navigate exact live target; Registry/topology write 0"
					case operation == "focus":
						outcome = "unresolved; Registry/topology write 0"
					case operation == "materialize" && root == coremetadata.KindControlSession:
						outcome = "outside Project materializer; preserve root unchanged"
					case operation == "materialize" && live:
						outcome = "require exact live anchor; infer no alternate Pane"
					case operation == "materialize" && role == coremetadata.PaneRoleShell:
						outcome = "create Window from exact shell anchor"
					case operation == "materialize":
						outcome = "plan lazy default shell, then replay exact Agent anchor"
					case root == coremetadata.KindControlSession && !live:
						outcome = "refuse; Project runtime bootstrap is not ControlSession authority"
					case live:
						outcome = "detached split on exact role-agnostic anchor"
					case role == coremetadata.PaneRoleShell:
						outcome = "materialize exact shell anchor, then detached split"
					default:
						outcome = "materialize lazy default shell, keep Agent anchor, then detached split"
					}
					cells = append(cells, cell{root: root, role: role, live: live, operation: operation, outcome: outcome})
				}
			}
		}
	}
	if len(cells) != len(roots)*len(roles)*2*len(operations) {
		t.Fatalf("outcome cells=%d, want %d", len(cells), len(roots)*len(roles)*2*len(operations))
	}
	seen := map[string]bool{}
	for _, row := range cells {
		key := string(row.root) + "/" + string(row.role) + "/" + strconv.FormatBool(row.live) + "/" + row.operation
		if seen[key] || strings.TrimSpace(row.outcome) == "" {
			t.Fatalf("duplicate or unclassified outcome cell %q: %+v", key, row)
		}
		seen[key] = true
	}
}

func TestAnchorAwareCreatePaneAndAgentUseExactLiveAgentAnchorDetached(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *fakeResourceStore, *fakeTmux) error
	}{
		{
			name: "Pane",
			run: func(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) error {
				command, _ := newTestResourceCreateCommand(t, store, tmux)
				_, _, err := runRoute(t, command, "pane", "--project", "alpha", "--window", "main")
				return err
			},
		},
		{
			name: "Agent",
			run: func(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) error {
				command, _ := newTestAgentCreateCommand(t, store, tmux)
				_, _, err := runRoute(t, command, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--interactive-only")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			liveWindow, _, agentAnchorID := seedLiveAgentAnchor(t, store, tmux)
			beforePanes := len(liveWindow.panes)
			if err := test.run(t, store, tmux); err != nil {
				t.Fatalf("create %s: %v", test.name, err)
			}
			calls := mutationSplitCalls(tmux)
			if len(calls) != 1 || flagValue(calls[0], "-t") != agentAnchorID || !slices.Contains(calls[0], "-d") {
				t.Fatalf("create %s split calls = %v, want one detached split on exact Agent anchor %s", test.name, calls, agentAnchorID)
			}
			if len(liveWindow.panes) != beforePanes+1 {
				t.Fatalf("create %s materialized outside exact Window: panes=%d->%d", test.name, beforePanes, len(liveWindow.panes))
			}
			window, _ := store.registry.Window("win-alpha-main")
			if window.Spec.AnchorPaneRef != "pan-alpha-codex" || window.Spec.DefaultShellPaneRef != "pan-alpha-zsh" {
				t.Fatalf("create %s rewrote Window roles: %+v", test.name, window.Spec)
			}
		})
	}
}

func TestAnchorAwareCreatePaneAndAgentUseExactLiveShellAnchorDetached(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *fakeResourceStore, *fakeTmux) error
	}{
		{name: "Pane", run: func(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) error {
			command, _ := newTestResourceCreateCommand(t, store, tmux)
			_, _, err := runRoute(t, command, "pane", "--project", "alpha", "--window", "main")
			return err
		}},
		{name: "Agent", run: func(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) error {
			command, _ := newTestAgentCreateCommand(t, store, tmux)
			_, _, err := runRoute(t, command, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--interactive-only")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			session := seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex")
			seedOwnedSession(session, "prj-alpha", "/srv/alpha")
			liveWindow := session.windows[len(session.windows)-1]
			shellAnchorID := liveWindow.panes[0].id
			if err := test.run(t, store, tmux); err != nil {
				t.Fatalf("create %s: %v", test.name, err)
			}
			calls := mutationSplitCalls(tmux)
			if len(calls) != 1 || flagValue(calls[0], "-t") != shellAnchorID || !slices.Contains(calls[0], "-d") {
				t.Fatalf("create %s split calls = %v, want one detached split on exact shell anchor %s", test.name, calls, shellAnchorID)
			}
		})
	}
}

func TestAnchorAwareCreatePaneAdoptsNewShellAsEmptyDefaultWithoutReplacingShellAnchor(t *testing.T) {
	store := newFakeResourceStore(t)
	window, _ := store.registry.Window("win-alpha-main")
	window.Spec.DefaultShellPaneRef = ""
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("shell-anchor empty-default fixture: %v", err)
	}
	tmux := newFakeTmux()
	session := seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	before := map[string]bool{}
	for _, pane := range store.registry.Panes {
		before[pane.Metadata.UID] = true
	}
	command, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, command, "pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatal(err)
	}
	window, _ = store.registry.Window("win-alpha-main")
	if window.Spec.AnchorPaneRef != "pan-alpha-zsh" {
		t.Fatalf("create replaced shell anchor: %+v", window.Spec)
	}
	if window.Spec.DefaultShellPaneRef == "" || before[window.Spec.DefaultShellPaneRef] {
		t.Fatalf("create default = %q, want newly allocated shell Pane", window.Spec.DefaultShellPaneRef)
	}
	defaultShell, ok := store.registry.WindowDefaultShell(window.Metadata.UID)
	if !ok || defaultShell.Metadata.UID != window.Spec.DefaultShellPaneRef {
		t.Fatalf("created default shell = %+v, ok=%t", defaultShell, ok)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("created graph: %v", err)
	}
}

func TestAnchorAwareExplicitPaneBeatsStoredAgentAnchor(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	_, shellID, agentAnchorID := seedLiveAgentAnchor(t, store, tmux)
	command, _ := newTestResourceCreateCommand(t, store, tmux)

	if _, _, err := runRoute(t, command, "pane", "--project", "alpha", "--window", "main", "--pane", "zsh"); err != nil {
		t.Fatal(err)
	}
	calls := mutationSplitCalls(tmux)
	if len(calls) != 1 || flagValue(calls[0], "-t") != shellID || flagValue(calls[0], "-t") == agentAnchorID {
		t.Fatalf("explicit Pane did not beat stored Agent anchor: shell=%s agent=%s calls=%v", shellID, agentAnchorID, calls)
	}
}

func TestAnchorAwarePopupOriginPaneBeatsStoredAgentAnchor(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	_, shellID, agentAnchorID := seedLiveAgentAnchor(t, store, tmux)
	command, _ := newTestResourceCreateCommand(t, store, tmux)
	withPopupOrigin(command, tmux, popupEnv(shellID))

	err := command.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: shellID,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	calls := mutationSplitCalls(tmux)
	if len(calls) != 1 || flagValue(calls[0], "-t") != shellID || flagValue(calls[0], "-t") == agentAnchorID {
		t.Fatalf("popup origin did not beat stored Agent anchor: shell=%s agent=%s calls=%v", shellID, agentAnchorID, calls)
	}
}

func TestAnchorAwareAgentResumeUsesExactLiveAgentAnchorDetached(t *testing.T) {
	store := newFakeResourceStore(t)
	addFixtureAgent(t, store, "agt-alpha-resume", "resume-me", "win-alpha-main", coremetadata.PhaseOffline, resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	liveWindow, _, agentAnchorID := seedLiveAgentAnchor(t, store, tmux)
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, command, store, "agt-alpha-resume", recorder)

	if _, _, err := runRoute(t, command, "resume", "uid:agt-alpha-resume"); err != nil {
		t.Fatal(err)
	}
	calls := mutationSplitCalls(tmux)
	if len(calls) != 1 || flagValue(calls[0], "-t") != agentAnchorID || !slices.Contains(calls[0], "-d") {
		t.Fatalf("resume split calls = %v, want one detached split on exact Agent anchor %s", calls, agentAnchorID)
	}
	resumed, _ := store.registry.Agent("agt-alpha-resume")
	if resumed.Status.Phase != coremetadata.PhaseRunning || resumed.Status.PaneRef == "" {
		t.Fatalf("resumed Agent did not bind: %+v", resumed.Status)
	}
	managed, ok := store.registry.Pane(resumed.Status.PaneRef)
	if !ok || managed.Metadata.OwnerUID() != resumed.Metadata.UID {
		t.Fatalf("resumed managed Pane = %+v, ok=%t", managed, ok)
	}
	if len(liveWindow.panes) != 3 || liveWindow.panes[2].opts[tmuxopts.PaneUID] != managed.Metadata.UID {
		t.Fatalf("resumed Pane did not materialize in exact Window: %+v", liveWindow.panes)
	}
}

func TestAnchorAwareAgentResumeUsesExactLiveShellAnchorDetached(t *testing.T) {
	store := newFakeResourceStore(t)
	addFixtureAgent(t, store, "agt-alpha-resume", "resume-me", "win-alpha-main", coremetadata.PhaseOffline, resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	session := seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex")
	seedOwnedSession(session, "prj-alpha", "/srv/alpha")
	liveWindow := session.windows[len(session.windows)-1]
	shellAnchorID := liveWindow.panes[0].id
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, command, store, "agt-alpha-resume", recorder)

	if _, _, err := runRoute(t, command, "resume", "uid:agt-alpha-resume"); err != nil {
		t.Fatal(err)
	}
	calls := mutationSplitCalls(tmux)
	if len(calls) != 1 || flagValue(calls[0], "-t") != shellAnchorID || !slices.Contains(calls[0], "-d") {
		t.Fatalf("resume split calls = %v, want one detached split on exact shell anchor %s", calls, shellAnchorID)
	}
}

func TestAnchorAwareCreateRefusesDeadAndCrossWindowAnchorsWithZeroWrites(t *testing.T) {
	for _, test := range []struct {
		name        string
		anchorUID   string
		livePaneUID string
	}{
		{name: "dead anchor with alternate live Pane", anchorUID: "pan-alpha-zsh", livePaneUID: "pan-alpha-log"},
		{name: "cross-Window anchor", anchorUID: "pan-alpha-review", livePaneUID: "pan-alpha-zsh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			window, _ := store.registry.Window("win-alpha-main")
			window.Spec.AnchorPaneRef = test.anchorUID
			tmux := newFakeTmux()
			session := tmux.addSession("alpha")
			seedOwnedSession(session, "prj-alpha", "/srv/alpha")
			seedLiveWindow(t, tmux, session, "win-alpha-main", test.livePaneUID)
			registryBefore, runtimeBefore := store.registry.Clone(), tmux.state()
			command, _ := newTestResourceCreateCommand(t, store, tmux)

			stdout, _, err := runRoute(t, command, "pane", "--project", "alpha", "--window", "main")
			if err == nil || stdout != "" {
				t.Fatalf("invalid anchor create stdout/error = %q / %v", stdout, err)
			}
			if store.writes != 0 || tmux.state() != runtimeBefore || !reflect.DeepEqual(store.registry, registryBefore) {
				t.Fatalf("invalid anchor mutated state: writes=%d\n%s", store.writes, tmux.state())
			}
			if len(mutationSplitCalls(tmux)) != 0 {
				t.Fatalf("invalid anchor inferred an alternate split: %v", mutationSplitCalls(tmux))
			}
			if !strings.Contains(err.Error(), "anchor") {
				t.Fatalf("invalid anchor refusal does not identify anchor: %v", err)
			}
		})
	}
}
