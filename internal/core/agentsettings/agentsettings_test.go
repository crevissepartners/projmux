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

// overridden is an Agent on the role profile that overrides its model and
// effort and follows its instructions.
func overridden() map[string]string {
	return with(createdFromProfile(),
		metadata.AnnotationAgentProfileSource, metadata.SettingSourceRole,
		metadata.AnnotationAgentInstructionsSource, metadata.SettingSourceProfile,
		metadata.AnnotationAgentModel, "haiku",
		metadata.AnnotationAgentModelSource, metadata.SettingSourceFlag,
		metadata.AnnotationAgentEffort, "low",
		metadata.AnnotationAgentEffortSource, metadata.SettingSourceRelaunch)
}

// TestASwitchClearsEveryOverrideAndTakesTheNewProfile is the profile switch of
// Task 3: every override is dropped, each item takes the new profile's value,
// and only the overrides given with the switch are overrides again.
func TestASwitchClearsEveryOverrideAndTakesTheNewProfile(t *testing.T) {
	review := &Profile{Name: "review", Digest: "sha256:r1", Instructions: "reviewer", Model: "sonnet"}
	got := Resolve(Input{
		Annotations: overridden(), Profile: roleProfile("sha256:p1"),
		InstructionsDigest: digests(map[string]string{"lead": digestA, "reviewer": digestB}),
		Switch:             true, NewProfile: review, SwitchSource: metadata.SettingSourceRelaunch,
		Effort: &Override{Value: "max", Source: metadata.SettingSourceRelaunch},
	})
	if !got.ProfileSwitched || got.New.Profile != (ProfileLayer{Name: "review", Digest: "sha256:r1", Source: metadata.SettingSourceRelaunch}) {
		t.Fatalf("new profile = %+v switched=%t, want review from relaunch", got.New.Profile, got.ProfileSwitched)
	}
	want := Settings{
		Profile:      got.New.Profile,
		Instructions: Setting{Value: "reviewer", Source: metadata.SettingSourceProfile, ProfileValue: "reviewer"},
		Model:        Setting{Value: "sonnet", Source: metadata.SettingSourceProfile, ProfileValue: "sonnet"},
		Effort:       Setting{Value: "max", Source: metadata.SettingSourceRelaunch, Override: true},
	}
	if got.New != want {
		t.Fatalf("new settings =\n%+v\nwant\n%+v", got.New, want)
	}
	if got.Current.Model != (Setting{Value: "haiku", Source: metadata.SettingSourceFlag, ProfileValue: "opus", Override: true}) {
		t.Fatalf("current model = %+v, want the recorded override over the old profile", got.Current.Model)
	}
	wantReasons := []string{ReasonProfileChanged, ReasonInstructionsChanged, ReasonModelChanged, ReasonEffortChanged}
	if !slices.Equal(got.Reasons, wantReasons) || !got.PassModel || got.InstructionsDigest != digestB {
		t.Fatalf("reasons %v passModel %t digest %q, want %v true %q", got.Reasons, got.PassModel, got.InstructionsDigest, wantReasons, digestB)
	}

	// A profile the switch cannot read the old one of still switches.
	got = Resolve(Input{Annotations: overridden(), InstructionsDigest: digests(map[string]string{"reviewer": digestB}),
		Switch: true, NewProfile: review, SwitchSource: metadata.SettingSourceRelaunch})
	if got.New.Effort != (Setting{Source: metadata.SettingSourceProfile}) || got.Current.Effort.ProfileValue != "" {
		t.Fatalf("switch from an unreadable profile: new effort %+v current %+v", got.New.Effort, got.Current.Effort)
	}
}

// TestASwitchToTheRecordedProfileKeepsTheOverrides pins that naming the
// profile the Agent records again is no switch: its overrides stay.
func TestASwitchToTheRecordedProfileKeepsTheOverrides(t *testing.T) {
	plain := Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"), InstructionsDigest: digests(map[string]string{"lead": digestA})})
	same := Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"), InstructionsDigest: digests(map[string]string{"lead": digestA}),
		Switch: true, NewProfile: roleProfile("sha256:p1"), SwitchSource: metadata.SettingSourceRelaunch})
	if same.ProfileSwitched || same.New != plain.New || !slices.Equal(same.Reasons, plain.Reasons) || same.LayersChanged() {
		t.Fatalf("same-name switch = %+v, want the plain resolution %+v", same, plain)
	}
}

// TestASwitchToNoProfileLeavesNoLayer is `--profile none`: the profile and
// every override go, and each item has no value and no source.
func TestASwitchToNoProfileLeavesNoLayer(t *testing.T) {
	got := Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"),
		InstructionsDigest: digests(map[string]string{"lead": digestA}), Switch: true, SwitchSource: metadata.SettingSourceRelaunch})
	none := Setting{Override: true}
	if !got.ProfileSwitched || got.New != (Settings{Instructions: none, Model: none, Effort: none}) {
		t.Fatalf("new settings = %+v, want no profile and no values", got.New)
	}
	if want := []string{ReasonProfileChanged, ReasonInstructionsChanged, ReasonEffortChanged}; !slices.Equal(got.Reasons, want) || got.PassModel {
		t.Fatalf("reasons %v passModel %t, want %v and no model passed", got.Reasons, got.PassModel, want)
	}
}

