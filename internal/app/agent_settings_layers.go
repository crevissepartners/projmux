package app

import (
	"fmt"
	"maps"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// agentSettingsLaunch is the layered settings one `agent resume` or `agent
// relaunch` launches an existing Agent with: the resolution, and the snapshot
// of the new instructions content when the launch passes one. The zero value
// means the launch is not layered and reads the Agent's annotations as they
// are.
type agentSettingsLaunch struct {
	layered    bool
	resolution agentsettings.Resolution
	// snapshotErr is why the new instructions snapshot could not be written.
	snapshotErr error
}

// agentSettingsResolver is the optional seam that resolves an Agent's layered
// settings from the profile and instructions stores of its home. The Claude
// resume launcher implements it, next to the profile permissions it re-reads
// from the same home; a launcher without it launches the recorded values.
type agentSettingsResolver interface {
	ResolveAgentSettings(provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error)
}

var _ agentSettingsResolver = (*aiCommand)(nil)

// ResolveAgentSettings is resolveAgentSettings over the resume seam's home.
func (c *aiCommand) ResolveAgentSettings(provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error) {
	return resolveAgentSettings(c.homeDir, c.lookupEnv, provider, annotations, model, effort, source)
}

// resolveSettings resolves the layered settings of one rebind: a Codex Agent
// from the create command's home, where its profile policy is re-read too
// (codexResumeProfile), and any other Agent through the resume launcher.
func (r *agentRebinder) resolveSettings(provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error) {
	if provider == aiModeCodex {
		return resolveAgentSettings(r.create.homeDir, r.create.lookupEnv, provider, annotations, model, effort, source)
	}
	if resolver, ok := r.launcher.(agentSettingsResolver); ok {
		return resolver.ResolveAgentSettings(provider, annotations, model, effort, source)
	}
	return agentSettingsLaunch{}, nil
}

// resolveAgentSettings reads the profile an Agent records, by name, and the
// current content of the instructions its layers name, and resolves the
// settings its next launch runs with (agentsettings.Resolve). model and effort
// are the explicit overrides of this launch, recorded under source.
//
// Only Claude and Codex Agents are layered. A profile that is gone or invalid
// is the profile-resume-unavailable refusal the resume itself makes, so the
// caller refuses the same way.
//
// When the launch passes new instructions content, the snapshot of that
// content is written here, before any transaction opens, like the snapshot a
// create writes: it is content addressed, so one a later failure leaves
// behind is harmless.
func resolveAgentSettings(homeDir func() (string, error), lookupEnv func(string) string, provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error) {
	if provider != aiModeClaude && provider != aiModeCodex {
		return agentSettingsLaunch{}, nil
	}
	in := agentsettings.Input{Annotations: annotations, InstructionsFixed: provider == aiModeCodex}
	if name := annotations[coremetadata.AnnotationAgentProfile]; name != "" {
		_, loaded, spec, err := resolveRecordedProfile(homeDir, lookupEnv, name)
		if err != nil {
			return agentSettingsLaunch{}, err
		}
		in.Profile = &agentsettings.Profile{Name: loaded.Name, Digest: loaded.Digest,
			Instructions: spec.Instructions, Model: spec.Model, Effort: spec.Effort}
	}
	if model != "" {
		in.Model = &agentsettings.Override{Value: model, Source: source}
	}
	if effort != "" {
		in.Effort = &agentsettings.Override{Value: effort, Source: source}
	}
	var store persona.Store
	storeOK := false
	if paths, err := configPaths(homeDir, lookupEnv); err == nil {
		store, storeOK = persona.NewDefaultStore(paths), true
	}
	contents := map[string][]byte{}
	in.InstructionsDigest = func(name string) (string, bool) {
		if !storeOK {
			return "", false
		}
		loaded, err := store.Load(name)
		if err != nil {
			return "", false
		}
		contents[name] = loaded.Content
		return persona.Digest(loaded.Content), true
	}
	launch := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolve(in)}
	if name := launch.resolution.New.Instructions.Value; name != "" && launch.resolution.InstructionsChanged() {
		if _, err := store.WriteSnapshot(contents[name]); err != nil {
			launch.snapshotErr = err
		}
	}
	return launch, nil
}

