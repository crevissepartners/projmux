package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// recordAgentSessionRef persists the provider conversation ref an already
// ingested hook reported onto the Agent resource that owns the pane the event
// was attributed to.
//
// This is purely additive to the ingest path. The tmux pane options — including
// `@projmux_ai_session_id` — are written exactly as before and are not read,
// moved, or removed here, because the two values answer different questions:
// the pane option is the live routing index ingest matches incoming events
// against, and this one is the durable conversation pointer that has to outlive
// the Pane.
//
// Every step is best effort and silent, matching the surrounding
// markAIHookPane convention where every transport write is a discarded error. A
// hook must never fail because the resource registry was unavailable, and an
// ingest whose only job was a status update must never become the reason a user
// loses a notification.
//
// The ordering below is load-bearing for the no-side-effect contract:
//
//  1. The read goes through LoadReadOnly, which creates no directory, no lock,
//     and no registry file. A user who has never used the resource model still
//     has no <state>/projmux/metadata/ after a hook fires.
//  2. An empty Agent set short-circuits before any tmux query, so the common
//     unmanaged case costs one file stat and nothing else.
//  3. The store transaction only opens once a matching Agent is found AND the
//     observed conversation differs from what is already stored, so a hook that
//     fires every turn rewrites nothing.
func (c *aiCommand) stageAgentSessionRef(paneID string, obs coremetadata.AgentSessionObservation) {
	if c == nil || strings.TrimSpace(paneID) == "" {
		return
	}
	if _, ok := coremetadata.NewAgentSessionRef(obs, c.sessionRefClock()()); !ok {
		return
	}
	c.agentObservationMu.Lock()
	defer c.agentObservationMu.Unlock()
	if c.pendingAgentSessionRefs == nil {
		c.pendingAgentSessionRefs = map[string]coremetadata.AgentSessionObservation{}
	}
	c.pendingAgentSessionRefs[paneID] = obs
}

func (c *aiCommand) takeAgentSessionRef(paneID string) (coremetadata.AgentSessionObservation, bool) {
	if c == nil {
		return coremetadata.AgentSessionObservation{}, false
	}
	c.agentObservationMu.Lock()
	defer c.agentObservationMu.Unlock()
	obs, ok := c.pendingAgentSessionRefs[paneID]
	delete(c.pendingAgentSessionRefs, paneID)
	return obs, ok
}

func (c *aiCommand) flushPendingAgentSessionRef(paneID string) {
	obs, ok := c.takeAgentSessionRef(paneID)
	if ok {
		c.persistAgentSessionRef(paneID, obs)
	}
	if native, ok := c.takeCodexBinding(paneID); ok {
		c.persistCodexBindingRefinement(paneID, native)
	}
}

func (c *aiCommand) stageCodexBinding(paneID, threadID, turnID string) {
	if c == nil || strings.TrimSpace(paneID) == "" || strings.TrimSpace(threadID) == "" {
		return
	}
	c.agentObservationMu.Lock()
	defer c.agentObservationMu.Unlock()
	if c.pendingCodexBindings == nil {
		c.pendingCodexBindings = map[string]coremetadata.CodexActivationObservation{}
	}
	c.pendingCodexBindings[paneID] = coremetadata.CodexActivationObservation{ThreadID: threadID, TurnID: turnID}
}

func (c *aiCommand) takeCodexBinding(paneID string) (coremetadata.CodexActivationObservation, bool) {
	if c == nil {
		return coremetadata.CodexActivationObservation{}, false
	}
	c.agentObservationMu.Lock()
	defer c.agentObservationMu.Unlock()
	obs, ok := c.pendingCodexBindings[paneID]
	delete(c.pendingCodexBindings, paneID)
	return obs, ok
}

// nativeCodexHookAllowed is the early, pre-tmux-write guard for a Pane whose
// current generation already has app-server identity. Fallback generations have
// no native binding and retain the existing hook behavior.
func (c *aiCommand) nativeCodexHookAllowed(paneID, threadID string) (bool, string) {
	if !c.resourceOwnedPane(paneID) {
		return true, ""
	}
	binding, ok, err := c.managedAgentBindingForPane(paneID)
	if err != nil {
		return false, "native binding unavailable"
	}
	if !ok || binding.codex == nil {
		return true, ""
	}
	if !c.exactProviderActivationEvidence(binding, paneID) {
		return false, "stale activation generation"
	}
	if strings.TrimSpace(threadID) != binding.codex.ThreadID {
		return false, "thread does not match native binding"
	}
	return true, ""
}