// TestAResetPutsOnlyItsItemsBackInTheProfileLayer is `--reset`: the named
// items follow the profile again, and the rest keep their overrides. An item
// the profile does not set is left with no value, and a model left without
// one is not passed.
func TestAResetPutsOnlyItsItemsBackInTheProfileLayer(t *testing.T) {
	known := digests(map[string]string{"lead": digestA})
	got := Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"), InstructionsDigest: known, Reset: []string{ItemEffort}})
	if got.New.Effort != (Setting{Value: "high", Source: metadata.SettingSourceProfile, ProfileValue: "high"}) || got.New.Model != got.Current.Model {
		t.Fatalf("reset effort: effort %+v model %+v, want the profile's effort and the model override kept", got.New.Effort, got.New.Model)
	}
	if !slices.Equal(got.Reasons, []string{ReasonEffortChanged}) || got.PassModel {
		t.Fatalf("reset effort reasons %v passModel %t", got.Reasons, got.PassModel)
	}

	noModel := &Profile{Name: "role", Digest: "sha256:p1", Instructions: "lead", Effort: "high"}
	got = Resolve(Input{Annotations: overridden(), Profile: noModel, InstructionsDigest: known, Reset: []string{ItemModel}})
	if got.New.Model != (Setting{Source: metadata.SettingSourceProfile}) || got.PassModel || len(got.Reasons) != 0 || !got.LayersChanged() {
		t.Fatalf("reset model to a profile without one: %+v passModel %t reasons %v", got.New.Model, got.PassModel, got.Reasons)
	}

	got = Resolve(Input{Annotations: with(overridden(), metadata.AnnotationAgentProfile, "", metadata.AnnotationAgentProfileDigest, ""),
		InstructionsDigest: known, Reset: Items()})
	if got.New.Effort != (Setting{Override: true}) || got.New.Instructions != (Setting{Override: true}) {
		t.Fatalf("reset without a profile: %+v", got.New)
	}
}

// TestAnInstructionsOverrideOfNoneIsAnExplicitNone pins `--instructions none`
// and a detach: no instructions, recorded as an override.
func TestAnInstructionsOverrideOfNoneIsAnExplicitNone(t *testing.T) {
	got := Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"), InstructionsDigest: digests(map[string]string{"lead": digestA}),
		Instructions: &Override{Source: metadata.SettingSourceRelaunch}})
	if got.New.Instructions != (Setting{Source: metadata.SettingSourceRelaunch, ProfileValue: "lead", Override: true}) || !got.InstructionsChanged() || got.InstructionsDigest != "" {
		t.Fatalf("instructions none = %+v digest %q", got.New.Instructions, got.InstructionsDigest)
	}
	// Codex keeps its thread's instructions and says it did not apply them.
	got = Resolve(Input{Annotations: overridden(), Profile: roleProfile("sha256:p1"), InstructionsDigest: digests(map[string]string{"lead": digestA}),
		InstructionsFixed: true, Instructions: &Override{Source: metadata.SettingSourceRelaunch}})
	if !got.InstructionsNotApplied || got.New.Instructions.Value != "lead" || got.InstructionsChanged() {
		t.Fatalf("codex instructions none = %+v notApplied %t", got.New.Instructions, got.InstructionsNotApplied)
	}
}

// TestGuidanceAndLinkRulesChangesAreReasonsAfterTheItems is Epic decision
// T4-0: the agent guidance and the Project's label link rules the launch
// passes are compared with the digests the Agent recorded, after the item
// reasons and in this order. A part the launch does not pass (nil) gives no
// reason, whatever the Agent recorded; turning a part off ("") is a change
// from a recorded digest. Neither is an item: the settings are the same.
func TestGuidanceAndLinkRulesChangesAreReasonsAfterTheItems(t *testing.T) {
	recorded := with(createdFromProfile(),
		metadata.AnnotationAgentGuidanceDigest, "sha256:g1",
		metadata.AnnotationAgentProjectLinkRulesDigest, "sha256:l1")
	str := func(s string) *string { return &s }
	for _, test := range []struct {
		name                string
		annotations         map[string]string
		guidance, linkRules *string
		effort              string
		want                []string
	}{
		{name: "both recorded", annotations: recorded, guidance: str("sha256:g1"), linkRules: str("sha256:l1")},
		{name: "not passed", annotations: recorded},
		{name: "guidance edited", annotations: recorded, guidance: str("sha256:g2"), linkRules: str("sha256:l1"),
			want: []string{ReasonGuidanceChanged}},
		{name: "guidance turned off", annotations: recorded, guidance: str(""), linkRules: str("sha256:l1"),
			want: []string{ReasonGuidanceChanged}},
		{name: "link rules added", annotations: createdFromProfile(), linkRules: str("sha256:l1"),
			want: []string{ReasonLinkRulesChanged}},
		{name: "none recorded, none passed", annotations: createdFromProfile(), guidance: str(""), linkRules: str("")},
		{name: "after the items", annotations: recorded, guidance: str("sha256:g2"), linkRules: str("sha256:l2"), effort: "low",
			want: []string{ReasonEffortChanged, ReasonGuidanceChanged, ReasonLinkRulesChanged}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := Input{Annotations: test.annotations, Profile: roleProfile("sha256:p1"),
				InstructionsDigest: digests(map[string]string{"lead": digestA}), Guidance: test.guidance, LinkRules: test.linkRules}
			if test.effort != "" {
				in.Effort = &Override{Value: test.effort, Source: metadata.SettingSourceRelaunch}
			}
			got := Resolve(in)
			if !slices.Equal(got.Reasons, test.want) {
				t.Fatalf("reasons = %v, want %v", got.Reasons, test.want)
			}
			in.Guidance, in.LinkRules = nil, nil
			if without := Resolve(in); without.New != got.New || without.Current != got.Current || without.PassModel != got.PassModel {
				t.Fatalf("the prompt parts changed the settings: %+v, want %+v", got.New, without.New)
			}
		})
	}
}