// launchAnnotations are the annotations the resume seam reads for this
// launch: base with the effort and the instructions the layers resolved, and
// the system prompt snapshot mode off when the instructions content changed,
// because the conversation's recorded prompt carries the old content. The
// model is not an annotation the seam reads; see model. An unlayered launch
// returns base itself.
func (l agentSettingsLaunch) launchAnnotations(base map[string]string) map[string]string {
	if !l.layered {
		return base
	}
	r := l.resolution
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	setOrDelete(out, coremetadata.AnnotationAgentEffort, r.New.Effort.Value)
	if r.InstructionsChanged() {
		setOrDelete(out, coremetadata.AnnotationAgentPersona, r.New.Instructions.Value)
		setOrDelete(out, coremetadata.AnnotationAgentPersonaDigest, r.InstructionsDigest)
		out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	}
	if maps.Equal(out, base) {
		return base
	}
	return out
}

func setOrDelete(annotations map[string]string, key, value string) {
	if value == "" {
		delete(annotations, key)
		return
	}
	annotations[key] = value
}

// model is the model the launch passes, "" for none: an explicit override, or
// the layers' model when it differs from the recorded one (U1).
func (l agentSettingsLaunch) model(override string) string {
	if !l.layered {
		return override
	}
	if l.resolution.PassModel {
		return l.resolution.New.Model.Value
	}
	return ""
}

// notice is the one-line disclosure of instructions the layers ask for that
// this launch does not pass, or "".
func (l agentSettingsLaunch) notice(label string) string {
	r := l.resolution
	switch {
	case !l.layered:
		return ""
	case r.InstructionsNotApplied:
		return fmt.Sprintf("projmux: agent/%s resumed with the instructions its Codex thread started with; instructions %s were not applied (%s): %s",
			label, agentSettingsInstructionsWord(r.WantedInstructions), personaReasonCodexInstructionsImmutable,
			"its thread replays the developer message recorded when it started")
	case r.InstructionsUnavailable != "":
		return fmt.Sprintf("projmux: agent/%s resumed with its recorded instructions; instructions %s cannot be read now (%s)",
			label, r.InstructionsUnavailable, persona.ReasonUnavailable)
	}
	return ""
}

// agentSettingsInstructionsWord names the instructions a Codex launch could
// not apply, "none" for no instructions.
func agentSettingsInstructionsWord(name string) string {
	if name == "" {
		return "none"
	}
	return name
}

// record writes the layered settings this launch runs with onto the Agent, in
// the rebind transaction: every value it launched with and the source it
// resolved. A setting the launch left as it was is left as it was, and one
// kept as an override of unknown source keeps no source (O-1).
func (l agentSettingsLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if !l.layered {
		return nil
	}
	r := l.resolution
	if next := r.New.Effort; next != r.Current.Effort && next.Source != "" {
		var err error
		if next.Source == coremetadata.SettingSourceProfile {
			_, err = mutator.SetAgentEffortFromProfile(registry, agentUID, next.Value)
		} else {
			_, err = mutator.SetAgentEffort(registry, agentUID, next.Value, next.Source)
		}
		if err != nil {
			return MapMetadataError(err)
		}
	}
	if next := r.New.Model; next.Source != "" {
		var err error
		switch {
		case r.PassModel && next.Source == coremetadata.SettingSourceProfile:
			_, err = mutator.SetAgentModelFromProfile(registry, agentUID, next.Value)
		case r.PassModel:
			_, err = mutator.SetAgentModel(registry, agentUID, next.Value, next.Source)
		case next.Source != r.Current.Model.Source:
			// The model is not passed, so it stays the last one requested;
			// only its layer is recorded.
			_, err = mutator.SetAgentModelFromProfile(registry, agentUID, r.Current.Model.Value)
		}
		if err != nil {
			return MapMetadataError(err)
		}
	}
	current := coremetadata.PersonaAnnotationsOf(coremetadata.Agent{Metadata: coremetadata.ObjectMeta{Annotations: registryAgentAnnotations(registry, agentUID)}})
	want := current
	if r.InstructionsChanged() {
		want.Persona, want.PersonaDigest = r.New.Instructions.Value, r.InstructionsDigest
		want.SystemPromptSnapshot = coremetadata.SystemPromptSnapshotOff
	}
	want.InstructionsSource = r.New.Instructions.Source
	if want != current {
		if _, err := mutator.SetAgentPersona(registry, agentUID, want); err != nil {
			return MapMetadataError(err)
		}
	}
	return nil
}

func registryAgentAnnotations(registry *coremetadata.Registry, agentUID string) map[string]string {
	if agent, ok := registry.Agent(agentUID); ok {
		return agent.Metadata.Annotations
	}
	return nil
}
