package app

import (
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
func (c *aiCommand) recordAgentSessionRef(paneID string, obs coremetadata.AgentSessionObservation) {
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
	// Pre-check outside the lock so an unchanged observation never opens a
	// write transaction at all.
	if agent, ok := registry.Agent(agentUID); ok {
		if candidate, built := coremetadata.NewAgentSessionRef(obs, clock()); built && agent.Status.SessionRef.SameConversation(candidate) {
			return
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
