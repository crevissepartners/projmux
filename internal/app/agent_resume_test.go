package app

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
)

// resumeFixtureConversation is the Codex conversation the resume fixtures store
// on an Agent. It is deliberately not a substring of any other fixture value, so
// "the launched argv addresses this conversation" is assertable by search.
const resumeFixtureConversation = "thr-resume-0001"

// resumeFixtureRef builds the stored pointer the resume fixtures use.
func resumeFixtureRef(observedAt time.Time) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: observedAt,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID:  resumeFixtureConversation,
			SessionID: "sess-resume-0001",
		},
	}
}

// fakeResumeLauncher stands in for the provider resume-launch half of the AI
// command.
//
// It records what the route asked for rather than what it did, which is what
// makes the central property directly countable: every argv this route can
// possibly launch is built here, from a conversation id, and there is no method
// on it that builds a fresh-start launch at all.
type fakeResumeLauncher struct {
	mu sync.Mutex
	// disabled names the providers the Settings gate refuses.
	disabled map[string]bool
	// planErr fails the resume construction, the way an unusable conversation id
	// or a missing provider binary does.
	planErr error
	// plans records one entry per successful resume construction.
	plans []fakeResumeRequest
	// gated records one entry per Settings gate call.
	gated []string
	// bound records one entry per managed-pane binding.
	bound []fakeResumedPane
}

type fakeResumeRequest struct {
	provider       string
	contextDir     string
	conversationID string
}

type fakeResumedPane struct {
	paneID         string
	provider       string
	title          string
	conversationID string
}

func newFakeResumeLauncher() *fakeResumeLauncher {
	return &fakeResumeLauncher{disabled: map[string]bool{}}
}

func (f *fakeResumeLauncher) RequireAgentEnabled(provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gated = append(f.gated, provider)
	if f.disabled[provider] {
		return errors.New("AI agent " + provider + " is disabled in Settings > AI Settings > Enabled agents")
	}
	return nil
}

func (f *fakeResumeLauncher) PlanAgentResume(provider, contextDir, conversationID string) (string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.planErr != nil {
		return "", nil, f.planErr
	}
	f.plans = append(f.plans, fakeResumeRequest{
		provider:       provider,
		contextDir:     contextDir,
		conversationID: conversationID,
	})
	// The shape mirrors the real seam: a shell wrapper whose exec tail is the
	// provider's own resume argv, so the conversation id is observable in the
	// argv the split actually receives.
	return provider + ":resume", []string{"sh", "-lc", "exec " + provider + " resume " + conversationID}, nil
}

func (f *fakeResumeLauncher) BindResumedAgentPane(paneID, provider, _, title, conversationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = append(f.bound, fakeResumedPane{
		paneID: paneID, provider: provider, title: title, conversationID: conversationID,
	})
}

// newTestAgentResumeCommand wires the Agent namespace onto the in-memory
// registry, the in-memory tmux server, and a recording resume launcher.
func newTestAgentResumeCommand(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (
	*agentCommand, *fakeResumeLauncher, *recordingArgv, *recordingArgv,
) {
	t.Helper()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	launcher := newFakeResumeLauncher()
	ai := &recordingArgv{}
	usage := &recordingArgv{}
	return &agentCommand{
		ai:           ai,
		usage:        usage,
		loadRegistry: store.store().load,
		rebind:       newAgentRebinder(create, launcher),
	}, launcher, ai, usage
}

// splitWindowCalls returns every tmux split-window argv a run issued.
//
// A split-window is the only way this process starts a provider conversation, so
// counting them is how "a failed resume started no conversation" is measured as a
// number rather than as an error string.
func splitWindowCalls(tmux *fakeTmux) [][]string {
	var out [][]string
	for _, call := range tmux.calls {
		if len(call) > 0 && call[0] == "split-window" {
			out = append(out, call)
		}
	}
	return out
}

// assertOnlyResumeLaunches pins both halves of the launch contract: how many
// conversations this run started, and that every one of them was the stored
// conversation rather than a fresh one.
func assertOnlyResumeLaunches(t *testing.T, tmux *fakeTmux, conversationID string, want int) {
	t.Helper()
	calls := splitWindowCalls(tmux)
	if len(calls) != want {
		t.Fatalf("the run issued %d split-window calls, want %d: %v", len(calls), want, calls)
	}
	for _, call := range calls {
		if !slices.ContainsFunc(call, func(arg string) bool { return strings.Contains(arg, conversationID) }) {
			t.Fatalf("a launched pane did not address the stored conversation %q: %v", conversationID, call)
		}
	}
}

// addFixtureAgent appends one more Agent to the fixture registry.
func addFixtureAgent(t *testing.T, store *fakeResourceStore, uid, name, windowUID string, phase coremetadata.AgentPhase, ref *coremetadata.AgentSessionRef) {
	t.Helper()
	store.registry.Agents = append(store.registry.Agents, coremetadata.Agent{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{
			UID: uid, Name: name,
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID},
			CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.AgentSpec{Provider: "codex"},
		Status: coremetadata.AgentStatus{
			Phase: phase, LastTransitionAt: resourceFixtureClock, SessionRef: ref,
		},
	})
	store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
		Scope: windowUID, Kind: coremetadata.KindAgent, Name: name, UID: uid,
	})
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fixture registry invalid after adding agent %q: %v", uid, err)
	}
}

