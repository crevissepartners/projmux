package agentsettings

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

const (
	digestA = "sha256:aaaa"
	digestB = "sha256:bbbb"
)

// digests is an InstructionsDigest over a fixed set of readable instructions.
func digests(known map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		digest, ok := known[name]
		return digest, ok
	}
}

func roleProfile(digest string) *Profile {
	return &Profile{Name: "role", Digest: digest, Instructions: "lead", Model: "opus", Effort: "high"}
}

// createdFromProfile are the annotations of an Agent a create gave the whole
// role profile, before sources were recorded.
func createdFromProfile() map[string]string {
	return map[string]string{
		metadata.AnnotationAgentProfile:       "role",
		metadata.AnnotationAgentProfileDigest: "sha256:p1",
		metadata.AnnotationAgentPersona:       "lead",
		metadata.AnnotationAgentPersonaDigest: digestA,
		metadata.AnnotationAgentModel:         "opus",
		metadata.AnnotationAgentEffort:        "high",
	}
}

func with(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, base)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(out, kv[i])
			continue
		}
		out[kv[i]] = kv[i+1]
	}
	return out
}

// TestAnAgentRecordedBeforeSourcesLaunchesWhatItLaunchedBefore is O-1: right
// after the upgrade, every Agent -- with a profile or without, values equal to
// the profile or not -- launches the values it recorded, so its argv is the
// argv it had. Only the layers differ.
func TestAnAgentRecordedBeforeSourcesLaunchesWhatItLaunchedBefore(t *testing.T) {
	for _, test := range []struct {
		name        string
		annotations map[string]string
		profile     *Profile
		wantLayer   map[string]bool // item -> override
	}{
		{name: "whole profile", annotations: createdFromProfile(), profile: roleProfile("sha256:p1"),
			wantLayer: map[string]bool{"instructions": false, "model": false, "effort": false}},
		{name: "effort flag over the profile", annotations: with(createdFromProfile(), metadata.AnnotationAgentEffort, "low"), profile: roleProfile("sha256:p1"),
			wantLayer: map[string]bool{"instructions": false, "model": false, "effort": true}},
		{name: "profile sets a model the Agent never recorded", annotations: with(createdFromProfile(), metadata.AnnotationAgentModel, ""), profile: roleProfile("sha256:p1"),
			wantLayer: map[string]bool{"instructions": false, "model": true, "effort": false}},
		{name: "no profile", annotations: with(createdFromProfile(), metadata.AnnotationAgentProfile, "", metadata.AnnotationAgentProfileDigest, ""),
			wantLayer: map[string]bool{"instructions": true, "model": true, "effort": true}},
		{name: "nothing recorded", annotations: nil,
			wantLayer: map[string]bool{"instructions": true, "model": true, "effort": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Resolve(Input{Annotations: test.annotations, Profile: test.profile, InstructionsDigest: digests(map[string]string{"lead": digestA})})
			if len(got.Reasons) != 0 || got.PassModel {
				t.Fatalf("reasons=%v passModel=%t, want a launch identical to the recorded one", got.Reasons, got.PassModel)
			}
			for item, pair := range map[string][2]Setting{
				"instructions": {got.Current.Instructions, got.New.Instructions},
				"model":        {got.Current.Model, got.New.Model},
				"effort":       {got.Current.Effort, got.New.Effort},
			} {
				current, next := pair[0], pair[1]
				if next.Value != current.Value {
					t.Fatalf("%s: new value %q, want the recorded %q", item, next.Value, current.Value)
				}
				if current.Override != test.wantLayer[item] || next.Override != test.wantLayer[item] {
					t.Fatalf("%s: override current=%t new=%t, want %t", item, current.Override, next.Override, test.wantLayer[item])
				}
				// A profile-layer item records the profile source; an unknown
				// override records none (T2-0 ①).
				wantSource := ""
				if !test.wantLayer[item] {
					wantSource = metadata.SettingSourceProfile
				}
				if current.Source != "" || next.Source != wantSource {
					t.Fatalf("%s: sources current=%q new=%q, want \"\" and %q", item, current.Source, next.Source, wantSource)
				}
			}
			if got.InstructionsDigest != test.annotations[metadata.AnnotationAgentPersonaDigest] {
				t.Fatalf("instructions digest %q, want the recorded one", got.InstructionsDigest)
			}
		})
	}
}

