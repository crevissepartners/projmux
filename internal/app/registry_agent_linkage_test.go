package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The fixture in this file is the measured machine of 2026-08-16: a drifted
// registry, live tmux panes running Claude, and no Agent resource pointing at
// any of them.
//
// `get panes` printed an empty AGENT column for every row and `get agents`
// listed two Agents that had finished while hiding the ones that were running.
// The two are one defect: nothing connected an Agent to the pane it was running
// in, so there was nothing for either verb to report.

// aiPaneOptions is what the AI routes stamp onto a pane they launch an agent
// into. A pane the operator started an agent in by hand carries none of them.
func aiPaneOptions(provider, sessionID string) map[string]string {
	return map[string]string{
		tmuxopts.AgentProviderPane:  provider,
		tmuxopts.AgentTopicPane:     "roadmap",
		tmuxopts.AgentSessionIDPane: sessionID,
	}
}

// agentColumn resolves every Pane the way the read verbs do and returns
// name -> the AGENT owner leg the pane table prints.
func agentColumn(t *testing.T, registry coremetadata.Registry, observed coremetadata.RuntimeObservation) map[string]string {
	t.Helper()
	resolution, err := selector.NewObserved(registry, observed).ResolvePanes(selector.Query{})
	if err != nil {
		t.Fatalf("ResolvePanes: %v", err)
	}
	out := map[string]string{}
	for _, match := range resolution.Matches {
		out[match.UID] = match.Owner.Agent
	}
	return out
}

// liveObservation builds the observation the read verbs would take of a fake
// tmux server: the uids its live windows and panes still mirror.
func liveObservation(tmux *fakeTmux) coremetadata.RuntimeObservation {
	observed := coremetadata.RuntimeObservation{
		Windows: map[string]bool{},
		Panes:   map[string]bool{},
	}
	for _, session := range tmux.sessions {
		for _, window := range session.windows {
			if uid := window.opts[tmuxopts.WindowUID]; uid != "" {
				observed.Windows[uid] = true
			}
			for _, pane := range window.panes {
				if uid := pane.opts[tmuxopts.PaneUID]; uid != "" {
					observed.Panes[uid] = true
				}
			}
		}
	}
	return observed
}

// TestReconcileGivesEveryLiveAgentPaneAnAgentAndAnAgentColumn is acceptance
// criteria 0 and 0b together, at the reconciler seam.
//
// Two live panes are running agents projmux launched and one is a plain shell.
// After one pass both agent panes have an Agent, both Agents report live, and
// the shell pane is untouched.
func TestReconcileGivesEveryLiveAgentPaneAnAgentAndAnAgentColumn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	window := session.windows[0]
	window.name = "zsh"
	// window.panes[0] is the fixture's plain shell. Two more panes are running
	// agents projmux itself launched.
	firstAgent := addLivePane(tmux, window, "claude", aiPaneOptions("claude", "sess-1"))
	secondAgent := addLivePane(tmux, window, "claude", aiPaneOptions("claude", "sess-2"))

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(registry.Agents) != 2 {
		t.Fatalf("agents = %d, want one per live agent pane", len(registry.Agents))
	}

	observed := liveObservation(tmux)
	column := agentColumn(t, registry, observed)
	for _, pane := range []*fakeTmuxPane{firstAgent, secondAgent} {
		uid := pane.opts[tmuxopts.PaneUID]
		if uid == "" {
			t.Fatalf("a live agent pane still carries no @projmux_pane_uid")
		}
		if column[uid] == "" {
			t.Fatalf("pane %q has an empty AGENT column", uid)
		}
		registered, _ := registry.Pane(uid)
		if registered.Spec.Role != coremetadata.PaneRoleAgent {
			t.Fatalf("pane %q role = %q, want %q", uid, registered.Spec.Role, coremetadata.PaneRoleAgent)
		}
	}
	// The plain shell pane is not an agent and must not acquire one.
	if uid := window.panes[0].opts[tmuxopts.PaneUID]; column[uid] != "" {
		t.Fatalf("the shell pane %q was given AGENT %q", uid, column[uid])
	}

	// And the live count agrees with the machine: every Agent resolves live,
	// because every one of them has a live managed Pane.
	agents, err := selector.NewObserved(registry, observed).ResolveAgents(selector.Query{})
	if err != nil {
		t.Fatalf("ResolveAgents: %v", err)
	}
	live := 0
	for _, match := range agents.Matches {
		if match.Status == selector.StatusLive {
			live++
		}
	}
	if live != 2 {
		t.Fatalf("live agents = %d, want the 2 live agent panes", live)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after linkage: %v", err)
	}
}