// addFixtureWindow appends one more Window to the fixture registry, optionally
// with an initial Pane that becomes its primaryPaneRef.
func addFixtureWindow(t *testing.T, store *fakeResourceStore, projectUID, windowUID, name, paneUID string) {
	t.Helper()
	window := coremetadata.Window{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{
			UID: windowUID, Name: name,
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: projectUID},
			CreatedAt: resourceFixtureClock,
		},
	}
	if paneUID != "" {
		window.Spec.PrimaryPaneRef = paneUID
		store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion,
			Kind:       coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{
				UID: paneUID, Name: "zsh",
				OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID},
				CreatedAt: resourceFixtureClock,
			},
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		})
		store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
			Scope: windowUID, Kind: coremetadata.KindPane, Name: "zsh", UID: paneUID,
		})
	}
	store.registry.Windows = append(store.registry.Windows, window)
	store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
		Scope: projectUID, Kind: coremetadata.KindWindow, Name: name, UID: windowUID,
	})
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fixture registry invalid after adding window %q: %v", windowUID, err)
	}
}

// TestAgentResumeRebindsTheExistingAgentToANewManagedPane is acceptance
// criterion 1.
//
// The measurement is deliberately about identity rather than about success: the
// Agent count does not change, the uid and metadata.name are the ones that were
// already there, and the argv the split actually received addresses the stored
// conversation. A route that had quietly fallen through to a create would fail
// every one of those, not just the last.
func TestAgentResumeRebindsTheExistingAgentToANewManagedPane(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	before, _ := store.registry.Agent("agt-beta-codex")
	beforeUID, beforeName := before.Metadata.UID, before.Metadata.Name
	beforeAgentCount := len(store.registry.Agents)

	tmux := newFakeTmux()
	agent, launcher, ai, usage := newTestAgentResumeCommand(t, store, tmux)

	stdout, stderr, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
	if err != nil {
		t.Fatalf("agent resume error = %v", err)
	}
	if stdout != "agent/codex resumed\n" {
		t.Fatalf("stdout = %q, want the resumed result line", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want none for a conversation no other Agent shares", stderr)
	}
	if len(ai.calls) != 0 || len(usage.calls) != 0 {
		t.Fatalf("resume forwarded to ai=%q usage=%q, want neither", ai.calls, usage.calls)
	}

	// Identity is preserved. This is the half `create agent` can never satisfy.
	if got := len(store.registry.Agents); got != beforeAgentCount {
		t.Fatalf("agent count = %d, want %d: resume must never mint an Agent", got, beforeAgentCount)
	}
	after, ok := store.registry.Agent(beforeUID)
	if !ok {
		t.Fatalf("agent %q disappeared", beforeUID)
	}
	if after.Metadata.UID != beforeUID || after.Metadata.Name != beforeName {
		t.Fatalf("identity changed: uid %q->%q name %q->%q",
			beforeUID, after.Metadata.UID, beforeName, after.Metadata.Name)
	}
	if after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("phase = %q, want Running", after.Status.Phase)
	}
	if after.Status.PaneRef == "" {
		t.Fatal("status.paneRef is empty, so nothing was rebound")
	}
	pane, ok := store.registry.Pane(after.Status.PaneRef)
	if !ok {
		t.Fatalf("status.paneRef %q resolves to no Pane", after.Status.PaneRef)
	}
	if pane.Metadata.OwnerUID() != beforeUID || pane.Spec.Role != coremetadata.PaneRoleAgent {
		t.Fatalf("the new Pane is not this Agent's managed Pane: %#v", pane.Metadata)
	}
	// The durable conversation pointer is consumed, never rewritten, by resume.
	if !after.Status.SessionRef.SameConversation(resumeFixtureRef(resourceFixtureClock)) {
		t.Fatalf("resume changed the stored session ref: %#v", after.Status.SessionRef)
	}

	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("transactions=%d writes=%d, want 1/1", store.transactions, store.writes)
	}
	// Exactly one conversation was started, and it was the stored one.
	assertOnlyResumeLaunches(t, tmux, resumeFixtureConversation, 1)
	if len(launcher.plans) != 1 {
		t.Fatalf("the resume launch was constructed %d times, want 1", len(launcher.plans))
	}
	if got := (launcher.plans[0]); got.provider != "codex" || got.conversationID != resumeFixtureConversation || got.contextDir != "/srv/beta" {
		t.Fatalf("resume launch request = %+v, want the stored codex conversation rooted at /srv/beta", got)
	}
	if len(launcher.bound) != 1 || launcher.bound[0].conversationID != resumeFixtureConversation {
		t.Fatalf("managed-pane bindings = %+v, want one seeded with the stored conversation", launcher.bound)
	}
	if launcher.bound[0].paneID == "" || launcher.bound[0].title != "codex:resume" {
		t.Fatalf("managed-pane binding = %+v, want the resumed pane id and the resume title", launcher.bound[0])
	}
	// The Settings enabled-agents gate is the same one `create agent` runs, and
	// it runs exactly once, before the launch is constructed.
	if !reflect.DeepEqual(launcher.gated, []string{"codex"}) {
		t.Fatalf("enabled-agents gate calls = %v, want exactly one for codex", launcher.gated)
	}
	// Resume is detached like create: it rebinds a pane, it does not move the
	// operator's view onto it.
	assertNoClientMovement(t, tmux)
}