// routeNativeCodexHook resolves native hook identity only from the internal
// activation envelope and the Registry's exact current Pane generation. A
// native generation never consults cwd, title, Pane ordering, or TMUX_PANE.
// handled=false means this is a current legacy generation and the established
// hook matcher remains its compatibility lane.
func (c *aiCommand) routeNativeCodexHook(threadID string) (paneID string, handled, allowed bool, reason string) {
	paneUID := strings.TrimSpace(c.env(internalActivationPaneUIDEnv))
	generation := strings.TrimSpace(c.env(internalActivationGenerationEnv))
	if paneUID == "" && generation == "" {
		return "", false, true, ""
	}
	if paneUID == "" || generation == "" || c.loadRegistry == nil {
		return "", true, false, "incomplete activation identity"
	}
	var registry coremetadata.Registry
	var pane *coremetadata.Pane
	for attempt := range 11 {
		loaded, err := c.loadRegistry()
		if err != nil {
			return "", true, false, "native binding unavailable"
		}
		registry = loaded
		if current, ok := registry.Pane(paneUID); ok {
			pane = current
			break
		}
		if attempt < 10 {
			// The provider can emit its first hook while the create transaction
			// is publishing the Pane it just launched. Wait only for that exact
			// uid to become readable; never search by cwd, title, or order.
			c.sleepFor(25 * time.Millisecond)
		}
	}
	if pane == nil {
		return "", true, false, "stale activation Pane"
	}
	if pane.Status.Activation.Generation != generation || pane.Status.Activation.AgentUID == "" ||
		strings.TrimSpace(pane.Status.Activation.RuntimeID) == "" {
		return "", true, false, "stale activation generation"
	}
	agent, ok := registry.Agent(pane.Status.Activation.AgentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID ||
		pane.Metadata.OwnerUID() != agent.Metadata.UID {
		return "", true, false, "stale Agent/Pane binding"
	}
	if pane.Status.Activation.Codex == nil {
		return "", false, true, ""
	}
	if strings.TrimSpace(threadID) != pane.Status.Activation.Codex.ThreadID {
		return "", true, false, "thread does not match native binding"
	}
	return pane.Status.Activation.RuntimeID, true, true, ""
}

func (c *aiCommand) persistAgentSessionRef(paneID string, obs coremetadata.AgentSessionObservation) {
	if c == nil || c.loadRegistry == nil || c.updateRegistry == nil {
		return
	}
	if paneID == "" {
		return
	}
	clock := c.sessionRefClock()
	if _, ok := coremetadata.NewAgentSessionRef(obs, clock()); !ok {
		return
	}

	registry, err := c.loadRegistry()
	if err != nil || len(registry.Agents) == 0 {
		return
	}
	paneUID := c.readTmuxPaneOption(paneID, tmuxopts.PaneUID)
	if paneUID == "" {
		return
	}
	agentUID, ok := agentUIDForPaneUID(registry, paneUID)
	if !ok {
		return
	}
	pane, _ := registry.Pane(paneUID)
	if pane != nil && pane.Status.Activation.Codex != nil {
		candidate, built := coremetadata.NewAgentSessionRef(obs, clock())
		if !built || candidate.Codex == nil || candidate.Codex.ThreadID != pane.Status.Activation.Codex.ThreadID ||
			strings.TrimSpace(c.env(internalActivationPaneUIDEnv)) != paneUID ||
			strings.TrimSpace(c.env(internalActivationGenerationEnv)) != pane.Status.Activation.Generation {
			return
		}
	}
	// Pre-check outside the lock so an unchanged observation never opens a
	// write transaction at all.
	if agent, ok := registry.Agent(agentUID); ok {
		if candidate, built := coremetadata.NewAgentSessionRef(obs, clock()); built {
			// A hook attributed to the right Pane but naming another provider is
			// refused before the write transaction opens. This is the same
			// cross-provider invariant RecordAgentSessionRef enforces under the
			// lock, made write-free on the common ingest path.
			if agent.Spec.Provider != "" && candidate.Provider != agent.Spec.Provider {
				return
			}
			if agent.Status.SessionRef.SameConversation(candidate) {
				return
			}
		}
	}

	// The recorded observedAt is the ingest clock, not wall time inside the
	// mutator, so a hook and the ref it produced carry the same instant.
	mutator := intmetadata.DefaultMutator()
	mutator.Now = clock

	_, err = c.updateRegistry(func(working *coremetadata.Registry) error {
		if _, ok := working.Agent(agentUID); !ok {
			return errAgentSessionRefNoop
		}
		_, changed, err := mutator.RecordAgentSessionRef(working, agentUID, obs)
		if err != nil {
			return err
		}
		if !changed {
			return errAgentSessionRefNoop
		}
		return nil
	})
	_ = err
}

// managedAgentForPane resolves only the current live Pane binding of a
// Registry Agent. Compatibility commands use this to forward through the
// canonical authority without treating a shell pane or stale binding as an
// Agent.
func (c *aiCommand) managedAgentForPane(paneID string) (coremetadata.Agent, bool, error) {
	binding, ok, err := c.managedAgentBindingForPane(paneID)
	return binding.agent, ok, err
}

// managedAgentBinding is the exact evidence a provider-hook observation is
// allowed to refine. Pane uid alone is durable across resume/replacement; the
// activation generation names the one materialization that emitted the hook.
type managedAgentBinding struct {
	agent      coremetadata.Agent
	paneUID    string
	generation string
	runtimeID  string
	codex      *coremetadata.CodexActivationBinding
}

func (c *aiCommand) managedAgentBindingForPane(paneID string) (managedAgentBinding, bool, error) {
	if c == nil || c.loadRegistry == nil || strings.TrimSpace(paneID) == "" {
		return managedAgentBinding{}, false, nil
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return managedAgentBinding{}, false, err
	}
	if len(registry.Agents) == 0 {
		return managedAgentBinding{}, false, nil
	}
	paneUID := c.readTmuxPaneOption(paneID, tmuxopts.PaneUID)
	agentUID, ok := agentUIDForPaneUID(registry, paneUID)
	if !ok {
		return managedAgentBinding{}, false, nil
	}
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID {
		return managedAgentBinding{}, false, nil
	}
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return managedAgentBinding{}, false, nil
	}
	return managedAgentBinding{
		agent:      agent.Clone(),
		paneUID:    paneUID,
		generation: pane.Status.Activation.Generation,
		runtimeID:  pane.Status.Activation.RuntimeID,
		codex:      pane.Status.Activation.Codex,
	}, true, nil
}