func TestAutomaticReconcilePreservesCanonicalShellWithGenericAgentMarker(t *testing.T) {
	t.Parallel()

	_, store, tmux, _, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	session := tmux.session("alpha")
	liveWindow, livePane := session.windows[0], session.windows[0].panes[0]
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	liveWindow.opts[tmuxopts.WindowUID] = window.Metadata.UID
	liveWindow.opts[tmuxopts.WindowName] = window.Metadata.Name
	liveWindow.opts[tmuxopts.AutomaticRenameWindow] = "off"
	livePane.opts[tmuxopts.PaneUID] = pane.Metadata.UID
	livePane.opts[tmuxopts.PaneName] = pane.Metadata.Name
	livePane.opts[tmuxopts.AgentProviderPane] = "codex"
	livePane.command = "codex"
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, window.Metadata.UID, session.id, liveWindow.id); err != nil {
		t.Fatalf("seed exact Window runtime binding: %v", err)
	}

	registryBefore, tmuxBefore := store.registry.Clone(), tmux.state()
	reconciler := reconcileFixtureReconciler(root, "alpha")(tmux, inttmux.NewClient(tmux))
	reconciler.refuseForeign = true
	for pass := 1; pass <= 2; pass++ {
		if err := reconciler.reconcile(context.Background(), &store.registry, store.mutator(), "op-canonical-shell"); err != nil {
			t.Fatalf("automatic reconcile pass %d: %v", pass, err)
		}
		if !reflect.DeepEqual(registryBefore, store.registry) || tmux.state() != tmuxBefore || tmuxMutationCallCount(tmux) != 0 {
			t.Fatalf("automatic reconcile pass %d changed Registry or tmux\nbefore=%+v\nafter=%+v\ntmux=%s", pass, registryBefore, store.registry, tmux.state())
		}
	}
	protected, _ := store.registry.Pane(pane.Metadata.UID)
	storedWindow, _ := store.registry.Window(window.Metadata.UID)
	if len(store.registry.Agents) != 0 || protected.Metadata.OwnerUID() != window.Metadata.UID || protected.Spec.Role != coremetadata.PaneRoleShell ||
		storedWindow.Spec.AnchorPaneRef != pane.Metadata.UID || storedWindow.Spec.DefaultShellPaneRef != pane.Metadata.UID {
		t.Fatalf("canonical shell was promoted or refs changed: agents=%d pane=%+v window=%+v", len(store.registry.Agents), protected, storedWindow.Spec)
	}
}

// TestAgentLinkageIsIdempotentAcrossPasses is the convergence property. The
// reconciler runs on every mutation route, so a linkage that minted a fresh
// Agent each time would grow the Agent list without bound on a machine that
// never changed.
func TestAgentLinkageIsIdempotentAcrossPasses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "zsh"
	addLivePane(tmux, session.windows[0], "claude", aiPaneOptions("claude", "sess-1"))

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	mutator := fixtureMutator()

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	afterFirst := registry.Clone()
	tmuxAfterFirst := tmux.state()

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !reflect.DeepEqual(registry, afterFirst) {
		t.Fatalf("a second reconcile changed the registry:\nbefore %+v\nafter  %+v", afterFirst, registry)
	}
	if got := tmux.state(); got != tmuxAfterFirst {
		t.Fatalf("a second reconcile changed tmux:\nbefore\n%s\nafter\n%s", tmuxAfterFirst, got)
	}
}

// TestAgentLinkageNeedsProjmuxAuthorshipNotACommandName is the evidence rule as
// a table, and it is the decision the whole phase turns on.
//
// `pane_current_command == claude` says a process called claude is running. It
// does not say projmux started it, and it is equally true of a pane the operator
// typed `claude` into. `@projmux_ai_agent` is written by the AI routes when
// projmux launches an agent, so it is authorship. Only authorship links.
func TestAgentLinkageNeedsProjmuxAuthorshipNotACommandName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		command   string
		opts      map[string]string
		wantAgent bool
	}{
		{
			name:      "projmux launched an agent into the pane",
			command:   "claude",
			opts:      aiPaneOptions("claude", "sess-1"),
			wantAgent: true,
		},
		{
			name:      "the operator typed claude into a shell",
			command:   "claude",
			wantAgent: false,
		},
		{
			name:      "a shell whose title mentions an agent",
			command:   "zsh",
			wantAgent: false,
		},
		{
			// The marker is what counts, not whether projmux recognizes the
			// spelling: a provider projmux has never heard of is still a pane
			// projmux launched an agent into.
			name:      "an unrecognized provider spelling",
			command:   "sfm",
			opts:      map[string]string{tmuxopts.AgentProviderPane: "some-future-model"},
			wantAgent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tmux := newFakeTmux()
			session := tmux.addSession(driftedSessionName)
			session.windows[0].name = "zsh"
			pane := addLivePane(tmux, session.windows[0], tt.command, tt.opts)

			reconciler := newTestReconciler(tmux, []string{root})
			reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
			registry := driftedRegistry(t, root)

			if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			uid := pane.opts[tmuxopts.PaneUID]
			if uid == "" {
				t.Fatal("the live pane was not registered at all")
			}
			got := agentColumn(t, registry, liveObservation(tmux))[uid] != ""
			if got != tt.wantAgent {
				t.Fatalf("pane has an AGENT = %t, want %t", got, tt.wantAgent)
			}
			if !tt.wantAgent && len(registry.Agents) != 0 {
				t.Fatalf("a pane with no authorship marker minted %d Agents", len(registry.Agents))
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("registry does not validate: %v", err)
			}
		})
	}
}