// TestAgentResumeFailuresStartNoConversationAtAll is acceptance criterion 2 and
// the single most important measurement in this Phase.
//
// Every row is a different reason a rebind cannot happen. What is asserted is
// not the wording but the count: zero new Agents, zero registry writes, and zero
// split-window calls, which together are "no conversation was started" -- neither
// a resumed one nor, crucially, a fresh one. A route that fell back to `create`
// on any of these rows would show a nonzero split count here.
func TestAgentResumeFailuresStartNoConversationAtAll(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// prepare mutates the fixture into the failing shape.
		prepare func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher)
		args    []string
		// wantUsage is true when the failure classifies as invalid input (exit 2)
		// rather than as a state or runtime failure (exit 1).
		wantUsage bool
		want      string
	}{
		{
			name:    "an Agent whose provider hook never ran has nothing to resume",
			prepare: func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:    []string{"resume", "codex", "--project", "beta"},
			want:    "has no provider session ref",
		},
		{
			name:    "and it names create rather than performing it",
			prepare: func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:    []string{"resume", "codex", "--project", "beta"},
			want:    "projmux create agent --provider <provider>",
		},
		{
			name: "a stored conversation the provider cannot revive fails loudly",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.planErr = errors.New("invalid codex resume id: contains control character")
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "cannot resume codex conversation " + resumeFixtureConversation,
		},
		{
			name: "a missing provider binary fails before the store is opened",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.planErr = errors.New("selected runner is not installed: codex")
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "selected runner is not installed",
		},
		{
			name: "a provider switched off in Settings is not resumable either",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.disabled["codex"] = true
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "is disabled in Settings",
		},
		{
			name: "a Running Agent is refused exactly as before",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-alpha-codex", resumeFixtureRef(resourceFixtureClock))
			},
			args:      []string{"resume", "codex", "--project", "alpha"},
			wantUsage: true,
			want:      "already owns a managed Pane",
		},
		{
			name: "an Agent that still owns a Pane is refused rather than given a second one",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-alpha-codex", resumeFixtureRef(resourceFixtureClock))
				agent, _ := store.registry.Agent("agt-alpha-codex")
				agent.Status.Phase = coremetadata.PhaseFailed
			},
			args: []string{"resume", "codex", "--project", "alpha"},
			want: "refusing to bind a second managed Pane",
		},
		{
			name: "a ref that contradicts the Agent's declared provider is refused",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				agent, _ := store.registry.Agent("agt-beta-codex")
				agent.Spec.Provider = "claude"
				agent.Status.SessionRef = resumeFixtureRef(resourceFixtureClock)
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "refusing to resume a mismatched conversation",
		},
		{
			name: "a Project whose root disappeared is refused before any tmux call",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				addFixtureWindow(t, store, "prj-gone", "win-gone-main", "main", "pan-gone-zsh")
				addFixtureAgent(t, store, "agt-gone-codex", "codex", "win-gone-main",
					coremetadata.PhaseOffline, resumeFixtureRef(resourceFixtureClock))
			},
			args:      []string{"resume", "uid:agt-gone-codex"},
			wantUsage: true,
			want:      "carries a MissingRoot condition",
		},
		{
			name: "a Window with no anchor Pane has nothing to split",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				addFixtureWindow(t, store, "prj-beta", "win-beta-empty", "empty", "")
				addFixtureAgent(t, store, "agt-empty-codex", "codex", "win-beta-empty",
					coremetadata.PhaseOffline, resumeFixtureRef(resourceFixtureClock))
			},
			args:      []string{"resume", "uid:agt-empty-codex"},
			wantUsage: true,
			want:      "has no spec.primaryPaneRef",
		},
		{
			name:      "an ambiguous reference is still refused rather than guessed",
			prepare:   func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:      []string{"resume", "codex"},
			wantUsage: true,
			want:      "want exactly one",
		},
		{
			name:      "a no-match is still refused",
			prepare:   func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:      []string{"resume", "nosuch"},
			wantUsage: true,
			want:      "matched no agents",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			agent, launcher, ai, usage := newTestAgentResumeCommand(t, store, tmux)
			test.prepare(t, store, launcher)
			before := store.snapshot()
			beforeAgents := len(store.registry.Agents)
			beforeTmux := tmux.state()

			stdout, _, err := runRoute(t, agent, test.args...)
			if err == nil {
				t.Fatalf("agent %v succeeded, want a refusal", test.args)
			}
			if got := IsUsageError(err); got != test.wantUsage {
				t.Fatalf("agent %v usage-error = %v, want %v (err = %v)", test.args, got, test.wantUsage, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("agent %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("agent %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}

			// The counted properties. Each is the same claim from a different
			// layer, and all three must read zero.
			if got := len(store.registry.Agents) - beforeAgents; got != 0 {
				t.Fatalf("a failed resume created %d Agents, want 0", got)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("a failed resume opened %d transactions and committed %d writes, want 0/0",
					store.transactions, store.writes)
			}
			if got := len(splitWindowCalls(tmux)); got != 0 {
				t.Fatalf("a failed resume started %d conversations, want 0: %v", got, splitWindowCalls(tmux))
			}
			if len(tmux.calls) != 0 {
				t.Fatalf("a failed resume issued %d tmux calls, want 0: %v", len(tmux.calls), tmux.calls)
			}
			if len(launcher.bound) != 0 {
				t.Fatalf("a failed resume bound %d managed panes, want 0", len(launcher.bound))
			}
			if store.snapshot() != before {
				t.Fatalf("a failed resume mutated the registry:\n--- before ---\n%s\n--- after ---\n%s", before, store.snapshot())
			}
			if tmux.state() != beforeTmux {
				t.Fatalf("a failed resume mutated the tmux server")
			}
			if len(ai.calls) != 0 || len(usage.calls) != 0 {
				t.Fatalf("a failed resume forwarded to ai=%q usage=%q, want neither", ai.calls, usage.calls)
			}
		})
	}
}

