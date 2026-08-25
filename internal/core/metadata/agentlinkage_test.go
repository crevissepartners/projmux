package metadata

import (
	"errors"
	"reflect"
	"testing"
)

// agentLinkageFixture registers one Project and returns the Window an orphan
// pane is bound inside, matching the shape the binding-repair walk hands to
// LinkAgentPane.
func agentLinkageFixture(t *testing.T) (Mutator, *Registry, string) {
	t.Helper()

	root := "/tmp/alpha"
	mutator := testMutator(dirSet{root: true})
	registry := NewRegistry()
	result, err := registerFixture(mutator, &registry, root)
	if err != nil {
		t.Fatalf("register project fixture: %v", err)
	}
	windows := registry.WindowsOf(result.Project.Metadata.UID)
	if len(windows) != 1 {
		t.Fatalf("fixture Project owns %d Windows, want 1", len(windows))
	}
	return mutator, &registry, windows[0].Metadata.UID
}

// importedShellPane reproduces what the orphan-pane import leaves behind: a
// Window-owned shell Pane for a live tmux pane, whatever that pane is running.
func importedShellPane(t *testing.T, mutator Mutator, registry *Registry, windowUID string, observed LegacyPane) Pane {
	t.Helper()
	pane, err := mutator.ImportOrphanPane(registry, windowUID, observed, "op-1")
	if err != nil {
		t.Fatalf("import orphan pane: %v", err)
	}
	return pane
}

// claudePane is a live pane the AI routes launched: it carries the authorship
// marker and the provider conversation id.
func claudePane(sessionID string) LegacyPane {
	return LegacyPane{
		Provider:         "claude",
		LaunchAuthorship: "1",
		Topic:            "roadmap",
		Command:          "claude",
		SessionID:        sessionID,
	}
}

func TestResolveAgentPaneAuthorityClosedTable(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		observed LegacyPane
		want     AgentPaneAuthority
	}{
		{name: "no marker", observed: LegacyPane{Command: "codex", Title: "codex"}, want: AgentPaneAuthorityNoMarker},
		{name: "hook only", observed: LegacyPane{Provider: "codex"}, want: AgentPaneAuthorityHookOnly},
		{name: "canonical launch", observed: LegacyPane{Provider: "codex", LaunchAuthorship: "1"}, want: AgentPaneAuthorityLaunch},
		{name: "launch without provider", observed: LegacyPane{LaunchAuthorship: "1"}, want: AgentPaneAuthorityAmbiguous},
		{name: "unknown launch receipt", observed: LegacyPane{Provider: "codex", LaunchAuthorship: "yes"}, want: AgentPaneAuthorityAmbiguous},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveAgentPaneAuthority(tt.observed); got != tt.want {
				t.Fatalf("authority = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLinkAgentPaneLaunchAuthorityConflictsAreRecordedAndZeroWrite(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		observed LegacyPane
		want     string
	}{
		{
			name: "ambiguous launch receipt", observed: LegacyPane{Provider: "codex", LaunchAuthorship: "yes"},
			want: "launch authorship marker and provider do not form one canonical receipt",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mutator, registry, windowUID := agentLinkageFixture(t)
			window, _ := registry.Window(windowUID)
			pane, _ := registry.Pane(window.Spec.AnchorPaneRef)
			window.Spec.DefaultShellPaneRef = ""
			before := registry.Clone()
			if got := AgentPanePromotionRefusal(registry, windowUID, pane.Metadata.UID, tt.observed); got != tt.want {
				t.Fatalf("refusal = %q, want %q", got, tt.want)
			}
			linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, tt.observed, nil, "op-conflict")
			if err != nil || linkage.Linked() || !reflect.DeepEqual(before, *registry) {
				t.Fatalf("conflict was not zero-write: linkage=%+v err=%v\nbefore=%+v\nafter=%+v", linkage, err, before, *registry)
			}
		})
	}
}

