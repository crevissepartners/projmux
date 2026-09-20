package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// claudeShapeFixture builds the smallest Registry whose route resolution fails
// for exactly one reason: the Claude registration. Everything else -- phase,
// ownership, activation identity -- is deliberately valid, so a test that
// reports a shape is reporting the registration and nothing else.
func claudeShapeFixture(t *testing.T, registered, residualSession, terminated bool) (coremetadata.Registry, coremetadata.Agent) {
	t.Helper()
	const agentUID, paneUID = "agt-claude-shape", "pan-claude-shape"
	pane := coremetadata.Pane{}
	pane.Metadata.UID = paneUID
	pane.Metadata.Name = "worker-pane"
	pane.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: agentUID}
	pane.Spec.Role = coremetadata.PaneRoleAgent
	process := coremetadata.ProcessIdentity{PID: 4242, OwnerUID: 1000, Start: "linux:boot:1"}
	binding := &coremetadata.ClaudeActivationBinding{Process: process}
	if residualSession || registered {
		binding.RegistrationSessionID = "session-1"
		binding.RegistrationGeneration = "registration-1"
	}
	if registered {
		binding.Registration = &coremetadata.ClaudeRegistration{Ready: true, Authority: coremetadata.ClaudeAuthorityRef{
			SessionID: "session-1", Process: process, RegistrationGeneration: "registration-1",
			LeaseProcess: coremetadata.ProcessIdentity{PID: 4243, OwnerUID: 1000, Start: "linux:boot:2"},
		}}
	}
	pane.Status.Activation = coremetadata.PaneActivation{
		Generation: "gen-1", RuntimeID: "%7", AgentUID: agentUID, Claude: binding,
	}
	if terminated {
		pane.Status.LastTermination = &coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceReconcile, Classification: coremetadata.TerminationUnknown,
		}
	}
	agent := coremetadata.Agent{}
	agent.Metadata.UID = agentUID
	agent.Metadata.Name = "lead-ship-worker"
	agent.Spec.Provider = "claude"
	agent.Status.Phase = coremetadata.PhaseRunning
	agent.Status.PaneRef = paneUID
	registry := coremetadata.NewRegistry()
	registry.Panes = []coremetadata.Pane{pane}
	registry.Agents = []coremetadata.Agent{agent}
	return registry, agent
}

// withClaudeShapeLiveness states host process truth for one test instead of
// spawning a process, and restores the production reader afterwards.
func withClaudeShapeLiveness(t *testing.T, alive bool) {
	t.Helper()
	previous := claudeActivationProcessAlive
	claudeActivationProcessAlive = func(coremetadata.ProcessIdentity) bool { return alive }
	t.Cleanup(func() { claudeActivationProcessAlive = previous })
}

// claudeShapeCases is the three failing shapes with the host truth each needs.
var claudeShapeCases = []struct {
	name            string
	residualSession bool
	processAlive    bool
	terminated      bool
	want            coremetadata.ClaudeRegistrationShape
}{
	{name: "never registered", processAlive: true, want: coremetadata.ClaudeRegistrationNeverStarted},
	{name: "registration lost", residualSession: true, processAlive: true, want: coremetadata.ClaudeRegistrationLost},
	{name: "session gone", terminated: true, want: coremetadata.ClaudeRegistrationSessionGone},
}

