package projmux

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// loadAgentSettingsRegistry and newAgentSettingsPlanner are replaced only by
// package tests.
var (
	loadAgentSettingsRegistry = resources.Load
	newAgentSettingsPlanner   = guidanceServicePlanner
)

func guidanceServicePlanner(paths config.Paths) runtime.RelaunchPromptPlanner {
	return &runtime.GuidanceService{Paths: func() (config.Paths, error) { return paths, nil }}
}

// ReadAgentSettings resolves, without admitting anything, the settings each
// selected Agent's plain `agent relaunch` would resolve: the same request the
// CLI relaunch builds for the Agent's host (its agent guidance and Project
// label link rules included) and the same resolution, so Current, New and
// Reasons equal what `agent relaunch --dry-run -o json` reports for it.
//
// One call reads the Registry once, without locking, repairing or writing it.
// It contacts no tmux server, process host or provider, writes no
// instructions snapshot, and checks no phase, interaction, conversation or
// Codex preflight: a Starting or busy Agent is resolved like any other, and a
// resolution says nothing about whether that relaunch would be admitted. The
// agent guidance is planned at most once per provider and host and the label
// link rules at most once per provider and Project; planning them writes what
// every launch plan writes, their content-addressed snapshots (and the
// guidance's one-time first-use migration).
//
// An Agent whose settings cannot be resolved now has Unavailable set rather
// than failing the call; a Registry that cannot be read is a
// registry-unavailable refusal. Results own their values; concurrent calls are
// supported.
func (c *Client) ReadAgentSettings(ctx context.Context, q core.ReadAgentSettingsRequest) (core.ReadAgentSettingsResult, error) {
	if err := ctx.Err(); err != nil {
		return core.ReadAgentSettingsResult{}, err
	}
	for _, uid := range q.AgentUIDs {
		if strings.TrimSpace(uid) == "" || strings.HasPrefix(uid, "uid:") {
			return core.ReadAgentSettingsResult{}, &Refusal{Code: CodeSelectorInvalid, message: "ReadAgentSettingsRequest.AgentUIDs must hold bare, non-empty Agent UIDs"}
		}
	}
	if c.pathsErr != nil {
		return core.ReadAgentSettingsResult{}, resourceRefusal(c.pathsErr)
	}
	reg, err := loadAgentSettingsRegistry(c.paths)
	if err != nil {
		return core.ReadAgentSettingsResult{}, resourceRefusal(err)
	}
	uids := slices.Clone(q.AgentUIDs)
	if len(uids) == 0 {
		for _, agent := range reg.Agents {
			uids = append(uids, agent.Metadata.UID)
		}
	}
	slices.Sort(uids)
	uids = slices.Compact(uids)
	paths := c.paths
	reader := agentSettingsReader{registry: reg, paths: func() (config.Paths, error) { return paths, nil }, planner: newMemoPromptPlanner(newAgentSettingsPlanner(paths))}
	out := core.ReadAgentSettingsResult{Agents: make([]core.AgentSettingsResolution, 0, len(uids))}
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return core.ReadAgentSettingsResult{}, err
		}
		out.Agents = append(out.Agents, reader.resolve(uid))
	}
	return out, nil
}

type agentSettingsReader struct {
	registry metadata.Registry
	paths    func() (config.Paths, error)
	planner  runtime.RelaunchPromptPlanner
}

// resolve is one Agent's plain relaunch settings. The refusal tokens are the
// ones the CLI relaunch refuses the same resolution with.
func (r agentSettingsReader) resolve(uid string) core.AgentSettingsResolution {
	agent, ok := r.registry.Agent(uid)
	if !ok {
		return core.AgentSettingsResolution{AgentUID: uid, Unavailable: string(CodeResourceNotFound)}
	}
	out := core.AgentSettingsResolution{AgentUID: uid, Provider: runtime.RelaunchAgentProvider(*agent), Host: runtime.RelaunchCurrentHost(r.registry, uid)}
	if out.Provider != "claude" && out.Provider != "codex" {
		return out
	}
	unavailable := func(reason string) core.AgentSettingsResolution {
		out.Unavailable = reason
		return out
	}
	window, ok := r.registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return unavailable(string(CodeRelaunchNoConversation))
	}
	project, ok := r.registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return unavailable(string(CodeRelaunchNoConversation))
	}
	target := runtime.RelaunchSettingsTarget{Provider: out.Provider, Host: out.Host, Project: *project, Annotations: agent.Metadata.Annotations}
	request, _, _ := runtime.RelaunchSettingsRequest(runtime.AgentSettingsRequest{Source: metadata.SettingSourceRelaunch}, target, r.planner)
	launch, err := runtime.ResolveAgentSettingsWithPaths(r.paths, out.Provider, agent.Metadata.Annotations, request)
	if err != nil {
		return unavailable(agentSettingsUnavailableReason(err))
	}
	// A process Claude relaunch refuses instructions it cannot read now.
	if out.Host == "process" && out.Provider != "codex" && launch.InstructionsErr != nil {
		return unavailable(string(CodeRelaunchNoConversation))
	}
	out.Layered = launch.Layered
	out.Current, out.New = publicAgentLaunchSettings(launch.Resolution.Current), publicAgentLaunchSettings(launch.Resolution.New)
	out.Reasons = append([]string{}, launch.Resolution.Reasons...)
	return out
}