// TestAResumeRuntimeFailureRollsBackAndStartsNoFreshConversation is the second
// half of acceptance criterion 2: the failure that happens *after* the store is
// open.
//
// The tmux split is made to fail, which is the one class of failure that can
// reach a half-applied state. What must hold is that the transaction commits
// nothing, the Agent is still Offline with no Pane, and the only launch that was
// ever attempted addressed the stored conversation -- there is no second,
// fresh-start attempt anywhere on the path.
func TestAResumeRuntimeFailureRollsBackAndStartsNoFreshConversation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	beforeAgents := len(store.registry.Agents)
	beforePanes := len(store.registry.Panes)

	tmux := newFakeTmux()
	tmux.fail = []string{"split-window"}
	tmux.failMessage = "no space for new pane"
	agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)

	stdout, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
	if err == nil {
		t.Fatal("agent resume succeeded despite a failing split")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.writes != 0 {
		t.Fatalf("a rolled-back resume committed %d writes, want 0", store.writes)
	}
	if got := len(store.registry.Agents); got != beforeAgents {
		t.Fatalf("agent count = %d, want %d", got, beforeAgents)
	}
	if got := len(store.registry.Panes); got != beforePanes {
		t.Fatalf("pane count = %d, want %d", got, beforePanes)
	}
	after, _ := store.registry.Agent("agt-beta-codex")
	if after.Status.Phase != coremetadata.PhaseOffline || after.Status.PaneRef != "" {
		t.Fatalf("the Agent did not stay Offline: phase=%q paneRef=%q", after.Status.Phase, after.Status.PaneRef)
	}
	if !after.Status.SessionRef.SameConversation(resumeFixtureRef(resourceFixtureClock)) {
		t.Fatal("a failed resume disturbed the stored conversation pointer")
	}
	// Exactly one launch was attempted and it was the resume. No fallback ran.
	assertOnlyResumeLaunches(t, tmux, resumeFixtureConversation, 1)
	if len(launcher.bound) != 0 {
		t.Fatalf("a failed split still bound %d managed panes, want 0", len(launcher.bound))
	}
}