// TestExplainClaudeRouteReasonSeparatesTheThreeShapes fixes the defect this
// change exists for: the three shapes used to refuse with one byte-identical
// sentence, so a refusal said nothing about which one had happened.
func TestExplainClaudeRouteReasonSeparatesTheThreeShapes(t *testing.T) {
	seen := map[string]string{}
	for _, testCase := range claudeShapeCases {
		t.Run(testCase.name, func(t *testing.T) {
			withClaudeShapeLiveness(t, testCase.processAlive)
			registry, agent := claudeShapeFixture(t, false, testCase.residualSession, testCase.terminated)
			_, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID)
			if reason != coremetadata.ClaudeRegistrationUnavailableReason {
				t.Fatalf("fixture failed for the wrong reason: %q", reason)
			}
			explained := explainClaudeRouteReason(registry, agent, reason)
			if !strings.HasPrefix(explained, coremetadata.ClaudeRegistrationUnavailableReason+"; ") {
				t.Fatalf("the exact existing reason is no longer the prefix: %q", explained)
			}
			if !strings.Contains(explained, "uid:"+agent.Metadata.UID) {
				t.Fatalf("the refusal does not name which Agent: %q", explained)
			}
			if other, ok := seen[explained]; ok {
				t.Fatalf("shapes %q and %q refuse identically: %q", other, testCase.name, explained)
			}
			seen[explained] = testCase.name
		})
	}
	if len(seen) != len(claudeShapeCases) {
		t.Fatalf("got %d distinct refusals for %d shapes", len(seen), len(claudeShapeCases))
	}
}

// TestExplainClaudeRouteReasonLeavesOtherReasonsExact keeps the change purely
// additive: only the one registration reason grows a tail.
func TestExplainClaudeRouteReasonLeavesOtherReasonsExact(t *testing.T) {
	withClaudeShapeLiveness(t, true)
	registry, agent := claudeShapeFixture(t, false, false, false)
	for _, reason := range []string{"no current Running Agent activation", "managed activation ownership mismatch", "provider has no endpoint adapter"} {
		if got := explainClaudeRouteReason(registry, agent, reason); got != reason {
			t.Fatalf("reason %q was rewritten to %q", reason, got)
		}
	}
	registry, agent = claudeShapeFixture(t, true, true, false)
	if got := explainClaudeRouteReason(registry, agent, coremetadata.ClaudeRegistrationUnavailableReason); got != coremetadata.ClaudeRegistrationUnavailableReason {
		t.Fatalf("a ready registration grew an explanation: %q", got)
	}
}

// measuredRefusedRecovery is the two spellings the recovery string used to
// name that a missing lease can never satisfy: `--dialogue-reply-only` resume
// is refused while the Agent is Running, and `agent message qualify` requires
// the very lease that is absent. Both were run against a Running unregistered
// Agent and both were refused.
var measuredRefusedRecovery = []string{"--dialogue-reply-only", "agent message qualify"}

// resumePreconditions are the phrases that make a plain `agent resume` honest.
// Resume rebinds an Offline or Failed Agent only, so naming it without saying
// the session must end first is the same unexecutable advice in new words.
var resumePreconditions = []string{"let this one exit", "no longer Running"}

// TestClaudeCoordinationRecoveryNamesOnlyExecutableActions holds the recovery
// projection to the measurement: no shape may be handed a command that its own
// state refuses, and a resume may only appear with the precondition that makes
// it callable.
func TestClaudeCoordinationRecoveryNamesOnlyExecutableActions(t *testing.T) {
	seen := map[string]string{}
	for _, testCase := range claudeShapeCases {
		t.Run(testCase.name, func(t *testing.T) {
			withClaudeShapeLiveness(t, testCase.processAlive)
			registry, agent := claudeShapeFixture(t, false, testCase.residualSession, testCase.terminated)
			projection := projectClaudeCoordinationEligibilityAt(registry, agent, "")
			if projection == nil || projection.Eligible {
				t.Fatalf("coordination projection = %#v", projection)
			}
			if !strings.HasPrefix(projection.Reason, coremetadata.ClaudeRegistrationUnavailableReason+"; ") {
				t.Fatalf("reason lost its exact prefix: %q", projection.Reason)
			}
			if projection.Recovery == "" {
				t.Fatal("a refused shape has no recovery")
			}
			for _, refused := range measuredRefusedRecovery {
				if strings.Contains(projection.Recovery, refused) {
					t.Fatalf("shape %q recovery still names the refused %q: %q", testCase.want, refused, projection.Recovery)
				}
			}
			if strings.Contains(projection.Recovery, "projmux agent resume") {
				qualified := false
				for _, precondition := range resumePreconditions {
					qualified = qualified || strings.Contains(projection.Recovery, precondition)
				}
				if !qualified {
					t.Fatalf("shape %q recovery names a resume with no precondition: %q", testCase.want, projection.Recovery)
				}
			}
			if other, ok := seen[projection.Recovery]; ok {
				t.Fatalf("shapes %q and %q share a recovery: %q", other, testCase.name, projection.Recovery)
			}
			seen[projection.Recovery] = testCase.name
		})
	}
}