func TestLinkAgentPaneExistingOwnerAndProviderConflictsAreZeroWrite(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	paneUID := window.Spec.AnchorPaneRef
	window.Spec.DefaultShellPaneRef = ""
	linked, err := mutator.LinkAgentPane(registry, windowUID, paneUID, LegacyPane{
		Provider: "codex", LaunchAuthorship: "1",
	}, nil, "op-seed-owner")
	if err != nil || !linked.Linked() {
		t.Fatalf("seed exact Agent owner: linkage=%+v err=%v", linked, err)
	}

	t.Run("provider conflicts with exact owner", func(t *testing.T) {
		before := registry.Clone()
		observed := LegacyPane{Provider: "claude", LaunchAuthorship: "1"}
		if got := AgentPanePromotionRefusal(registry, windowUID, paneUID, observed); got != "launch provider conflicts with the exact Agent owner provider" {
			t.Fatalf("provider conflict refusal = %q", got)
		}
		linkage, err := mutator.LinkAgentPane(registry, windowUID, paneUID, observed, nil, "op-provider-conflict")
		if err != nil || linkage.Linked() || !reflect.DeepEqual(before, *registry) {
			t.Fatalf("provider conflict wrote state: linkage=%+v err=%v", linkage, err)
		}
	})

	t.Run("Agent belongs to another Window", func(t *testing.T) {
		pane, _ := registry.Pane(paneUID)
		agent, _ := registry.Agent(linked.AgentUID)
		agent.Metadata.OwnerRef = &OwnerRef{Kind: KindWindow, UID: "win-other"}
		before := registry.Clone()
		observed := LegacyPane{Provider: "codex", LaunchAuthorship: "1"}
		if got := AgentPanePromotionRefusal(registry, windowUID, pane.Metadata.UID, observed); got != "launch authorship conflicts with the exact Agent/Pane owner chain" {
			t.Fatalf("owner conflict refusal = %q", got)
		}
		linkage, err := mutator.LinkAgentPane(registry, windowUID, paneUID, observed, nil, "op-owner-conflict")
		if err != nil || linkage.Linked() || !reflect.DeepEqual(before, *registry) {
			t.Fatalf("owner conflict wrote state: linkage=%+v err=%v", linkage, err)
		}
	})
}

func TestLinkAgentPaneMalformedAuthorityRefusesBeforeAlreadyOwnedRebind(t *testing.T) {
	t.Parallel()
	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	paneUID := window.Spec.AnchorPaneRef
	window.Spec.DefaultShellPaneRef = ""
	seeded, err := mutator.LinkAgentPane(registry, windowUID, paneUID, LegacyPane{
		Provider: "codex", LaunchAuthorship: "1",
	}, nil, "op-seed-malformed-owned")
	if err != nil || !seeded.Linked() {
		t.Fatalf("seed exact owner: linkage=%+v err=%v", seeded, err)
	}
	agent, _ := registry.Agent(seeded.AgentUID)
	agent.Status.PaneRef = ""
	before := registry.Clone()
	linkage, err := mutator.LinkAgentPane(registry, windowUID, paneUID, LegacyPane{
		Provider: "codex", LaunchAuthorship: "yes",
	}, NewBindingMatcher(RuntimeObservation{}), "op-malformed-owned")
	if err != nil {
		t.Fatal(err)
	}
	if linkage.Linked() || !reflect.DeepEqual(*registry, before) {
		t.Fatalf("malformed already-owned receipt rebound topology: linkage=%+v\nbefore=%+v\nafter=%+v", linkage, before, *registry)
	}
}

func TestLinkAgentPanePostStateValidationFailureRestoresExactPreimage(t *testing.T) {
	t.Parallel()
	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	paneUID := window.Spec.AnchorPaneRef
	window.Spec.DefaultShellPaneRef = ""
	mutator.NewUID = func(kind Kind) (string, error) {
		if kind == KindAgent {
			return paneUID, nil // deliberately violates global UID uniqueness after mint
		}
		return NewUID(kind)
	}
	before := registry.Clone()
	if _, err := mutator.LinkAgentPane(registry, windowUID, paneUID, LegacyPane{
		Provider: "codex", LaunchAuthorship: "1",
	}, nil, "op-invalid-post-state"); err == nil {
		t.Fatal("invalid composite post-state succeeded")
	}
	if !reflect.DeepEqual(*registry, before) {
		t.Fatalf("post-state validation failure leaked partial promotion:\nbefore=%+v\nafter=%+v", before, *registry)
	}
}