// TestAnEditedProfileReachesOnlyTheItemsItsAgentDoesNotOverride is C-2: the
// profile's current effort, instructions name and model reach the items in
// its layer, and an override stays as it is.
func TestAnEditedProfileReachesOnlyTheItemsItsAgentDoesNotOverride(t *testing.T) {
	annotations := with(createdFromProfile(),
		metadata.AnnotationAgentProfileSource, metadata.SettingSourceRole,
		metadata.AnnotationAgentInstructionsSource, metadata.SettingSourceProfile,
		metadata.AnnotationAgentModelSource, metadata.SettingSourceProfile,
		metadata.AnnotationAgentEffortSource, metadata.SettingSourceFlag,
		metadata.AnnotationAgentEffort, "low")
	edited := &Profile{Name: "role", Digest: "sha256:p2", Instructions: "lead-v2", Model: "sonnet", Effort: "max"}
	got := Resolve(Input{Annotations: annotations, Profile: edited, InstructionsDigest: digests(map[string]string{"lead-v2": digestB})})

	if want := []string{ReasonProfileChanged, ReasonInstructionsChanged, ReasonModelChanged}; !slices.Equal(got.Reasons, want) {
		t.Fatalf("reasons = %v, want %v", got.Reasons, want)
	}
	if got.New.Instructions != (Setting{Value: "lead-v2", Source: metadata.SettingSourceProfile, ProfileValue: "lead-v2"}) || got.InstructionsDigest != digestB {
		t.Fatalf("instructions = %+v digest %q", got.New.Instructions, got.InstructionsDigest)
	}
	if !got.PassModel || got.New.Model != (Setting{Value: "sonnet", Source: metadata.SettingSourceProfile, ProfileValue: "sonnet"}) {
		t.Fatalf("model = %+v pass=%t, want the profile's sonnet passed", got.New.Model, got.PassModel)
	}
	if got.New.Effort != (Setting{Value: "low", Source: metadata.SettingSourceFlag, ProfileValue: "max", Override: true}) {
		t.Fatalf("effort = %+v, want the flag override kept", got.New.Effort)
	}
	if got.New.Profile != (ProfileLayer{Name: "role", Digest: "sha256:p2", Source: metadata.SettingSourceRole}) ||
		got.Current.Profile != (ProfileLayer{Name: "role", Digest: "sha256:p1", Source: metadata.SettingSourceRole}) {
		t.Fatalf("profile layers current=%+v new=%+v", got.Current.Profile, got.New.Profile)
	}
}

// TestTheModelIsPassedOnlyWhenItDiffersFromTheRecordedOne is U1 (a).
func TestTheModelIsPassedOnlyWhenItDiffersFromTheRecordedOne(t *testing.T) {
	profiled := with(createdFromProfile(), metadata.AnnotationAgentModelSource, metadata.SettingSourceProfile)
	for _, test := range []struct {
		name     string
		model    string
		override *Override
		want     bool
		reason   bool
	}{
		{name: "same model", model: "opus"},
		{name: "profile has no model", model: ""},
		{name: "profile model changed", model: "sonnet", want: true, reason: true},
		{name: "explicit override of the same model", model: "opus", override: &Override{Value: "opus", Source: metadata.SettingSourceRelaunch}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := roleProfile("sha256:p1")
			p.Model = test.model
			got := Resolve(Input{Annotations: profiled, Profile: p, Model: test.override, InstructionsDigest: digests(map[string]string{"lead": digestA})})
			if got.PassModel != test.want || slices.Contains(got.Reasons, ReasonModelChanged) != test.reason {
				t.Fatalf("passModel=%t reasons=%v, want pass=%t reason=%t", got.PassModel, got.Reasons, test.want, test.reason)
			}
		})
	}
}