// TestClaudeRegistrationNextActionMatchesMeasuredRecoverability states each
// shape's measured verdict: A needs the hook installed and a new SessionStart,
// B needs only a new SessionStart, and C cannot recover in place at all.
func TestClaudeRegistrationNextActionMatchesMeasuredRecoverability(t *testing.T) {
	never := claudeRegistrationNextAction(coremetadata.ClaudeRegistrationNeverStarted, "agt-x")
	if !strings.Contains(never, "projmux agent integrate claude") || !strings.Contains(never, "SessionStart") {
		t.Fatalf("a never-registered session is not told to install the hook: %q", never)
	}
	lost := claudeRegistrationNextAction(coremetadata.ClaudeRegistrationLost, "agt-x")
	if !strings.Contains(lost, "SessionStart") || strings.Contains(lost, "projmux agent integrate claude") {
		t.Fatalf("a lost lease is not told that one new SessionStart is enough: %q", lost)
	}
	gone := claudeRegistrationNextAction(coremetadata.ClaudeRegistrationSessionGone, "agt-x")
	if !strings.Contains(gone, "no longer Running") || strings.Contains(gone, "SessionStart again") {
		t.Fatalf("a gone session is not told that nothing can re-register it: %q", gone)
	}
	if claudeRegistrationNextAction(coremetadata.ClaudeRegistrationReady, "agt-x") != "" {
		t.Fatal("a ready registration has a next action")
	}
}

// claudeCreateWarningTarget is the activation target of the fixture Agent.
var claudeCreateWarningTarget = agentActivationTarget{
	agentUID: "agt-claude-shape", agentName: "lead-ship-worker",
	paneUID: "pan-claude-shape", paneID: "%7", generation: "gen-1",
}

// TestCreateWarnsWhenActivationFinishesWithNoRegistration covers the state that
// used to be silent: the Pane is live and the Agent looks healthy in every
// projection, but nothing can message it. The create still succeeds -- the
// registration hook runs after the activation this create waited for, so
// failing here would break the ordinary path.
func TestCreateWarnsWhenActivationFinishesWithNoRegistration(t *testing.T) {
	withClaudeShapeLiveness(t, true)
	previousSleep, previousGrace := claudeRegistrationCreateSleep, claudeRegistrationCreateGrace
	slept := 0
	claudeRegistrationCreateSleep = func(time.Duration) { slept++ }
	claudeRegistrationCreateGrace = time.Millisecond
	t.Cleanup(func() {
		claudeRegistrationCreateSleep, claudeRegistrationCreateGrace = previousSleep, previousGrace
	})

	registry, _ := claudeShapeFixture(t, false, false, false)
	loads := 0
	command := &createCommand{store: &resourceStore{load: func() (coremetadata.Registry, error) {
		loads++
		return registry.Clone(), nil
	}}}
	var stderr bytes.Buffer
	if err := command.warnUnregisteredClaudeActivations([]agentActivationTarget{claudeCreateWarningTarget}, &stderr); err != nil {
		t.Fatal(err)
	}
	warning := stderr.String()
	if !strings.Contains(warning, "warning:") || !strings.Contains(warning, "uid:agt-claude-shape") ||
		!strings.Contains(warning, "Pane %7") || !strings.Contains(warning, "projmux agent integrate claude") {
		t.Fatalf("create warning does not name the Agent, its Pane, and the fix: %q", warning)
	}
	if strings.Count(warning, "\n") != 1 {
		t.Fatalf("create warning is not one line: %q", warning)
	}
	if slept == 0 || loads < 2 {
		t.Fatalf("the grace window did not re-read the Registry: slept=%d loads=%d", slept, loads)
	}
}

