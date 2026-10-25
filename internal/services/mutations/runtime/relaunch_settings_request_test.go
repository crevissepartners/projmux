package runtime

import (
	"slices"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// recordingPromptPlanner answers every part with an active launch whose digest
// names the method that planned it, and records the calls.
type recordingPromptPlanner struct{ calls []string }

func (p *recordingPromptPlanner) guidance(name string) AgentGuidanceLaunch {
	p.calls = append(p.calls, name)
	return AgentGuidanceLaunch{Active: true, Digest: name}
}

func (p *recordingPromptPlanner) links(name string) ProjectLinksLaunch {
	p.calls = append(p.calls, name)
	return ProjectLinksLaunch{Active: true, Digest: name, Project: ProjectGuidanceLaunch{Active: true, Digest: name + "-project"}}
}

func (p *recordingPromptPlanner) PlanAgentGuidance(string, map[string]string) AgentGuidanceLaunch {
	return p.guidance("agent-guidance")
}
func (p *recordingPromptPlanner) PlanProjectLinks(string, coremetadata.Project, map[string]string) ProjectLinksLaunch {
	return p.links("agent-links")
}
func (p *recordingPromptPlanner) PlanCodexAgentGuidance() AgentGuidanceLaunch {
	return p.guidance("codex-guidance")
}
func (p *recordingPromptPlanner) PlanCodexProjectLinks(coremetadata.Project) ProjectLinksLaunch {
	return p.links("codex-links")
}
func (p *recordingPromptPlanner) PlanProcessAgentGuidance() AgentGuidanceLaunch {
	return p.guidance("process-guidance")
}

func TestRelaunchSettingsRequestPassesTheHostsPromptParts(t *testing.T) {
	replyOnly := map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn, coremetadata.AnnotationAgentGuidanceDigest: "recorded"}
	for _, tc := range []struct {
		name, provider, host string
		annotations          map[string]string
		calls                []string
	}{
		{"tmux claude", "claude", "tmux", nil, []string{"agent-guidance", "agent-links"}},
		{"tmux reply-only claude", "claude", "tmux", replyOnly, nil},
		{"tmux codex", "codex", "tmux", nil, []string{"codex-guidance", "codex-links"}},
		{"process claude", "claude", "process", replyOnly, []string{"process-guidance", "agent-links"}},
		{"process codex", "codex", "process", nil, []string{"process-guidance", "codex-links"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := &recordingPromptPlanner{}
			project := coremetadata.Project{Metadata: coremetadata.ObjectMeta{UID: "proj-fixture"}}
			base := AgentSettingsRequest{Model: "sonnet", Source: coremetadata.SettingSourceRelaunch}
			q, guidance, links := RelaunchSettingsRequest(base, RelaunchSettingsTarget{Provider: tc.provider, Host: tc.host, Project: project, Annotations: tc.annotations}, planner)
			if !slices.Equal(planner.calls, tc.calls) {
				t.Fatalf("planned %v, want %v", planner.calls, tc.calls)
			}
			if q.Model != "sonnet" || q.Source != coremetadata.SettingSourceRelaunch || q.ProjectUID != "proj-fixture" || q.ManagedCodexBase != (tc.provider == "codex") {
				t.Fatalf("request %+v", q)
			}
			if tc.calls == nil {
				if q.Guidance != nil || q.LinkRules != nil || q.ProjectGuidance != nil || guidance.Active || links.Active {
					t.Fatalf("reply-only passed prompt parts: %+v", q)
				}
				return
			}
			if q.Guidance == nil || *q.Guidance != tc.calls[0] || q.LinkRules == nil || *q.LinkRules != tc.calls[1] || q.ProjectGuidance == nil || *q.ProjectGuidance != tc.calls[1]+"-project" {
				t.Fatalf("request digests %+v", q)
			}
			if tc.host == "process" && guidance.Recorded != tc.annotations[coremetadata.AnnotationAgentGuidanceDigest] {
				t.Fatalf("process guidance recorded %q", guidance.Recorded)
			}
		})
	}
}

func TestRelaunchAgentProviderAndCurrentHost(t *testing.T) {
	agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-a"}, Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{Provider: "codex"}}}
	if got := RelaunchAgentProvider(agent); got != "codex" {
		t.Fatalf("session provider %q", got)
	}
	agent.Spec.Provider = "claude"
	if got := RelaunchAgentProvider(agent); got != "claude" {
		t.Fatalf("declared provider %q", got)
	}
	owner := &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agent-a"}
	pane := func(uid string, kind coremetadata.RuntimeKind) coremetadata.Pane {
		return coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: uid, OwnerRef: owner}, Spec: coremetadata.PaneSpec{Runtime: coremetadata.PaneRuntimeSpec{Kind: kind}}}
	}
	for _, tc := range []struct {
		panes []coremetadata.Pane
		want  string
	}{
		{nil, "tmux"},
		{[]coremetadata.Pane{pane("pane-t", coremetadata.RuntimeTmux)}, "tmux"},
		{[]coremetadata.Pane{pane("pane-p", coremetadata.RuntimeProcess)}, "process"},
		{[]coremetadata.Pane{pane("pane-p", coremetadata.RuntimeProcess), pane("pane-q", coremetadata.RuntimeProcess)}, "tmux"},
	} {
		if got := RelaunchCurrentHost(coremetadata.Registry{Panes: tc.panes}, "agent-a"); got != tc.want {
			t.Fatalf("%d panes: host %q want %q", len(tc.panes), got, tc.want)
		}
	}
}