func TestLinkAgentPaneCanonicalLaunchClearsDefaultAndRetainsAnchor(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	paneUID := window.Spec.DefaultShellPaneRef
	if paneUID == "" || window.Spec.AnchorPaneRef != paneUID {
		t.Fatalf("fixture default/anchor = %q/%q, want the same exact Pane", paneUID, window.Spec.AnchorPaneRef)
	}
	linkage, err := mutator.LinkAgentPane(registry, windowUID, paneUID, LegacyPane{
		Provider: "codex", LaunchAuthorship: "1", SessionID: "session-1",
	}, NewBindingMatcher(RuntimeObservation{}), "op-canonical-launch")
	if err != nil {
		t.Fatalf("link canonical launch: %v", err)
	}
	if linkage.Kind != AgentLinkMinted || !linkage.Promoted {
		t.Fatalf("linkage = %+v, want one minted atomic promotion", linkage)
	}
	window, _ = registry.Window(windowUID)
	if window.Spec.AnchorPaneRef != paneUID || window.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("post Window refs = anchor %q default %q, want retained anchor and cleared default", window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef)
	}
	pane, _ := registry.Pane(paneUID)
	agent, _ := registry.Agent(linkage.AgentUID)
	if pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Spec.Role != PaneRoleAgent || agent.Status.PaneRef != paneUID {
		t.Fatalf("composite post-state pane=%+v agent=%+v", pane, agent)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("composite post-state is invalid: %v", err)
	}
}

// TestLinkAgentPaneMintsAnAgentAndPromotesThePaneWhenNoneExists is the import
// half of the phase: a running agent whose Window has no Agent resource at all
// is exactly the state that made `get agents` hide what was running.
func TestLinkAgentPaneMintsAnAgentAndPromotesThePaneWhenNoneExists(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	binder := NewBindingMatcher(RuntimeObservation{})

	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), binder, "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkMinted || !linkage.Promoted {
		t.Fatalf("linkage = %+v, want a minted Agent with a promoted Pane", linkage)
	}

	agent, ok := registry.Agent(linkage.AgentUID)
	if !ok {
		t.Fatal("the minted Agent is not in the registry")
	}
	if agent.Metadata.Name != "claude" {
		t.Fatalf("minted Agent name = %q, want the provider name base", agent.Metadata.Name)
	}
	if agent.Metadata.OwnerUID() != windowUID || agent.Metadata.OwnerRef.Kind != KindWindow {
		t.Fatalf("minted Agent owner = %+v, want the paired Window", agent.Metadata.OwnerRef)
	}
	if agent.Spec.Provider != "claude" {
		t.Fatalf("minted Agent provider = %q, want claude", agent.Spec.Provider)
	}
	if agent.Metadata.Annotations[AnnotationAgentTopic] != "roadmap" {
		t.Fatalf("the observed topic must land on the annotation, got %+v", agent.Metadata.Annotations)
	}

	// The linkage is expressed in both directions: ownerRef is the edge every
	// reader resolves through, paneRef is the reverse pointer describe renders.
	linked, _ := registry.Pane(pane.Metadata.UID)
	if linked.Metadata.OwnerRef.Kind != KindAgent || linked.Metadata.OwnerRef.UID != linkage.AgentUID {
		t.Fatalf("promoted Pane owner = %+v, want the minted Agent", linked.Metadata.OwnerRef)
	}
	if linked.Spec.Role != PaneRoleAgent {
		t.Fatalf("promoted Pane role = %q, want %q", linked.Spec.Role, PaneRoleAgent)
	}
	if agent.Status.PaneRef != pane.Metadata.UID || agent.Status.Phase != PhaseRunning {
		t.Fatalf("agent status = %+v, want Running with the promoted Pane", agent.Status)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after linkage: %v", err)
	}
}