func (c *aiCommand) persistCodexBindingRefinement(paneID string, obs coremetadata.CodexActivationObservation) {
	binding, ok, err := c.managedAgentBindingForPane(paneID)
	if err != nil || !ok || binding.codex == nil || !c.exactProviderActivationEvidence(binding, paneID) || c.updateRegistry == nil {
		return
	}
	obs.AgentUID = binding.agent.Metadata.UID
	obs.PaneUID = binding.paneUID
	obs.Generation = binding.generation
	if binding.agent.Status.SessionRef == nil || binding.agent.Status.SessionRef.Codex == nil ||
		binding.agent.Status.SessionRef.Codex.Endpoint == nil || !binding.agent.Status.SessionRef.Codex.Endpoint.Valid() {
		return
	}
	obs.Endpoint = *binding.agent.Status.SessionRef.Codex.Endpoint
	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	_, _ = c.updateRegistry(func(working *coremetadata.Registry) error {
		changed, err := mutator.RefineCodexActivation(working, obs)
		if err != nil {
			return err
		}
		if !changed {
			return errAgentSessionRefNoop
		}
		return nil
	})
}

// resourceOwnedPane is the fail-closed watcher gate. Any Pane carrying resource
// identity is withheld from title/content inference. The title watcher belongs
// only to legacy panes and must not depend on a Registry read to recognize that
// boundary.
func (c *aiCommand) resourceOwnedPane(paneID string) bool {
	if c == nil || strings.TrimSpace(paneID) == "" {
		return false
	}
	return c.readTmuxPaneOption(paneID, tmuxopts.PaneUID) != ""
}

func (c *aiCommand) persistManagedAgentTopic(paneID, topic string) (coremetadata.Agent, bool, error) {
	agent, ok, err := c.managedAgentForPane(paneID)
	if err != nil || !ok {
		return agent, ok, err
	}
	if c.updateRegistry == nil {
		return coremetadata.Agent{}, true, fmt.Errorf("agent registry mutation is not configured")
	}
	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	var committed coremetadata.Agent
	_, err = c.updateRegistry(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(agent.Metadata.UID)
		if !ok || current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != agent.Status.PaneRef {
			return fmt.Errorf("managed Agent binding changed before topic commit")
		}
		updated, err := mutator.SetAgentTopic(working, agent.Metadata.UID, topic)
		committed = updated.Clone()
		return err
	})
	return committed, true, err
}

