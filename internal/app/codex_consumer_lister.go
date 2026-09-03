package app

import (
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// generationAwareLivePaneLister decorates the tmux presentation with the
// Registry's exact runtimeID -> Pane ownerRef -> Agent paneRef chain. It is
// read-only. Provider-neutral and pre-generation Codex rows retain their
// existing behavior; a declared generation tuple fails closed when the chain
// or canonical endpoint/fence is no longer exact.
type generationAwareLivePaneLister struct {
	base         livePaneLister
	readRegistry func() (coremetadata.Registry, error)
}

func newGenerationAwareLivePaneLister(base livePaneLister, readRegistry func() (coremetadata.Registry, error)) livePaneLister {
	return generationAwareLivePaneLister{base: base, readRegistry: readRegistry}
}

func (l generationAwareLivePaneLister) ListLivePanes() ([]livePaneRow, error) {
	rows, err := l.base.ListLivePanes()
	if err != nil || l.readRegistry == nil {
		return rows, err
	}
	registry, readErr := l.readRegistry()
	if readErr != nil {
		// A failed Registry read cannot prove whether a Codex row still owns the
		// generation-aware tuple it presented previously. Suppress every Codex
		// action for this observation; notification classification will also see
		// the missing exact metadata and keep an existing generation notice stale.
		for i := range rows {
			if strings.TrimSpace(rows[i].Agent) == aiModeCodex {
				rows[i].ReplyState = false
			}
		}
		return rows, nil
	}
	for i := range rows {
		if strings.TrimSpace(rows[i].Agent) != aiModeCodex {
			continue
		}
		decorateGenerationLivePane(&rows[i], registry)
	}
	return rows, nil
}

func decorateGenerationLivePane(row *livePaneRow, registry coremetadata.Registry) {
	if row == nil || strings.TrimSpace(row.Pane) == "" {
		return
	}
	type candidate struct {
		agent coremetadata.Agent
		pane  coremetadata.Pane
	}
	candidates := make([]candidate, 0, 1)
	for _, agent := range registry.Agents {
		if agent.Spec.Provider != aiModeCodex || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
			agent.Status.SessionRef.Codex.Lifecycle == nil || agent.Status.PaneRef == "" {
			continue
		}
		pane, ok := registry.Pane(agent.Status.PaneRef)
		if !ok || pane.Status.Activation.RuntimeID != row.Pane {
			continue
		}
		candidates = append(candidates, candidate{agent: agent, pane: *pane})
	}
	if len(candidates) == 0 {
		return // provider-neutral compatibility lane
	}
	if len(candidates) != 1 {
		row.ReplyState = false
		return
	}
	match := candidates[0]
	row.AgentUID, row.PaneUID = match.agent.Metadata.UID, match.pane.Metadata.UID
	codex := match.agent.Status.SessionRef.Codex
	if codex.Endpoint != nil {
		row.StateDomainID = codex.Endpoint.StateDomainID
		row.EndpointGenerationID = codex.Endpoint.EndpointGenerationID
	}
	input, durable, authorized := resourceAgentLifecycleProjectionInput(registry, match.agent, match.pane.Metadata.UID, row.Pane, match.agent.EffectiveInteraction(time.Now()).Kind)
	if !durable || !authorized || match.pane.Status.Activation.Codex == nil || match.pane.Status.Activation.Codex.Authority == nil {
		row.ReplyState = false
		return
	}
	authority := match.pane.Status.Activation.Codex.Authority
	projection := codexgeneration.ProjectConsumers(input, codexgeneration.RuntimeMutationInput{
		DurableEndpoint: codex.Endpoint, StoredAuthority: authority, PresentedAuthority: authority,
		TargetRuntimeID: match.pane.Status.Activation.RuntimeID, EventRuntimeID: row.Pane,
	}, true)
	row.AuthorityFence = projection.Fence
	row.ReplyState = row.ReplyState && projection.Sidebar &&
		row.AIState == projection.Lifecycle.State
}