func TestLinkAgentPaneRefusesCanonicalShellMarkerWithZeroWrites(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	pane, _ := registry.Pane(window.Spec.DefaultShellPaneRef)
	before := registry.Clone()
	observed := LegacyPane{Provider: "codex", Command: "codex"}

	reason := AgentPanePromotionRefusal(registry, windowUID, pane.Metadata.UID, observed)
	if reason != "runtime Agent marker cannot reparent the canonical Window default shell Pane" {
		t.Fatalf("canonical shell refusal = %q", reason)
	}
	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, observed, NewBindingMatcher(RuntimeObservation{}), "op-refuse")
	if err != nil {
		t.Fatalf("link canonical shell marker: %v", err)
	}
	if linkage.Kind != AgentLinkNone || linkage.Linked() || linkage.Promoted {
		t.Fatalf("canonical shell linkage = %+v, want zero-write refusal", linkage)
	}
	if !reflect.DeepEqual(before, *registry) {
		t.Fatalf("canonical shell refusal changed Registry:\nbefore=%+v\nafter=%+v", before, *registry)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("canonical shell refusal left invalid Registry: %v", err)
	}
}

func TestLinkAgentPanePreservesAnchorOnlyPromotionBehavior(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	window, _ := registry.Window(windowUID)
	pane, _ := registry.Pane(window.Spec.AnchorPaneRef)
	window.Spec.DefaultShellPaneRef = ""
	observed := claudePane("sess-anchor-only")

	if reason := AgentPanePromotionRefusal(registry, windowUID, pane.Metadata.UID, observed); reason != "" {
		t.Fatalf("anchor-only Pane was refused by Corrective A: %q", reason)
	}
	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, observed, NewBindingMatcher(RuntimeObservation{}), "op-anchor-only")
	if err != nil {
		t.Fatalf("link anchor-only Pane: %v", err)
	}
	if !linkage.Linked() || !linkage.Promoted {
		t.Fatalf("anchor-only linkage = %+v, want preserved promotion", linkage)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("anchor-only linkage left invalid Registry: %v", err)
	}
}

// TestLinkAgentPaneNeverActsOnAPaneWithNoAuthorshipMarker is Phase 1's refuse
// rule applied to this layer: `pane_current_command` is not evidence, and a
// pane the operator started an agent in by hand carries no projmux marker.
func TestLinkAgentPaneNeverActsOnAPaneWithNoAuthorshipMarker(t *testing.T) {
	t.Parallel()

	unmarked := []struct {
		name     string
		observed LegacyPane
	}{
		{name: "a shell", observed: LegacyPane{Command: "zsh"}},
		{name: "claude typed into a shell", observed: LegacyPane{Command: "claude", Title: "claude - working"}},
		{name: "a provider hook without launch authorship", observed: LegacyPane{Provider: "codex", Command: "codex"}},
		{name: "a title that mentions codex", observed: LegacyPane{Command: "zsh", Title: "codex refactor"}},
	}

	for _, tt := range unmarked {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mutator, registry, windowUID := agentLinkageFixture(t)
			pane := importedShellPane(t, mutator, registry, windowUID, tt.observed)
			before := registry.Clone()

			linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, tt.observed, NewBindingMatcher(RuntimeObservation{}), "op-1")
			if err != nil {
				t.Fatalf("link agent pane: %v", err)
			}
			if linkage.Kind != AgentLinkNone || linkage.Linked() {
				t.Fatalf("linkage = %+v, want none", linkage)
			}
			if !reflect.DeepEqual(*registry, before) {
				t.Fatal("a refused link still wrote to the registry")
			}
		})
	}
}