// agentSettingsUnavailableReason is the refusal token the CLI relaunch
// refuses a settings resolution failure with.
func agentSettingsUnavailableReason(err error) string {
	var profileErr *runtime.RelaunchProfileError
	if errors.As(err, &profileErr) {
		return profileErr.Reason
	}
	return string(CodeRelaunchNoConversation)
}

func publicAgentLaunchSettings(s agentsettings.Settings) core.AgentLaunchSettings {
	setting := func(v agentsettings.Setting) core.AgentLaunchSetting {
		return core.AgentLaunchSetting{Value: v.Value, Source: v.Source, ProfileValue: v.ProfileValue, Override: v.Override}
	}
	return core.AgentLaunchSettings{Profile: core.AgentLaunchProfile{Name: s.Profile.Name, Digest: s.Profile.Digest, Source: s.Profile.Source}, Instructions: setting(s.Instructions), Model: setting(s.Model), Effort: setting(s.Effort)}
}

// memoPromptPlanner plans each prompt part once per read. A settings request
// carries only the parts' digests, never the recorded values a launch plan
// also holds, so one plan answers every Agent with the same provider and host
// (guidance) or provider and Project (link rules).
type memoPromptPlanner struct {
	next     runtime.RelaunchPromptPlanner
	guidance map[string]runtime.AgentGuidanceLaunch
	links    map[string]runtime.ProjectLinksLaunch
}

func newMemoPromptPlanner(next runtime.RelaunchPromptPlanner) *memoPromptPlanner {
	return &memoPromptPlanner{next: next, guidance: map[string]runtime.AgentGuidanceLaunch{}, links: map[string]runtime.ProjectLinksLaunch{}}
}

func (p *memoPromptPlanner) planGuidance(key string, plan func() runtime.AgentGuidanceLaunch) runtime.AgentGuidanceLaunch {
	if launch, ok := p.guidance[key]; ok {
		return launch
	}
	launch := plan()
	p.guidance[key] = launch
	return launch
}

func (p *memoPromptPlanner) planLinks(key string, plan func() runtime.ProjectLinksLaunch) runtime.ProjectLinksLaunch {
	if launch, ok := p.links[key]; ok {
		return launch
	}
	launch := plan()
	p.links[key] = launch
	return launch
}

func (p *memoPromptPlanner) PlanAgentGuidance(provider string, recorded map[string]string) runtime.AgentGuidanceLaunch {
	return p.planGuidance("agent\x00"+provider, func() runtime.AgentGuidanceLaunch { return p.next.PlanAgentGuidance(provider, recorded) })
}

func (p *memoPromptPlanner) PlanCodexAgentGuidance() runtime.AgentGuidanceLaunch {
	return p.planGuidance("codex", p.next.PlanCodexAgentGuidance)
}

func (p *memoPromptPlanner) PlanProcessAgentGuidance() runtime.AgentGuidanceLaunch {
	return p.planGuidance("process", p.next.PlanProcessAgentGuidance)
}

func (p *memoPromptPlanner) PlanProjectLinks(provider string, project metadata.Project, recorded map[string]string) runtime.ProjectLinksLaunch {
	return p.planLinks("agent\x00"+provider+"\x00"+project.Metadata.UID, func() runtime.ProjectLinksLaunch { return p.next.PlanProjectLinks(provider, project, recorded) })
}

func (p *memoPromptPlanner) PlanCodexProjectLinks(project metadata.Project) runtime.ProjectLinksLaunch {
	return p.planLinks("codex\x00"+project.Metadata.UID, func() runtime.ProjectLinksLaunch { return p.next.PlanCodexProjectLinks(project) })
}