// TestInstructionsContentIsComparedByDigest is U2 (a) and T2-0 ②.
func TestInstructionsContentIsComparedByDigest(t *testing.T) {
	attached := with(createdFromProfile(), metadata.AnnotationAgentInstructionsSource, metadata.SettingSourceAttach)
	for _, test := range []struct {
		name        string
		annotations map[string]string
		known       map[string]string
		fixed       bool
		reason      string
		digest      string
		unavailable string
		notApplied  bool
	}{
		{name: "unchanged", annotations: attached, known: map[string]string{"lead": digestA}, digest: digestA},
		{name: "content edited", annotations: attached, known: map[string]string{"lead": digestB}, reason: ReasonInstructionsContentChanged, digest: digestB},
		{name: "file gone keeps the recorded snapshot", annotations: attached, known: nil, digest: digestA, unavailable: "lead"},
		{name: "codex keeps its thread instructions", annotations: attached, known: map[string]string{"lead": digestB}, fixed: true, digest: digestA, notApplied: true},
		{name: "explicit none", annotations: with(attached, metadata.AnnotationAgentPersona, "", metadata.AnnotationAgentPersonaDigest, ""), known: map[string]string{"lead": digestA}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Resolve(Input{Annotations: test.annotations, Profile: roleProfile("sha256:p1"), InstructionsDigest: digests(test.known), InstructionsFixed: test.fixed})
			var want []string
			if test.reason != "" {
				want = []string{test.reason}
			}
			if !slices.Equal(got.Reasons, want) || got.InstructionsDigest != test.digest || got.InstructionsUnavailable != test.unavailable || got.InstructionsNotApplied != test.notApplied {
				t.Fatalf("got reasons=%v digest=%q unavailable=%q notApplied=%t", got.Reasons, got.InstructionsDigest, got.InstructionsUnavailable, got.InstructionsNotApplied)
			}
			if got.New.Instructions.Source != metadata.SettingSourceAttach || !got.New.Instructions.Override {
				t.Fatalf("an attached override became %+v", got.New.Instructions)
			}
		})
	}
}

// TestAProfileWithoutAnItemDropsTheItemFromItsLayer covers a profile that
// stops setting an item its Agent follows: the effort is no longer passed and
// the source stays, so the Agent keeps following the profile.
func TestAProfileWithoutAnItemDropsTheItemFromItsLayer(t *testing.T) {
	annotations := with(createdFromProfile(), metadata.AnnotationAgentEffortSource, metadata.SettingSourceProfile)
	p := roleProfile("sha256:p1")
	p.Effort = ""
	got := Resolve(Input{Annotations: annotations, Profile: p, InstructionsDigest: digests(map[string]string{"lead": digestA})})
	if got.New.Effort != (Setting{Source: metadata.SettingSourceProfile}) || !slices.Equal(got.Reasons, []string{ReasonEffortChanged}) {
		t.Fatalf("effort = %+v reasons=%v", got.New.Effort, got.Reasons)
	}
}

// TestSettingsJSONIsTheShapeRelaunchPrints pins the field names WEB reads.
func TestSettingsJSONIsTheShapeRelaunchPrints(t *testing.T) {
	got, err := json.Marshal(Settings{
		Profile: ProfileLayer{Name: "role", Digest: "sha256:p1", Source: "role"},
		Effort:  Setting{Value: "low", Source: "flag", ProfileValue: "high", Override: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"profile":{"name":"role","digest":"sha256:p1","source":"role"},` +
		`"instructions":{"value":"","source":"","profileValue":"","override":false},` +
		`"model":{"value":"","source":"","profileValue":"","override":false},` +
		`"effort":{"value":"low","source":"flag","profileValue":"high","override":true}}`
	if string(got) != want {
		t.Fatalf("settings JSON = %s\nwant %s", got, want)
	}
}