// TestLinkAgentPaneAttachesToTheAgentRecordingTheSameConversation is the
// adoption half: an Agent that already exists for this conversation is reused
// rather than duplicated, on an exact identifier equality both sides got from
// the provider.
func TestLinkAgentPaneAttachesToTheAgentRecordingTheSameConversation(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	existing, err := mutator.CreateAgent(registry, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-0"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(registry, existing.Metadata.UID, AgentSessionObservation{
		Provider:  "claude",
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("record session ref: %v", err)
	}
	// A second Agent for a different conversation must not be a candidate.
	other, err := mutator.CreateAgent(registry, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-0"})
	if err != nil {
		t.Fatalf("create second agent: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(registry, other.Metadata.UID, AgentSessionObservation{
		Provider:  "claude",
		SessionID: "sess-2",
	}); err != nil {
		t.Fatalf("record second session ref: %v", err)
	}

	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	agentsBefore := len(registry.Agents)

	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkAttached || linkage.AgentUID != existing.Metadata.UID {
		t.Fatalf("linkage = %+v, want an attach onto %q", linkage, existing.Metadata.UID)
	}
	if len(registry.Agents) != agentsBefore {
		t.Fatalf("attaching minted a %dth Agent, want %d", len(registry.Agents), agentsBefore)
	}
	attached, _ := registry.Agent(existing.Metadata.UID)
	if attached.Status.Phase != PhaseRunning || attached.Status.PaneRef != pane.Metadata.UID {
		t.Fatalf("attached agent status = %+v", attached.Status)
	}
	if untouched, _ := registry.Agent(other.Metadata.UID); untouched.Status.PaneRef != "" {
		t.Fatalf("the other conversation's Agent was written to: %+v", untouched.Status)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after attach: %v", err)
	}
}

// TestLinkAgentPaneMintsRatherThanGuessWhenTheConversationIsAmbiguous keeps the
// refuse rule intact where it actually bites. Two Agents may legally record one
// conversation, so "the first one" is a guess, and taking a binding that
// belongs to the other Agent is the mistake no later pass can undo. An extra
// Agent is inert and visible; a wrong attach is neither.
func TestLinkAgentPaneMintsRatherThanGuessWhenTheConversationIsAmbiguous(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	for range 2 {
		agent, err := mutator.CreateAgent(registry, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-0"})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		if _, _, err := mutator.RecordAgentSessionRef(registry, agent.Metadata.UID, AgentSessionObservation{
			Provider:  "claude",
			SessionID: "sess-1",
		}); err != nil {
			t.Fatalf("record session ref: %v", err)
		}
	}

	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkMinted {
		t.Fatalf("linkage = %+v, want a mint rather than a guess", linkage)
	}
	for _, agent := range registry.Agents {
		if agent.Metadata.UID != linkage.AgentUID && agent.Status.PaneRef != "" {
			t.Fatalf("an ambiguous candidate was written to: %+v", agent)
		}
	}
}

// TestLinkAgentPaneNeverEquatesTwoProvidersConversationIDs keeps the union
// discriminator load bearing: a Codex thread id and a Claude session id are
// different namespaces and an equal string across them is a coincidence.
func TestLinkAgentPaneNeverEquatesTwoProvidersConversationIDs(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	codex, err := mutator.CreateAgent(registry, windowUID, CreateAgentOptions{Provider: "codex", OperationID: "op-0"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(registry, codex.Metadata.UID, AgentSessionObservation{
		Provider: "codex",
		ThreadID: "shared-id",
	}); err != nil {
		t.Fatalf("record session ref: %v", err)
	}

	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("shared-id"))
	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("shared-id"), NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkMinted {
		t.Fatalf("linkage = %+v, want a mint: the ids belong to different providers", linkage)
	}
	if attached, _ := registry.Agent(codex.Metadata.UID); attached.Status.PaneRef != "" {
		t.Fatalf("a Codex Agent took a Claude pane: %+v", attached.Status)
	}
}

// TestLinkAgentPaneIsIdempotentAcrossPasses is the convergence property the
// reconciler depends on. It runs on every mutation route, so a link that minted
// a second Agent on every pass would turn a working machine into an Agent list
// that grows without bound.
func TestLinkAgentPaneIsIdempotentAcrossPasses(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))

	first, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("first link: %v", err)
	}
	after := registry.Clone()

	second, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-2")
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if second.Kind != AgentLinkRebound || second.AgentUID != first.AgentUID {
		t.Fatalf("second pass linkage = %+v, want a rebind onto %q", second, first.AgentUID)
	}
	if second.Promoted {
		t.Fatal("the second pass promoted an already-managed Pane")
	}
	if !reflect.DeepEqual(*registry, after) {
		t.Fatal("a converged link still rewrote the registry")
	}
}