// TestCreateStaysSilentOnceRegistrationLands proves the grace window is a
// grace and not a delay: a registration that arrives during it ends the loop
// with no warning, which is the ordinary create.
func TestCreateStaysSilentOnceRegistrationLands(t *testing.T) {
	withClaudeShapeLiveness(t, true)
	previousSleep, previousGrace := claudeRegistrationCreateSleep, claudeRegistrationCreateGrace
	claudeRegistrationCreateSleep = func(time.Duration) {}
	claudeRegistrationCreateGrace = time.Second
	t.Cleanup(func() {
		claudeRegistrationCreateSleep, claudeRegistrationCreateGrace = previousSleep, previousGrace
	})

	unregistered, _ := claudeShapeFixture(t, false, false, false)
	registered, _ := claudeShapeFixture(t, true, true, false)
	loads := 0
	command := &createCommand{store: &resourceStore{load: func() (coremetadata.Registry, error) {
		loads++
		if loads == 1 {
			return unregistered.Clone(), nil
		}
		return registered.Clone(), nil
	}}}
	var stderr bytes.Buffer
	if err := command.warnUnregisteredClaudeActivations([]agentActivationTarget{claudeCreateWarningTarget}, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a registration that landed during the grace window still warned: %q", stderr.String())
	}
	if loads != 2 {
		t.Fatalf("the grace loop did not stop at the first ready read: loads=%d", loads)
	}
}

// TestCreateWarningIgnoresNonClaudeAndUnreadableRegistries keeps the warning
// from turning a successful create into noise or into a failure.
func TestCreateWarningIgnoresNonClaudeAndUnreadableRegistries(t *testing.T) {
	withClaudeShapeLiveness(t, true)
	registry, _ := claudeShapeFixture(t, false, false, false)
	registry.Agents[0].Spec.Provider = "codex"
	command := &createCommand{store: &resourceStore{load: func() (coremetadata.Registry, error) {
		return registry.Clone(), nil
	}}}
	var stderr bytes.Buffer
	if err := command.warnUnregisteredClaudeActivations([]agentActivationTarget{claudeCreateWarningTarget}, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("a non-Claude activation warned: err=%v out=%q", err, stderr.String())
	}
	failing := &createCommand{store: &resourceStore{load: func() (coremetadata.Registry, error) {
		return coremetadata.Registry{}, errors.New("registry unreadable")
	}}}
	stderr.Reset()
	if err := failing.warnUnregisteredClaudeActivations([]agentActivationTarget{claudeCreateWarningTarget}, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("an unreadable Registry failed a successful create: err=%v out=%q", err, stderr.String())
	}
}

// TestMessageRouteResolverCarriesTheShapeIntoTheRefusal proves the wiring, not
// just the helper: the refusal an operator actually sees from
// `projmux agent message send` is the one that names the shape.
func TestMessageRouteResolverCarriesTheShapeIntoTheRefusal(t *testing.T) {
	seen := map[string]string{}
	for _, testCase := range claudeShapeCases {
		t.Run(testCase.name, func(t *testing.T) {
			withClaudeShapeLiveness(t, testCase.processAlive)
			registry, agent := claudeShapeFixture(t, false, testCase.residualSession, testCase.terminated)
			_, err := liveAgentMessageRouteResolver{}.Resolve(registry, agent)
			if err == nil {
				t.Fatal("a missing registration resolved a route")
			}
			refusal := err.Error()
			if !strings.HasPrefix(refusal, coremetadata.ClaudeRegistrationUnavailableReason+"; ") {
				t.Fatalf("the refusal lost its exact existing prefix: %q", refusal)
			}
			if other, ok := seen[refusal]; ok {
				t.Fatalf("shapes %q and %q refuse identically through the resolver: %q", other, testCase.name, refusal)
			}
			seen[refusal] = testCase.name
		})
	}
}