// TestAgentResumeNeverBuildsAFreshStartLaunch is the structural half of the
// no-fallback contract.
//
// The route holds an agentResumeLauncher, whose only launch method takes a
// conversation id. It deliberately does not hold the agentLauncher `create agent`
// uses, so there is no fresh-start argv it could build even by mistake. This
// pins that by reflection, which is what stops a future edit from re-adding the
// create seam to the resume path and reintroducing the silent fallback.
func TestAgentResumeNeverBuildsAFreshStartLaunch(t *testing.T) {
	t.Parallel()

	resume := reflect.TypeFor[agentResumeLauncher]()
	create := reflect.TypeFor[agentLauncher]()
	if resume.Implements(create) {
		t.Fatal("the resume launch seam also satisfies the create launch seam; a fresh-start argv is reachable from the resume path")
	}
	if _, ok := resume.MethodByName("PlanAgentLaunch"); ok {
		t.Fatal("the resume launch seam exposes the fresh-start launch builder")
	}
	method, ok := resume.MethodByName("PlanAgentResume")
	if !ok {
		t.Fatal("the resume launch seam has no resume launch builder")
	}
	// (provider, contextDir, conversationID): the conversation is a required
	// input, so an argv cannot be built without one.
	if got := method.Type.NumIn(); got != 3 {
		t.Fatalf("PlanAgentResume takes %d inputs, want 3 including the conversation id", got)
	}

	rebinder := reflect.TypeFor[agentRebinder]()
	for i := range rebinder.NumField() {
		if rebinder.Field(i).Type == create {
			t.Fatal("the rebinder holds the create launch seam")
		}
	}
}

