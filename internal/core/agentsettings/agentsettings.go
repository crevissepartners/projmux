// Package agentsettings computes the launch settings of one existing Agent
// from its layers: the settings an Agent launches with are its overrides,
// then its profile as it is now, then nothing.
//
//	value(item) = the override, when the Agent has one for item
//	              otherwise the profile's value, read now
//	              otherwise none (the provider default, or what the
//	              conversation already has)
//	item        = instructions | model | effort
//
// Permissions are not an item: they come only from the profile, and the
// resume that applies them re-reads the profile itself.
//
// An Agent records each value in its existing annotation (persona, model,
// effort) and where it came from beside it (the <item>-source annotations).
// A source of SettingSourceProfile puts the item in the profile layer; any
// other source makes the recorded value an override. An instructions source
// without a persona is an explicit "no instructions" override.
//
// # Agents recorded before sources were
//
// An item without a source key predates the source annotations, so the
// resolver decides its layer (Epic decision O-1): the item is in the profile
// layer when its recorded value equals the profile's current value, and an
// override otherwise. An empty recorded value is a value too: when the profile
// sets the item and the Agent recorded none, the item is an override of "none"
// and nothing is passed, so the first launch after the upgrade is the launch
// the Agent had before it. An Agent without a profile has every recorded value
// as an override. The launch records SettingSourceProfile for an item it put in
// the profile layer this way, and nothing for one it kept as an unknown
// override, so the layer of that item is decided the same way next time.
//
// The package is pure: the caller reads the profile and the instructions
// content and hands them in. Resume, relaunch and any other reader call the
// same Resolve, so they cannot disagree about what an Agent would launch with.
package agentsettings

import (
	"slices"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

// Reasons a launch of the Agent would differ from the one it last recorded.
// They are stable tokens: `agent relaunch -o json` prints them in
// relaunchReasons, in this order.
const (
	// ReasonProfileChanged is a profile whose content differs from the digest
	// the Agent recorded: its permissions, at least, would be applied again.
	ReasonProfileChanged = "profile-changed"
	// ReasonInstructionsChanged is instructions under another name than the
	// Agent recorded, none included.
	ReasonInstructionsChanged = "instructions-changed"
	// ReasonInstructionsContentChanged is instructions under the recorded
	// name whose current content differs from the recorded digest.
	ReasonInstructionsContentChanged = "instructions-content-changed"
	// ReasonModelChanged is a model the launch would pass that differs from
	// the recorded one.
	ReasonModelChanged = "model-changed"
	// ReasonEffortChanged is an effort that differs from the recorded one.
	ReasonEffortChanged = "effort-changed"
)

// Profile is the current content of the profile an Agent records: its name,
// the digest of its content, and the items it sets ("" for an item it does
// not set).
type Profile struct {
	Name         string
	Digest       string
	Instructions string
	Model        string
	Effort       string
}

// Override is a value one launch is given explicitly for an item (`agent
// resume --effort`, `agent relaunch --model`), with the source it is recorded
// under. It wins over the item's recorded layer.
type Override struct {
	Value  string
	Source string
}

// Input is everything Resolve reads.
type Input struct {
	// Annotations are the Agent's own.
	Annotations map[string]string
	// Profile is the profile the Agent records, read now, or nil when the
	// Agent records none.
	Profile *Profile
	// InstructionsDigest returns the digest of the current content of the
	// named instructions, and false when they cannot be read now. It is asked
	// only about the instructions the launch would use.
	InstructionsDigest func(name string) (string, bool)
	// InstructionsFixed is a provider that cannot change a conversation's
	// instructions on resume (Codex): the launch keeps the recorded
	// instructions and InstructionsNotApplied says what it could not apply.
	InstructionsFixed bool
	// Model and Effort are the explicit overrides of this launch, if any.
	Model  *Override
	Effort *Override
}

// Setting is one item on one side of a launch: the value, the source it is
// recorded under ("" when not known), the profile's current value for it, and
// whether the value overrides the profile.
type Setting struct {
	Value        string `json:"value"`
	Source       string `json:"source"`
	ProfileValue string `json:"profileValue"`
	Override     bool   `json:"override"`
}

// ProfileLayer is the profile on one side of a launch: its name, the digest
// of its content, and how the Agent was given it.
type ProfileLayer struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Source string `json:"source"`
}

// Settings are the launch settings on one side of a launch.
type Settings struct {
	Profile      ProfileLayer `json:"profile"`
	Instructions Setting      `json:"instructions"`
	Model        Setting      `json:"model"`
	Effort       Setting      `json:"effort"`
}

// Resolution is what one launch of an Agent would run with, next to what the
// Agent recorded.
type Resolution struct {
	// Current is what the Agent recorded: the values its last launch ran
	// with and their sources.
	Current Settings
	// New is what the next launch runs with, and the sources it records.
	New Settings
	// InstructionsDigest is the digest of the content New.Instructions
	// launches with ("" without instructions). It differs from the recorded
	// digest exactly when the launch must pass a new snapshot.
	InstructionsDigest string
	// InstructionsUnavailable names instructions the layers ask for whose
	// content cannot be read now. The launch keeps the recorded
	// instructions.
	InstructionsUnavailable string
	// InstructionsNotApplied is set when the layers ask for other
	// instructions than the recorded ones and Input.InstructionsFixed kept
	// them; New.Instructions is the recorded instructions.
	InstructionsNotApplied bool
	// WantedInstructions are the instructions the layers ask for ("" for
	// none), whether or not the launch could use them.
	WantedInstructions string
	// PassModel reports whether the launch passes New.Model.Value: an
	// explicit override always is, and a layer value only when it differs
	// from the recorded model, so an unchanged model leaves the provider to
	// restore the conversation's own (a `/model` switch included).
	PassModel bool
	// Reasons are why the launch differs from the recorded one, in the order
	// the Reason constants are declared; empty when it would not.
	Reasons []string
}

