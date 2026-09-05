package aiprovider

import (
	"slices"
	"strings"
)

// AgentSupportMode is the closed static implementation class of one Agent
// action for one provider. It says what the binary implements; it does not say
// whether a particular live Agent is currently eligible to run the action.
type AgentSupportMode string

const (
	SupportGenericRegistry AgentSupportMode = "generic-registry"
	SupportProviderResume  AgentSupportMode = "provider-resume"
	SupportNativeExact     AgentSupportMode = "native-exact-control"
	SupportProviderHook    AgentSupportMode = "provider-hook"
	SupportReadOnlyAdapter AgentSupportMode = "read-only-adapter"
	SupportUnsupported     AgentSupportMode = "unsupported"
)

// CompletionPrecision records the strongest completion evidence the current
// implementation can associate with an action. It is deliberately separate
// from support mode: a hook write, a provider launch, and an exact native turn
// are all supported operations, but they do not complete at the same boundary.
type CompletionPrecision string

const (
	CompletionNone              CompletionPrecision = "none"
	CompletionRegistryRead      CompletionPrecision = "registry-read"
	CompletionRegistryCommit    CompletionPrecision = "registry-commit"
	CompletionProviderLaunch    CompletionPrecision = "provider-launch"
	CompletionExactTurn         CompletionPrecision = "exact-turn"
	CompletionExactApproval     CompletionPrecision = "exact-approval"
	CompletionExactOperation    CompletionPrecision = "exact-operation"
	CompletionPlanPreview       CompletionPrecision = "plan-preview"
	CompletionLocalConfigCommit CompletionPrecision = "local-config-commit"
	CompletionProviderSnapshot  CompletionPrecision = "provider-snapshot"
)

// AgentCapabilityCell is one and only one provider cell for an action.
type AgentCapabilityCell struct {
	Provider            ID                  `json:"provider"`
	Mode                AgentSupportMode    `json:"mode"`
	CompletionPrecision CompletionPrecision `json:"completionPrecision"`
}

// AgentAction describes one leaf action in the provider-neutral Agent
// vocabulary. Callable false reserves a future vocabulary cell without adding
// a parser node or executable placeholder.
type AgentAction struct {
	ID       string                `json:"action"`
	Group    string                `json:"group"`
	Route    string                `json:"route,omitempty"`
	Callable bool                  `json:"callable"`
	Cells    []AgentCapabilityCell `json:"-"`
}

var providerOrder = []ID{Codex, Claude, Antigravity}

func cells(mode AgentSupportMode, precision CompletionPrecision) []AgentCapabilityCell {
	out := make([]AgentCapabilityCell, 0, len(providerOrder))
	for _, provider := range AgentProviders() {
		out = append(out, AgentCapabilityCell{Provider: provider, Mode: mode, CompletionPrecision: precision})
	}
	return out
}

func codexOnly(precision CompletionPrecision) []AgentCapabilityCell {
	out := make([]AgentCapabilityCell, 0, len(providerOrder))
	for _, provider := range AgentProviders() {
		cell := AgentCapabilityCell{Provider: provider, Mode: SupportUnsupported, CompletionPrecision: CompletionNone}
		if provider == Codex {
			cell.Mode = SupportNativeExact
			cell.CompletionPrecision = precision
		}
		out = append(out, cell)
	}
	return out
}