// persistManagedAgentStartupReadiness records the exact provider SessionStart
// observation without turning it into initial-task acknowledgement. Reusing
// pending keeps the Registry schema stable; provider-hook source plus the first
// observation timestamp is the one-shot boundary AwaitAgentActivation uses to
// open its independent acknowledgement window.
func (c *aiCommand) persistManagedAgentStartupReadiness(paneID, provider string) (coremetadata.Agent, bool, error) {
	binding, ok, err := c.managedAgentBindingForPane(paneID)
	if err != nil || !ok {
		return binding.agent, ok, err
	}
	agent := binding.agent
	if agent.Spec.Provider != strings.TrimSpace(provider) {
		return agent, true, nil
	}
	if c.updateRegistry == nil {
		return coremetadata.Agent{}, true, fmt.Errorf("agent registry mutation is not configured")
	}
	if !c.exactProviderActivationEvidence(binding, paneID) ||
		agent.Status.Activation.State != coremetadata.ActivationPending {
		return agent, true, nil
	}

	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	var committed coremetadata.Agent
	_, err = c.updateRegistry(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(agent.Metadata.UID)
		if !ok || current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != binding.paneUID {
			return fmt.Errorf("managed Agent binding changed before startup readiness commit")
		}
		currentPane, ok := working.Pane(binding.paneUID)
		if !ok || currentPane.Status.Activation.Generation != binding.generation ||
			currentPane.Status.Activation.AgentUID != agent.Metadata.UID ||
			currentPane.Status.Activation.RuntimeID != strings.TrimSpace(paneID) {
			return fmt.Errorf("managed Agent activation generation changed before startup readiness commit")
		}
		// The first exact SessionStart fixes the boundary. A duplicate hook may
		// not slide the acknowledgement deadline forward.
		if current.Status.Activation.State != coremetadata.ActivationPending ||
			(current.Status.Activation.Source == string(coremetadata.InteractionSourceProviderHook) &&
				!current.Status.Activation.ObservedAt.IsZero()) {
			committed = current.Clone()
			return nil
		}
		updated, setErr := mutator.SetAgentActivation(working, agent.Metadata.UID,
			coremetadata.ActivationPending, string(coremetadata.InteractionSourceProviderHook), "")
		committed = updated.Clone()
		return setErr
	})
	return committed, true, err
}

func (c *aiCommand) exactProviderActivationEvidence(binding managedAgentBinding, paneID string) bool {
	return strings.TrimSpace(binding.generation) != "" &&
		binding.runtimeID == strings.TrimSpace(paneID) &&
		strings.TrimSpace(c.env(internalActivationPaneUIDEnv)) == binding.paneUID &&
		strings.TrimSpace(c.env(internalActivationGenerationEnv)) == binding.generation
}