// TestSeveralAgentsOnOneConversationResolveDeterministically is acceptance
// criterion 5.
//
// Phase 0 declared several Agents pointing at one conversation to be legal state
// and left the tie-break to this Phase. The rule this pins is that the
// conversation is never a selector: the reference decides, duplicates are
// disclosed in uid order and never redirect or block the rebind. The registry
// order is permuted so a rule that accidentally depended on it would fail.
func TestSeveralAgentsOnOneConversationResolveDeterministically(t *testing.T) {
	t.Parallel()

	shared := func() *coremetadata.AgentSessionRef { return resumeFixtureRef(resourceFixtureClock) }

	var firstStderr string
	var firstArgv []string
	for permutation := range 3 {
		store := newFakeResourceStore(t)
		// Three Agents record one conversation: the Running one in alpha, a
		// second Offline one in alpha/review, and the target in beta.
		setFixtureSessionRef(t, store, "agt-alpha-codex", shared())
		setFixtureSessionRef(t, store, "agt-beta-codex", shared())
		addFixtureAgent(t, store, "agt-review-codex", "codex", "win-alpha-review", coremetadata.PhaseOffline, shared())

		// Rotate the registry order. A conversation-keyed selection, or an
		// unsorted disclosure, would move with it.
		for range permutation {
			store.registry.Agents = append(store.registry.Agents[1:], store.registry.Agents[0])
		}

		tmux := newFakeTmux()
		agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
		stdout, stderr, err := runRoute(t, agent, "resume", "uid:agt-beta-codex")
		if err != nil {
			t.Fatalf("permutation %d: agent resume error = %v", permutation, err)
		}
		if stdout != "agent/codex resumed\n" {
			t.Fatalf("permutation %d: stdout = %q", permutation, stdout)
		}
		// The referenced Agent is the one that got the Pane, every time.
		target, _ := store.registry.Agent("agt-beta-codex")
		if target.Status.Phase != coremetadata.PhaseRunning || target.Status.PaneRef == "" {
			t.Fatalf("permutation %d: the referenced Agent was not the one rebound: %+v", permutation, target.Status)
		}
		// And none of the conversation's other Agents moved.
		for _, uid := range []string{"agt-alpha-codex", "agt-review-codex"} {
			other, _ := store.registry.Agent(uid)
			if uid == "agt-review-codex" && other.Status.Phase != coremetadata.PhaseOffline {
				t.Fatalf("permutation %d: a conversation sibling changed phase: %+v", permutation, other.Status)
			}
			if !other.Status.SessionRef.SameConversation(shared()) {
				t.Fatalf("permutation %d: resume rewrote a sibling's conversation pointer", permutation)
			}
		}
		if len(launcher.plans) != 1 {
			t.Fatalf("permutation %d: %d launches, want 1", permutation, len(launcher.plans))
		}
		argv := splitWindowCalls(tmux)[0]

		if permutation == 0 {
			firstStderr, firstArgv = stderr, argv
			// The disclosure names both siblings, in uid order, and says which
			// Agent was actually rebound.
			for _, want := range []string{
				"agent/codex (uid:agt-alpha-codex)",
				"agent/codex (uid:agt-review-codex)",
				"rebinds only agent/codex",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("disclosure = %q, want it to mention %q", stderr, want)
				}
			}
			if got := strings.Count(stderr, "\n"); got != 2 {
				t.Fatalf("disclosure has %d lines, want one per sibling: %q", got, stderr)
			}
			if strings.Index(stderr, "agt-alpha-codex") > strings.Index(stderr, "agt-review-codex") {
				t.Fatalf("disclosure is not in uid order: %q", stderr)
			}
			continue
		}
		if stderr != firstStderr {
			t.Fatalf("permutation %d disclosure diverged:\n%q\nwant\n%q", permutation, stderr, firstStderr)
		}
		if !reflect.DeepEqual(argv, firstArgv) {
			t.Fatalf("permutation %d launched a different argv:\n%v\nwant\n%v", permutation, argv, firstArgv)
		}
	}
}