// TestLinkAgentPaneGivesOneAgentToOnlyOnePaneInAPass reuses the pass-scoped
// claim set the Window and Pane walks already keep: one registry object is the
// runtime of at most one live tmux object.
func TestLinkAgentPaneGivesOneAgentToOnlyOnePaneInAPass(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	existing, err := mutator.CreateAgent(registry, windowUID, CreateAgentOptions{Provider: "claude", OperationID: "op-0"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(registry, existing.Metadata.UID, AgentSessionObservation{
		Provider:  "claude",
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("record session ref: %v", err)
	}

	// Two live panes reporting the same conversation. Only one of them can be
	// the Agent's runtime.
	firstPane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	secondPane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	binder := NewBindingMatcher(RuntimeObservation{})

	first, err := mutator.LinkAgentPane(registry, windowUID, firstPane.Metadata.UID, claudePane("sess-1"), binder, "op-1")
	if err != nil {
		t.Fatalf("first link: %v", err)
	}
	second, err := mutator.LinkAgentPane(registry, windowUID, secondPane.Metadata.UID, claudePane("sess-1"), binder, "op-1")
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if first.AgentUID != existing.Metadata.UID || first.Kind != AgentLinkAttached {
		t.Fatalf("first linkage = %+v, want an attach onto the existing Agent", first)
	}
	if second.AgentUID == first.AgentUID {
		t.Fatalf("two live panes claimed one Agent %q", first.AgentUID)
	}
	if second.Kind != AgentLinkMinted {
		t.Fatalf("second linkage = %+v, want a mint", second)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate: %v", err)
	}
}

// TestLinkAgentPaneNeverAttachesAcrossWindows keeps the scope structural: the
// candidate set is the paired Window's Agents and nothing else, so an Agent can
// never be pulled across a Window -- or therefore a Project -- boundary.
func TestLinkAgentPaneNeverAttachesAcrossWindows(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/tmp/alpha": true, "/tmp/beta": true})
	reg := NewRegistry()
	registry := &reg
	alpha, err := registerFixture(mutator, registry, "/tmp/alpha")
	if err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	other, err := registerFixture(mutator, registry, "/tmp/beta")
	if err != nil {
		t.Fatalf("register beta: %v", err)
	}
	windowUID := registry.WindowsOf(alpha.Project.Metadata.UID)[0].Metadata.UID
	otherWindow := registry.WindowsOf(other.Project.Metadata.UID)[0].Metadata.UID
	foreign, err := mutator.CreateAgent(registry, otherWindow, CreateAgentOptions{Provider: "claude", OperationID: "op-0"})
	if err != nil {
		t.Fatalf("create foreign agent: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(registry, foreign.Metadata.UID, AgentSessionObservation{
		Provider:  "claude",
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("record session ref: %v", err)
	}

	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkMinted {
		t.Fatalf("linkage = %+v, want a mint inside the paired Window", linkage)
	}
	if minted, _ := registry.Agent(linkage.AgentUID); minted.Metadata.OwnerUID() != windowUID {
		t.Fatalf("minted Agent owner = %q, want %q", minted.Metadata.OwnerUID(), windowUID)
	}
	if untouched, _ := registry.Agent(foreign.Metadata.UID); untouched.Status.PaneRef != "" {
		t.Fatalf("an Agent in another Window took the pane: %+v", untouched.Status)
	}
}

// TestLinkAgentPanePreservesIdentityAndName is the preservation contract, which
// promotion is the one write in this file that could plausibly violate: the uid
// and the name the operator already selects by must survive a re-parent.
func TestLinkAgentPanePreservesIdentityAndName(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	wantUID, wantName := pane.Metadata.UID, pane.Metadata.Name
	windowPanesBefore := len(registry.Panes)

	if _, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, claudePane("sess-1"), NewBindingMatcher(RuntimeObservation{}), "op-1"); err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	promoted, ok := registry.Pane(wantUID)
	if !ok {
		t.Fatalf("Pane %q was re-identified or deleted by the promotion", wantUID)
	}
	if promoted.Metadata.Name != wantName {
		t.Fatalf("promoted Pane name = %q, want the unchanged %q", promoted.Metadata.Name, wantName)
	}
	if len(registry.Panes) != windowPanesBefore {
		t.Fatalf("Pane count moved from %d to %d; promotion must not create or delete a Pane", windowPanesBefore, len(registry.Panes))
	}
	// The reservation followed the resource into the Agent scope, which is what
	// keeps the name unique where it is now looked up.
	if owner, taken := registry.nameOwner(linkedAgentUID(t, registry, wantUID), KindPane, wantName); !taken || owner != wantUID {
		t.Fatalf("the Pane name reservation did not follow it into the Agent scope")
	}
	if _, taken := registry.nameOwner(windowUID, KindPane, wantName); taken {
		t.Fatal("the old Window-scoped reservation was left behind")
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after promotion: %v", err)
	}
}

func linkedAgentUID(t *testing.T, registry *Registry, paneUID string) string {
	t.Helper()
	pane, ok := registry.Pane(paneUID)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent {
		t.Fatalf("pane %q is not Agent-owned", paneUID)
	}
	return pane.Metadata.OwnerRef.UID
}

// TestLinkAgentPaneRefusesUnknownScopeWithZeroWrites keeps a caller that lost
// its Window or Pane between the match and the link from mutating anything.
func TestLinkAgentPaneRefusesUnknownScopeWithZeroWrites(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	pane := importedShellPane(t, mutator, registry, windowUID, claudePane("sess-1"))
	before := registry.Clone()

	if _, err := mutator.LinkAgentPane(registry, "win-does-not-exist", pane.Metadata.UID, claudePane("sess-1"), nil, "op-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown window error = %v, want ErrNotFound", err)
	}
	if _, err := mutator.LinkAgentPane(registry, windowUID, "pane-does-not-exist", claudePane("sess-1"), nil, "op-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown pane error = %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(*registry, before) {
		t.Fatal("a refused link still wrote to the registry")
	}
}

// TestLinkAgentPaneKeepsAnUnknownProviderSpellingAnAgent matches the create
// path exactly: a pane that declares some provider projmux does not recognize
// is still a projmux-launched agent pane and must not be demoted to a shell.
func TestLinkAgentPaneKeepsAnUnknownProviderSpellingAnAgent(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := agentLinkageFixture(t)
	observed := LegacyPane{Provider: "some-future-model", LaunchAuthorship: "1", Command: "sfm"}
	pane := importedShellPane(t, mutator, registry, windowUID, observed)

	linkage, err := mutator.LinkAgentPane(registry, windowUID, pane.Metadata.UID, observed, NewBindingMatcher(RuntimeObservation{}), "op-1")
	if err != nil {
		t.Fatalf("link agent pane: %v", err)
	}
	if linkage.Kind != AgentLinkMinted {
		t.Fatalf("linkage = %+v, want a mint", linkage)
	}
	agent, _ := registry.Agent(linkage.AgentUID)
	if agent.Metadata.Name != FallbackAgentNameBase {
		t.Fatalf("minted Agent name = %q, want %q", agent.Metadata.Name, FallbackAgentNameBase)
	}
	if agent.Spec.Provider != "" {
		t.Fatalf("an unrecognized spelling must not become a normalized provider, got %q", agent.Spec.Provider)
	}
}
