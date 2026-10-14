package runtime

import (
	"maps"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
)

func profileDigestRegistry(a map[string]string) m.Registry {
	return m.Registry{Agents: []m.Agent{{Metadata: m.ObjectMeta{UID: "agent", Annotations: maps.Clone(a)}}}}
}

func TestSameNameProfileDigestUsesResolvedLaunch(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			base := map[string]string{m.AnnotationAgentProfile: "role", m.AnnotationAgentProfileDigest: "old", m.AnnotationAgentProfileSource: m.SettingSourceRole, m.AnnotationAgentModel: "override", m.AnnotationAgentModelSource: m.SettingSourceFlag}
			layer := &agentsettings.Profile{Name: "role", Digest: "resolved"}
			launch := AgentSettingsLaunch{Layered: true, Resolution: agentsettings.Resolve(agentsettings.Input{Annotations: base, Profile: layer, InstructionsFixed: provider == "codex"})}
			if launch.Resolution.ProfileSwitched || !slices.Contains(launch.Resolution.Reasons, agentsettings.ReasonProfileChanged) {
				t.Fatal("fixture did not isolate same-name content change")
			}
			// Change the caller's current profile after resolution: neither projection
			// may stamp that later value into this launch.
			layer.Digest = "later-file"
			got := launch.LaunchAnnotations(base)
			if got[m.AnnotationAgentProfileDigest] != "resolved" {
				t.Errorf("launch digest=%q, want resolved", got[m.AnnotationAgentProfileDigest])
			}
			if base[m.AnnotationAgentProfileDigest] != "old" {
				t.Fatal("launch mutated input")
			}
			reg := profileDigestRegistry(base)
			if err := launch.Record(&reg, m.Mutator{}, "agent"); err != nil {
				t.Fatal(err)
			}
			a, _ := reg.Agent("agent")
			if a.Metadata.Annotations[m.AnnotationAgentProfileDigest] != "resolved" {
				t.Errorf("record digest=%q, want resolved", a.Metadata.Annotations[m.AnnotationAgentProfileDigest])
			}
			if a.Metadata.Annotations[m.AnnotationAgentProfileSource] != m.SettingSourceRole || a.Metadata.Annotations[m.AnnotationAgentModel] != "override" || a.Metadata.Annotations[m.AnnotationAgentModelSource] != m.SettingSourceFlag {
				t.Fatal("source/override changed")
			}
			after := agentsettings.Resolve(agentsettings.Input{Annotations: a.Metadata.Annotations, Profile: &agentsettings.Profile{Name: "role", Digest: "resolved"}})
			if slices.Contains(after.Reasons, agentsettings.ReasonProfileChanged) {
				t.Error("profile-changed repeated after record")
			}
			later := agentsettings.Resolve(agentsettings.Input{Annotations: a.Metadata.Annotations, Profile: layer})
			if !slices.Contains(later.Reasons, agentsettings.ReasonProfileChanged) {
				t.Error("later profile change hidden")
			}
		})
	}
}

func TestProfileDigestProjectionPreservesNoneUnlayeredAndSwitch(t *testing.T) {
	for _, tc := range []struct {
		name               string
		layered, switching bool
		profile            *agentsettings.Profile
	}{
		{name: "unlayered"}, {name: "none", layered: true}, {name: "switch", layered: true, switching: true, profile: &agentsettings.Profile{Name: "new", Digest: "new-digest"}}, {name: "switch-none", layered: true, switching: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := map[string]string{}
			if tc.switching {
				base = map[string]string{m.AnnotationAgentProfile: "old", m.AnnotationAgentProfileDigest: "old-digest", m.AnnotationAgentProfileSource: m.SettingSourceRole}
			}
			r := agentsettings.Resolve(agentsettings.Input{Annotations: base, Switch: tc.switching, NewProfile: tc.profile, SwitchSource: m.SettingSourceRelaunch})
			l := AgentSettingsLaunch{Layered: tc.layered, Resolution: r}
			before := maps.Clone(base)
			got := l.LaunchAnnotations(base)
			reg := profileDigestRegistry(base)
			if err := l.Record(&reg, m.Mutator{}, "agent"); err != nil {
				t.Fatal(err)
			}
			a, _ := reg.Agent("agent")
			if !maps.Equal(base, before) {
				t.Fatal("input changed")
			}
			for _, key := range []string{m.AnnotationAgentProfile, m.AnnotationAgentProfileDigest, m.AnnotationAgentProfileSource} {
				want := before[key]
				if tc.layered && tc.switching {
					switch key {
					case m.AnnotationAgentProfile:
						want = r.New.Profile.Name
					case m.AnnotationAgentProfileDigest:
						want = r.New.Profile.Digest
					case m.AnnotationAgentProfileSource:
						want = r.New.Profile.Source
					}
				}
				if got[key] != want || a.Metadata.Annotations[key] != want {
					t.Fatalf("%s projection=%q record=%q want=%q", key, got[key], a.Metadata.Annotations[key], want)
				}
			}
		})
	}
}

func TestPreparedResumeActualProfileDigestWins(t *testing.T) {
	base := map[string]string{m.AnnotationAgentProfile: "role", m.AnnotationAgentProfileDigest: "old", m.AnnotationAgentProfileSource: m.SettingSourceFlag}
	settings := AgentSettingsLaunch{Layered: true, Resolution: agentsettings.Resolve(agentsettings.Input{Annotations: base, Profile: &agentsettings.Profile{Name: "role", Digest: "resolved"}})}
	prepared := PreparedAgentResume{Settings: settings, Resumed: AgentResumeLaunch{ProfileName: "role", ProfileDigest: "actual-launch"}}
	reg := profileDigestRegistry(base)
	if err := prepared.Record(&reg, m.Mutator{}, "agent"); err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent("agent")
	if a.Metadata.Annotations[m.AnnotationAgentProfileDigest] != "actual-launch" || a.Metadata.Annotations[m.AnnotationAgentProfileSource] != m.SettingSourceFlag {
		t.Fatal("actual tmux/SDK digest priority changed")
	}
}