// TestObservedAtIsNotAResumeGate records the adjudication of the second Phase 0
// handoff item.
//
// `observedAt` is when projmux last saw the conversation, not a provider
// timestamp, so it cannot answer "is this ref stale". The route therefore never
// reads it, and the proof is that two refs identical except for an observedAt a
// decade apart produce byte-identical outcomes -- including the far-future one,
// which any wall-clock heuristic would have to treat differently from the
// ancient one.
func TestObservedAtIsNotAResumeGate(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, observedAt time.Time) (string, string, []string) {
		t.Helper()
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(observedAt))
		tmux := newFakeTmux()
		agent, _, _, _ := newTestAgentResumeCommand(t, store, tmux)
		stdout, stderr, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
		if err != nil {
			t.Fatalf("observedAt %s: agent resume error = %v", observedAt, err)
		}
		after, _ := store.registry.Agent("agt-beta-codex")
		if after.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("observedAt %s: phase = %q, want Running", observedAt, after.Status.Phase)
		}
		return stdout, stderr, splitWindowCalls(tmux)[0]
	}

	ancient := resourceFixtureClock.AddDate(-10, 0, 0)
	future := resourceFixtureClock.AddDate(10, 0, 0)
	wantOut, wantErr, wantArgv := run(t, ancient)
	gotOut, gotErr, gotArgv := run(t, future)
	if gotOut != wantOut || gotErr != wantErr {
		t.Fatalf("observedAt changed the streams: %q/%q vs %q/%q", gotOut, gotErr, wantOut, wantErr)
	}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("observedAt changed the launched argv:\n%v\nvs\n%v", gotArgv, wantArgv)
	}
	// And the value itself survives the rebind untouched: resume consumes the
	// pointer, it does not re-observe it.
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(ancient))
	agent, _, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta"); err != nil {
		t.Fatalf("agent resume error = %v", err)
	}
	after, _ := store.registry.Agent("agt-beta-codex")
	if !after.Status.SessionRef.ObservedAt.Equal(ancient) {
		t.Fatalf("observedAt = %s, want the stored %s: resume must not rewrite the observation",
			after.Status.SessionRef.ObservedAt, ancient)
	}
}

// TestProviderResumeArgvAddressesTheConversationAndCarriesNoTurnID records the
// adjudication of the third Phase 0 handoff item.
//
// Codex reports a turn id and Phase 0 deliberately did not store it. This Phase
// does not need it: `codex resume <thread-id>` has no turn slot at all, so
// resume is a conversation-granularity operation for every provider, uniformly.
// Turn-level resume would need a provider CLI that accepts a turn on resume, a
// durable home for a value that changes every turn, and a product decision about
// what rewinding means -- none of which is this Phase's, and none of which is
// implemented here. The argv shapes are pinned so a future turn slot cannot
// appear unnoticed.
func TestProviderResumeArgvAddressesTheConversationAndCarriesNoTurnID(t *testing.T) {
	t.Parallel()

	const uuid = "7ceaf499-728a-482e-97cd-1c0420efc7e2"
	for _, test := range []struct {
		provider string
		id       string
		want     []string
	}{
		{provider: aiModeClaude, id: uuid, want: []string{"claude", "--resume", uuid}},
		{provider: aiModeCodex, id: "thr-1", want: []string{"codex", "resume", "thr-1"}},
		{provider: aiModeAntigravity, id: uuid, want: []string{"agy", "--conversation", uuid}},
	} {
		t.Run(test.provider, func(t *testing.T) {
			t.Parallel()
			got, err := resumeArgsForAgent(test.provider, test.id)
			if err != nil {
				t.Fatalf("resumeArgsForAgent(%s) error = %v", test.provider, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resume argv = %v, want %v", got, test.want)
			}
			// Three elements exactly: binary, verb/flag, conversation. A turn id
			// would have to be a fourth, and there is nowhere to put it.
			if len(got) != 3 {
				t.Fatalf("resume argv has %d elements, want 3 with no turn slot: %v", len(got), got)
			}
		})
	}

	// An unusable conversation id is an error rather than a fresh start. These
	// are the failures the app layer surfaces as "cannot resume <conversation>".
	for _, test := range []struct {
		name     string
		provider string
		id       string
	}{
		{name: "empty claude id", provider: aiModeClaude, id: "  "},
		{name: "control character in a codex id", provider: aiModeCodex, id: "thr\x00"},
		{name: "an antigravity id that is not a conversation uuid", provider: aiModeAntigravity, id: "thr-1"},
		{name: "an unknown provider", provider: "gpt", id: uuid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if argv, err := resumeArgsForAgent(test.provider, test.id); err == nil {
				t.Fatalf("resumeArgsForAgent(%s, %q) = %v, want an error", test.provider, test.id, argv)
			}
		})
	}

	// The sentinel identity is what the app layer relies on to classify these.
	for _, err := range []error{claude.ErrInvalidResumeID, codex.ErrInvalidResumeID, antigravity.ErrInvalidResumeID} {
		if err == nil {
			t.Fatal("a provider lost its invalid-resume-id sentinel")
		}
	}
}