func (c *aiCommand) persistManagedAgentInteractionWithActivationPolicy(paneID string, kind coremetadata.AgentInteractionKind, source string, activationEligible bool) (coremetadata.Agent, bool, error) {
	binding, ok, err := c.managedAgentBindingForPane(paneID)
	if err != nil || !ok {
		return binding.agent, ok, err
	}
	agent := binding.agent
	if c.updateRegistry == nil {
		return coremetadata.Agent{}, true, fmt.Errorf("agent registry mutation is not configured")
	}
	mutator := intmetadata.DefaultMutator()
	clock := c.sessionRefClock()
	mutator.Now = clock
	obs, hasObservation := c.takeAgentSessionRef(paneID)
	nativeObservation, hasNativeObservation := c.takeCodexBinding(paneID)
	if binding.codex != nil {
		if !c.exactProviderActivationEvidence(binding, paneID) {
			return agent, true, errManagedAgentObservationIgnored
		}
		if hasObservation {
			candidate, built := coremetadata.NewAgentSessionRef(obs, clock())
			if !built || candidate.Codex == nil || candidate.Codex.ThreadID != binding.codex.ThreadID {
				return agent, true, errManagedAgentObservationIgnored
			}
		}
	}
	if hasObservation {
		if candidate, built := coremetadata.NewAgentSessionRef(obs, clock()); built && agent.Spec.Provider != "" && candidate.Provider != agent.Spec.Provider {
			return agent, true, errManagedAgentObservationIgnored
		}
	}
	current := agent.Status.Interaction
	sessionUnchanged := !hasObservation
	if hasObservation {
		if candidate, built := coremetadata.NewAgentSessionRef(obs, clock()); built {
			sessionUnchanged = agent.Status.SessionRef.SameConversation(candidate)
		}
	}
	activationNeedsAck := activationEligible && c.exactProviderActivationEvidence(binding, paneID) &&
		source == string(coremetadata.InteractionSourceProviderHook) &&
		(agent.Status.Activation.State == coremetadata.ActivationPending || agent.Status.Activation.State == coremetadata.ActivationUnconfirmed) &&
		kind != coremetadata.InteractionUnknown && kind != coremetadata.InteractionIdle
	turnUnchanged := !hasNativeObservation || binding.codex == nil || strings.TrimSpace(nativeObservation.TurnID) == "" || binding.codex.TurnID == strings.TrimSpace(nativeObservation.TurnID)
	if sessionUnchanged && turnUnchanged && !activationNeedsAck && current.Kind == kind && current.Source == source && clock().Sub(current.ObservedAt) < time.Second {
		return agent, true, nil
	}
	var committed coremetadata.Agent
	_, err = c.updateRegistry(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(agent.Metadata.UID)
		if !ok || current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != agent.Status.PaneRef {
			return fmt.Errorf("managed Agent binding changed before interaction commit")
		}
		if activationNeedsAck {
			currentPane, ok := working.Pane(binding.paneUID)
			if !ok || currentPane.Status.Activation.Generation != binding.generation ||
				currentPane.Status.Activation.AgentUID != agent.Metadata.UID ||
				currentPane.Status.Activation.RuntimeID != strings.TrimSpace(paneID) {
				return fmt.Errorf("managed Agent activation generation changed before interaction commit")
			}
		}
		if hasObservation {
			if _, _, err := mutator.RecordAgentSessionRef(working, agent.Metadata.UID, obs); err != nil {
				return err
			}
		}
		if hasNativeObservation && binding.codex != nil {
			nativeObservation.AgentUID = binding.agent.Metadata.UID
			nativeObservation.PaneUID = binding.paneUID
			nativeObservation.Generation = binding.generation
			if current.Status.SessionRef == nil || current.Status.SessionRef.Codex == nil ||
				current.Status.SessionRef.Codex.Endpoint == nil || !current.Status.SessionRef.Codex.Endpoint.Valid() {
				return fmt.Errorf("managed Agent durable Codex endpoint is missing")
			}
			nativeObservation.Endpoint = *current.Status.SessionRef.Codex.Endpoint
			if _, err := mutator.RefineCodexActivation(working, nativeObservation); err != nil {
				return err
			}
		}
		updated, err := mutator.SetAgentInteraction(working, agent.Metadata.UID, kind, source)
		if err != nil {
			return err
		}
		if activationNeedsAck && source == string(coremetadata.InteractionSourceProviderHook) &&
			(current.Status.Activation.State == coremetadata.ActivationPending || current.Status.Activation.State == coremetadata.ActivationUnconfirmed) &&
			kind != coremetadata.InteractionUnknown && kind != coremetadata.InteractionIdle {
			updated, err = mutator.SetAgentActivation(working, agent.Metadata.UID, coremetadata.ActivationAcknowledged, source, "")
			if err != nil {
				return err
			}
		}
		committed = updated.Clone()
		return nil
	})
	return committed, true, err
}

var errManagedAgentObservationIgnored = errors.New("managed Agent observation provider does not match")

// sessionRefClock is the observation clock of the session ref write. It falls
// back to wall time so a partially constructed aiCommand cannot panic inside a
// hook.
func (c *aiCommand) sessionRefClock() func() time.Time {
	if c != nil && c.now != nil {
		return c.now
	}
	return time.Now
}

// errAgentSessionRefNoop aborts the registry transaction when there is nothing
// to write. The store performs no write at all on a failed operation, so
// returning it is how this path stays read-only in the unchanged case.
var errAgentSessionRefNoop = errAgentSessionRefNothingToDo{}

type errAgentSessionRefNothingToDo struct{}

func (errAgentSessionRefNothingToDo) Error() string {
	return "agent session ref: nothing to record"
}

// agentUIDForPaneUID resolves the Agent that owns a managed Pane uid.
//
// Only an Agent-owned Pane resolves. A shell Pane is owned by its Window and
// has no conversation to record, so a hook that lands on one is ignored rather
// than attributed to the Window's Agents.
func agentUIDForPaneUID(registry coremetadata.Registry, paneUID string) (string, bool) {
	pane, ok := registry.Pane(paneUID)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent {
		return "", false
	}
	if _, ok := registry.Agent(pane.Metadata.OwnerRef.UID); !ok {
		return "", false
	}
	return pane.Metadata.OwnerRef.UID, true
}