// TestReconcileToleratesAnAgentLinkItCannotComplete keeps the new write inside
// the tolerance the rest of the binding-repair walk already has: this is
// maintenance riding along inside somebody else's transaction, so a link that
// cannot be made must not fail the operator's create.
func TestReconcileToleratesAnAgentLinkItCannotComplete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "zsh"
	agentPane := addLivePane(tmux, session.windows[0], "claude", aiPaneOptions("claude", "sess-1"))

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	mutator := fixtureMutator()
	mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
		if kind == coremetadata.KindAgent {
			return "", errors.New("uid source is unavailable")
		}
		return coremetadata.NewUID(kind)
	}

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("a failed agent link failed the whole reconcile: %v", err)
	}
	if len(registry.Agents) != 0 {
		t.Fatalf("agents = %d, want 0 after a failed mint", len(registry.Agents))
	}
	// The Pane registration and its binding, which succeeded, are kept.
	uid := agentPane.opts[tmuxopts.PaneUID]
	if uid == "" {
		t.Fatal("a failed agent link rolled back the pane binding that had succeeded")
	}
	pane, ok := registry.Pane(uid)
	if !ok {
		t.Fatalf("the mirrored uid %q names no registry Pane", uid)
	}
	if pane.Spec.Role != coremetadata.PaneRoleShell || pane.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
		t.Fatalf("a failed link left the Pane half-promoted: %+v", pane.Metadata.OwnerRef)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after a failed link: %v", err)
	}
}

// TestLinkedAgentGoesOfflineWhenItsPaneDiesAndStaysQueryable is the preservation
// half. The Agent resource survives a runtime that vanished; only its observed
// status changes, and its metadata is still there to select.
func TestLinkedAgentGoesOfflineWhenItsPaneDiesAndStaysQueryable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	window := session.windows[0]
	window.name = "zsh"
	addLivePane(tmux, window, "claude", aiPaneOptions("claude", "sess-1"))

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(registry.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(registry.Agents))
	}
	agentUID := registry.Agents[0].Metadata.UID

	statusOf := func(observed coremetadata.RuntimeObservation) selector.Status {
		t.Helper()
		resolution, err := selector.NewObserved(registry, observed).ResolveAgents(selector.Query{})
		if err != nil {
			t.Fatalf("ResolveAgents: %v", err)
		}
		for _, match := range resolution.Matches {
			if match.UID == agentUID {
				return match.Status
			}
		}
		t.Fatalf("agent %q is no longer queryable", agentUID)
		return ""
	}

	if got := statusOf(liveObservation(tmux)); got != selector.StatusLive {
		t.Fatalf("a running agent reports %q, want live", got)
	}
	// The operator closes the pane. No hook fires and no reconcile runs; the
	// next read observes the machine and answers from it.
	window.panes = window.panes[:len(window.panes)-1]
	if got := statusOf(liveObservation(tmux)); got != selector.StatusOffline {
		t.Fatalf("an agent whose pane closed reports %q, want offline", got)
	}
	if _, ok := registry.Agent(agentUID); !ok {
		t.Fatal("the Agent was deleted when its runtime vanished")
	}
}

// TestALinkedPaneJoinsTheManagedPaneLifecycle is the consequence of linkage
// that a reader has to know about, pinned rather than left implicit.
//
// The dead-agent-pane sweep -- shipped by the Agent liveness track and untouched
// here -- releases an Agent whose managed Pane died, and releasing removes that
// Pane row. A pane that used to be registered as a Window-owned shell was never
// in that sweep's reach and its offline row accumulated forever; once it is
// correctly registered as the managed Pane of an Agent, it follows the managed
// lifecycle instead. That is the existing contract for managed Panes applying to
// panes that genuinely are managed, not a new deletion rule: the *Agent* is
// preserved, keeps its name and uid, keeps its conversation ref, and stays
// resumable.
func TestALinkedPaneJoinsTheManagedPaneLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	window := session.windows[0]
	window.name = "zsh"
	addLivePane(tmux, window, "claude", aiPaneOptions("claude", "sess-1"))

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	mutator := fixtureMutator()

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(registry.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(registry.Agents))
	}
	agentUID := registry.Agents[0].Metadata.UID
	agentName := registry.Agents[0].Metadata.Name

	// The pane exits and a later mutation route reconciles.
	window.panes = window.panes[:len(window.panes)-1]
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	agent, ok := registry.Agent(agentUID)
	if !ok {
		t.Fatal("the Agent was deleted along with its managed Pane")
	}
	if agent.Metadata.Name != agentName {
		t.Fatalf("the Agent was renamed to %q", agent.Metadata.Name)
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("released agent status = %+v, want Offline with no paneRef", agent.Status)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after the sweep: %v", err)
	}
}