// TestPlanAgentResumeReadsOnlyWhatTheHookAlreadyGaveUs is the permanent-exclusion
// guard.
//
// Reading a provider's own configuration or transcript store is out of scope for
// good. A Claude ref carries a transcript *path*; the plan must ignore it
// entirely, so a run whose transcript path points at a file that does not exist,
// or at nothing at all, behaves identically.
func TestPlanAgentResumeReadsOnlyWhatTheHookAlreadyGaveUs(t *testing.T) {
	t.Parallel()

	plan := func(t *testing.T, transcript string) agentResumePlan {
		t.Helper()
		store := newFakeResourceStore(t)
		agent, _ := store.registry.Agent("agt-beta-codex")
		agent.Spec.Provider = "claude"
		agent.Status.SessionRef = &coremetadata.AgentSessionRef{
			Provider:   "claude",
			ObservedAt: resourceFixtureClock,
			Claude: &coremetadata.ClaudeSessionRef{
				SessionID:      "sess-1",
				TranscriptPath: transcript,
			},
		}
		got, err := planAgentResume("agent resume", store.registry, agent)
		if err != nil {
			t.Fatalf("planAgentResume error = %v", err)
		}
		return got
	}

	absent := plan(t, filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	none := plan(t, "")
	if absent.conversationID != "sess-1" || none.conversationID != "sess-1" {
		t.Fatalf("the conversation id depends on the transcript path: %q vs %q", absent.conversationID, none.conversationID)
	}
	if absent.provider != "claude" || none.provider != "claude" {
		t.Fatalf("provider = %q/%q, want claude", absent.provider, none.provider)
	}
	if !reflect.DeepEqual(absent.shared, none.shared) {
		t.Fatalf("the transcript path reached the tie-break: %v vs %v", absent.shared, none.shared)
	}
}

// TestResumeReusesTheCreateRuntimeRatherThanASecondMaterializer pins that the
// rebind rides the create command's own transaction, ledger, and detached
// materializer. A second implementation of any of those would be a second set of
// rollback bugs, and it would also be a second place a fresh-start launch could
// appear.
func TestResumeReusesTheCreateRuntimeRatherThanASecondMaterializer(t *testing.T) {
	t.Parallel()

	app := New()
	if app.agent == nil || app.agent.rebind == nil {
		t.Fatal("the application graph does not wire the resume rebinder")
	}
	if app.agent.rebind.create != app.create {
		t.Fatal("agent resume does not share the create command's runtime")
	}
	if app.agent.rebind.launcher != agentResumeLauncher(app.ai) {
		t.Fatal("agent resume does not share the AI command's provider seam")
	}
	// The materializer is the object that owns `-d`, so sharing it is what makes
	// "resume never moves the client" the same guarantee create already has.
	if app.create.runtime == nil || app.create.runtime.runner == nil || app.create.runtime.sessions == nil {
		t.Fatal("the shared runtime is not fully wired")
	}
}

// TestTheRealResumeSeamRefusesAnUnusableConversationBeforeTouchingTheProvider
// covers the production seam rather than the fake.
//
// Both rows fail inside the provider's own resume-argv builder, which runs
// before anything looks for a binary on disk, so the assertion is filesystem
// independent. What it pins is that the seam has no path that turns an unusable
// conversation id into a launch of any kind.
func TestTheRealResumeSeamRefusesAnUnusableConversationBeforeTouchingTheProvider(t *testing.T) {
	t.Parallel()

	c := &aiCommand{}
	for _, test := range []struct {
		name           string
		provider       string
		conversationID string
	}{
		{name: "a malformed codex conversation id", provider: aiModeCodex, conversationID: "thr\x00"},
		{name: "an empty claude conversation id", provider: aiModeClaude, conversationID: "   "},
		{name: "a provider that is not an agent provider", provider: "gpt", conversationID: "whatever"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			title, argv, err := c.PlanAgentResume(test.provider, "/srv/beta", test.conversationID)
			if err == nil {
				t.Fatalf("PlanAgentResume returned title=%q argv=%v, want an error", title, argv)
			}
			if title != "" || argv != nil {
				t.Fatalf("a failed resume plan still produced title=%q argv=%v", title, argv)
			}
		})
	}
}