// agentActions is the authoritative action x provider matrix. The first nine
// groups are the current public `projmux agent` groups. message and wait are
// reserved provider-neutral vocabulary only; Phase 0 intentionally gives them
// no route and no parser surface.
var agentActions = []AgentAction{
	{ID: "status.get", Group: "status", Route: "agent status", Callable: true, Cells: cells(SupportGenericRegistry, CompletionRegistryRead)},
	{ID: "status.set", Group: "status", Route: "agent status", Callable: true, Cells: cells(SupportGenericRegistry, CompletionRegistryCommit)},
	{ID: "topic.get", Group: "topic", Route: "agent topic", Callable: true, Cells: cells(SupportGenericRegistry, CompletionRegistryRead)},
	{ID: "topic.set", Group: "topic", Route: "agent topic", Callable: true, Cells: cells(SupportGenericRegistry, CompletionRegistryCommit)},
	{ID: "topic.clear", Group: "topic", Route: "agent topic", Callable: true, Cells: cells(SupportGenericRegistry, CompletionRegistryCommit)},
	{ID: "resume", Group: "resume", Route: "agent resume", Callable: true, Cells: cells(SupportProviderResume, CompletionProviderLaunch)},
	{ID: "turn.start", Group: "turn", Route: "agent turn start", Callable: true, Cells: codexOnly(CompletionExactTurn)},
	{ID: "turn.steer", Group: "turn", Route: "agent turn steer", Callable: true, Cells: codexOnly(CompletionExactTurn)},
	{ID: "turn.interrupt", Group: "turn", Route: "agent turn interrupt", Callable: true, Cells: codexOnly(CompletionExactTurn)},
	{ID: "approval.review", Group: "approval", Route: "agent approval review", Callable: true, Cells: codexOnly(CompletionExactApproval)},
	{ID: "review", Group: "review", Route: "agent review", Callable: true, Cells: codexOnly(CompletionExactTurn)},
	{ID: "integrate.install", Group: "integrate", Route: "agent integrate", Callable: true, Cells: cells(SupportProviderHook, CompletionLocalConfigCommit)},
	{ID: "integrate.remove", Group: "integrate", Route: "agent integrate", Callable: true, Cells: cells(SupportProviderHook, CompletionLocalConfigCommit)},
	{ID: "integrate.dry-run", Group: "integrate", Route: "agent integrate", Callable: true, Cells: cells(SupportProviderHook, CompletionPlanPreview)},
	{ID: "usage", Group: "usage", Route: "agent usage", Callable: true, Cells: cells(SupportReadOnlyAdapter, CompletionProviderSnapshot)},
	{ID: "app-server.upgrade.plan", Group: "app-server", Route: "agent app-server upgrade plan", Callable: true, Cells: codexOnly(CompletionPlanPreview)},
	{ID: "app-server.upgrade.apply", Group: "app-server", Route: "agent app-server upgrade apply", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "app-server.upgrade.resume", Group: "app-server", Route: "agent app-server upgrade resume", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "app-server.upgrade.abort", Group: "app-server", Route: "agent app-server upgrade abort", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "app-server.handover.plan", Group: "app-server", Route: "agent app-server handover plan", Callable: true, Cells: codexOnly(CompletionPlanPreview)},
	{ID: "app-server.handover.apply", Group: "app-server", Route: "agent app-server handover apply", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "app-server.handover.resume", Group: "app-server", Route: "agent app-server handover resume", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "app-server.handover.abort", Group: "app-server", Route: "agent app-server handover abort", Callable: true, Cells: codexOnly(CompletionExactOperation)},
	{ID: "message.send", Group: "message", Callable: false, Cells: cells(SupportUnsupported, CompletionNone)},
	{ID: "message.wait", Group: "message", Callable: false, Cells: cells(SupportUnsupported, CompletionNone)},
	{ID: "message.status", Group: "message", Callable: false, Cells: cells(SupportUnsupported, CompletionNone)},
	{ID: "wait.idle", Group: "wait", Callable: false, Cells: cells(SupportUnsupported, CompletionNone)},
}

// IntegrationTarget is a target accepted by `agent integrate`. tmux-bell is a
// provider-independent event source, so Provider is empty for that row.
type IntegrationTarget struct {
	ID       string `json:"id"`
	Provider ID     `json:"provider,omitempty"`
}

var integrationTargets = []IntegrationTarget{
	{ID: string(Codex), Provider: Codex},
	{ID: string(Claude), Provider: Claude},
	{ID: string(Antigravity), Provider: Antigravity},
	{ID: "tmux-bell"},
}

// AgentActions returns a defensive copy in stable documentation order.
func AgentActions() []AgentAction {
	out := make([]AgentAction, len(agentActions))
	for i, action := range agentActions {
		action.Cells = slices.Clone(action.Cells)
		out[i] = action
	}
	return out
}

// AgentProviders returns the closed provider dimension in projection order.
func AgentProviders() []ID { return slices.Clone(providerOrder) }

// UsageTargets returns the provider adapters plus the provider-neutral fan-out
// token accepted by `agent usage --model`.
func UsageTargets() []string {
	out := make([]string, 0, len(providerOrder)+1)
	for _, provider := range providerOrder {
		out = append(out, string(provider))
	}
	return append(out, "all")
}

// AgentGroups returns the current callable groups in public help order.
func AgentGroups() []string {
	var groups []string
	for _, action := range agentActions {
		if !action.Callable || slices.Contains(groups, action.Group) {
			continue
		}
		groups = append(groups, action.Group)
	}
	return groups
}

// LookupAgentCapability returns the unique cell for action and provider.
func LookupAgentCapability(actionID string, provider ID) (AgentAction, AgentCapabilityCell, bool) {
	actionID = strings.TrimSpace(actionID)
	for _, action := range agentActions {
		if action.ID != actionID {
			continue
		}
		for _, cell := range action.Cells {
			if cell.Provider == provider {
				action.Cells = slices.Clone(action.Cells)
				return action, cell, true
			}
		}
		return AgentAction{}, AgentCapabilityCell{}, false
	}
	return AgentAction{}, AgentCapabilityCell{}, false
}

// IntegrationTargets returns the exact accepted target inventory.
func IntegrationTargets() []IntegrationTarget { return slices.Clone(integrationTargets) }

// IntegrationCommand returns the canonical install spelling for a catalogued
// target. Empty means the target is not part of the public integration set.
func IntegrationCommand(targetID string) string {
	targetID = strings.TrimSpace(targetID)
	for _, target := range IntegrationTargets() {
		if target.ID == targetID {
			return "projmux agent integrate " + target.ID
		}
	}
	return ""
}