// InstructionsChanged reports whether the launch passes other instructions
// content than the Agent recorded: another name, none, or new content.
func (r Resolution) InstructionsChanged() bool {
	return slices.Contains(r.Reasons, ReasonInstructionsChanged) || slices.Contains(r.Reasons, ReasonInstructionsContentChanged)
}

// Resolve computes the launch settings of one Agent from its layers.
func Resolve(in Input) Resolution {
	a := in.Annotations
	var profileValues Profile
	if in.Profile != nil {
		profileValues = *in.Profile
	}
	var out Resolution
	out.Current.Profile = ProfileLayer{
		Name:   a[metadata.AnnotationAgentProfile],
		Digest: a[metadata.AnnotationAgentProfileDigest],
		Source: a[metadata.AnnotationAgentProfileSource],
	}
	out.New.Profile = out.Current.Profile
	if in.Profile != nil {
		out.New.Profile.Name, out.New.Profile.Digest = in.Profile.Name, in.Profile.Digest
	}

	out.Current.Instructions, out.New.Instructions = resolveItem(in.Profile != nil, a,
		metadata.AnnotationAgentPersona, metadata.AnnotationAgentInstructionsSource, profileValues.Instructions, nil)
	out.Current.Model, out.New.Model = resolveItem(in.Profile != nil, a,
		metadata.AnnotationAgentModel, metadata.AnnotationAgentModelSource, profileValues.Model, in.Model)
	out.Current.Effort, out.New.Effort = resolveItem(in.Profile != nil, a,
		metadata.AnnotationAgentEffort, metadata.AnnotationAgentEffortSource, profileValues.Effort, in.Effort)

	if in.Profile != nil && in.Profile.Digest != out.Current.Profile.Digest {
		out.Reasons = append(out.Reasons, ReasonProfileChanged)
	}
	out.resolveInstructions(in)
	recordedModel := out.Current.Model.Value
	out.PassModel = in.Model != nil || (out.New.Model.Value != "" && out.New.Model.Value != recordedModel)
	if out.PassModel && out.New.Model.Value != recordedModel {
		out.Reasons = append(out.Reasons, ReasonModelChanged)
	}
	if out.New.Effort.Value != out.Current.Effort.Value {
		out.Reasons = append(out.Reasons, ReasonEffortChanged)
	}
	return out
}

// resolveInstructions fixes the instructions content the launch uses and
// adds their reasons.
func (out *Resolution) resolveInstructions(in Input) {
	recordedName := in.Annotations[metadata.AnnotationAgentPersona]
	recordedDigest := in.Annotations[metadata.AnnotationAgentPersonaDigest]
	name := out.New.Instructions.Value
	out.WantedInstructions = name
	keep := func() {
		out.New.Instructions.Value, out.InstructionsDigest = recordedName, recordedDigest
		if out.New.Instructions.Source == metadata.SettingSourceProfile && out.Current.Instructions.Source != metadata.SettingSourceProfile {
			// The recorded instructions stay; so does their unknown source.
			out.New.Instructions.Source = out.Current.Instructions.Source
		}
	}
	digest := ""
	if name != "" {
		var ok bool
		if in.InstructionsDigest != nil {
			digest, ok = in.InstructionsDigest(name)
		}
		if !ok {
			out.InstructionsUnavailable = name
			keep()
			return
		}
	}
	var reason string
	switch {
	case name != recordedName:
		reason = ReasonInstructionsChanged
	case digest != recordedDigest:
		reason = ReasonInstructionsContentChanged
	}
	if reason != "" && in.InstructionsFixed {
		out.InstructionsNotApplied = true
		keep()
		return
	}
	out.InstructionsDigest = digest
	if reason != "" {
		out.Reasons = append(out.Reasons, reason)
	}
}

// resolveItem is the layer rule of one item: the recorded setting and the one
// the next launch uses.
func resolveItem(hasProfile bool, a map[string]string, valueKey, sourceKey, profileValue string, override *Override) (Setting, Setting) {
	recorded := a[valueKey]
	source, hasSource := a[sourceKey]
	current := Setting{Value: recorded, Source: source, ProfileValue: profileValue}
	switch {
	case !hasProfile:
		// Without a profile every recorded value is the Agent's own.
		current.Override = true
	case hasSource:
		current.Override = source != metadata.SettingSourceProfile
	default:
		// No source recorded: the value predates sources (O-1).
		current.Override = recorded != profileValue
	}
	next := current
	switch {
	case override != nil:
		next.Value, next.Source, next.Override = override.Value, override.Source, true
	case !current.Override:
		next.Value, next.Source = profileValue, metadata.SettingSourceProfile
	}
	return current, next
}
