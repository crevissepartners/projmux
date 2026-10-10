package app

import (
	"errors"
	"fmt"
	"maps"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// agentSettingsLaunch is the layered settings one `agent resume`, `agent
// relaunch`, Continue replay or resume-picker create launches an Agent with: the resolution, and the snapshot
// of the new instructions content when the launch passes one. The zero value
// means the launch is not layered and reads the Agent's annotations as they
// are.
type agentSettingsLaunch struct {
	// projectGuidance is recorded with an ordinary process resume reservation.
	projectGuidance projectGuidanceLaunch
	layered         bool
	resolution      agentsettings.Resolution
	// instructionsErr is why the instructions the layers ask for cannot be
	// read now (resolution.InstructionsUnavailable names them).
	instructionsErr error
	// snapshot writes the snapshot of the new instructions content; nil when
	// the launch passes none. It is written only by a launch, never by a
	// preflight or a dry run.
	snapshot func() error
	// snapshotErr is why the new instructions snapshot could not be written.
	snapshotErr error
}

// agentSettingsRequest is what one launch asks of an Agent's layers: the
// explicit overrides (model, effort and instructions, recorded under source),
// the items whose override it removes (reset), and the profile it switches to
// (profile; "" for none). The zero value asks nothing: the launch runs what
// the layers resolve to now.
type agentSettingsRequest struct {
	model, effort string
	// instructions and profile are nil when not asked; a pointer to "" is an
	// explicit none.
	instructions *string
	profile      *string
	reset        []string
	source       string
	// guidance and linkRules are the digests of the agent guidance and the
	// Project's label link rules this launch passes (withPromptParts), nil
	// when it passes none of its own. They change no layer; the resolver
	// only reports that they differ from the recorded ones.
	guidance, projectGuidance, linkRules *string
}

// changesLayers reports a request that changes the layers themselves rather
// than only the values of this launch: a profile, instructions, or a reset.
func (q agentSettingsRequest) changesLayers() bool {
	return q.profile != nil || q.instructions != nil || len(q.reset) > 0
}

// agentSettingsResolver is the optional seam that resolves an Agent's layered
// settings from the profile and instructions stores of its home. The Claude
// resume launcher implements it, next to the profile permissions it re-reads
// from the same home; a launcher without it launches the recorded values.
type agentSettingsResolver interface {
	ResolveAgentSettingsRequest(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error)
}

var _ agentSettingsResolver = (*aiCommand)(nil)

// ResolveAgentSettings is ResolveAgentSettingsRequest with only the model and
// effort overrides of `agent resume` and `agent relaunch`.
func (c *aiCommand) ResolveAgentSettings(provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error) {
	return c.ResolveAgentSettingsRequest(provider, annotations, agentSettingsRequest{model: model, effort: effort, source: source})
}

// ResolveAgentSettingsRequest is resolveAgentSettings over the resume seam's
// home.
func (c *aiCommand) ResolveAgentSettingsRequest(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	return resolveAgentSettings(c.homeDir, c.lookupEnv, provider, annotations, request)
}

// resolveSettings resolves the layered settings of one rebind: a Codex Agent
// from the create command's home, where its profile policy is re-read too
// (codexResumeProfile), and any other Agent through the resume launcher.
func (r *agentRebinder) resolveSettings(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	if provider == aiModeCodex {
		return resolveAgentSettings(r.create.homeDir, r.create.lookupEnv, provider, annotations, request)
	}
	if resolver, ok := r.launcher.(agentSettingsResolver); ok {
		return resolver.ResolveAgentSettingsRequest(provider, annotations, request)
	}
	if request.changesLayers() {
		return agentSettingsLaunch{}, errors.New("the resume launcher cannot change an Agent's profile or instructions")
	}
	return agentSettingsLaunch{}, nil
}

// withPromptParts is request with the digests of the agent guidance and the
// Project's label link rules the launch passes, as planAgentGuidanceWith and
// planProjectLinksWith read them. A part the launch does not pass, or cannot
// read now, stays nil: it gives the resolver nothing to compare.
func (q agentSettingsRequest) withPromptParts(guidance agentGuidanceLaunch, links projectLinksLaunch) agentSettingsRequest {
	if guidance.active && guidance.unavailable == nil {
		q.guidance = &guidance.digest
	}
	if links.project.active && links.project.unavailable == nil {
		q.projectGuidance = &links.project.digest
	}
	if links.active && links.unavailable == nil {
		q.linkRules = &links.digest
	}
	return q
}

// relaunchProfileError is a profile an `agent relaunch --profile` cannot
// switch to: gone, invalid, or for another provider. Reason is the store's
// own token, or profile-provider-mismatch.
type relaunchProfileError struct {
	name   string
	reason string
	detail string
}

func (e *relaunchProfileError) Error() string {
	return fmt.Sprintf("profile %q cannot be applied (%s): %s", e.name, e.reason, e.detail)
}

// resolveSwitchProfile reads the profile a relaunch switches to, by name, the
// way a create resolves an explicit --profile, and refuses one for another
// provider.
func resolveSwitchProfile(homeDir func() (string, error), lookupEnv func(string) string, provider, name string) (profile.Profile, profile.Spec, error) {
	_, loaded, spec, err := resolveRecordedProfile(homeDir, lookupEnv, name)
	if err != nil {
		var unavailable *profileResumeError
		if errors.As(err, &unavailable) {
			return profile.Profile{}, profile.Spec{}, &relaunchProfileError{name: name, reason: unavailable.reason, detail: unavailable.detail}
		}
		return profile.Profile{}, profile.Spec{}, err
	}
	if spec.Provider != "" && spec.Provider != provider {
		return profile.Profile{}, profile.Spec{}, &relaunchProfileError{name: name, reason: profileReasonProviderMismatch,
			detail: fmt.Sprintf("it is for provider %s, not %s", spec.Provider, provider)}
	}
	return loaded, spec, nil
}

func settingsProfile(loaded profile.Profile, spec profile.Spec) *agentsettings.Profile {
	return &agentsettings.Profile{Name: loaded.Name, Digest: loaded.Digest,
		Instructions: spec.Instructions, Model: spec.Model, Effort: spec.Effort}
}

// resolveAgentSettings reads the profile an Agent records, by name, and the
// current content of the instructions its layers name, and resolves the
// settings its next launch runs with (agentsettings.Resolve) for request.
//
// Only Claude and Codex Agents are layered. A profile that is gone or invalid
// is the profile-resume-unavailable refusal the resume itself makes, so the
// caller refuses the same way -- unless the request switches away from it:
// that profile then only describes what the Agent recorded. The profile a
// request switches to must resolve and be for the Agent's provider
// (relaunchProfileError).
//
// When the launch passes new instructions content, the launch writes the
// snapshot of that content (writeSnapshot) before any transaction opens, like
// the snapshot a create writes: it is content addressed, so one a later
// failure leaves behind is harmless.
func resolveAgentSettings(homeDir func() (string, error), lookupEnv func(string) string, provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	if provider != aiModeClaude && provider != aiModeCodex {
		return agentSettingsLaunch{}, nil
	}
	in := agentsettings.Input{Annotations: annotations, InstructionsFixed: provider == aiModeCodex, Reset: request.reset}
	recordedName := annotations[coremetadata.AnnotationAgentProfile]
	switching := request.profile != nil && *request.profile != recordedName
	if recordedName != "" {
		_, loaded, spec, err := resolveRecordedProfile(homeDir, lookupEnv, recordedName)
		switch {
		case err == nil:
			in.Profile = settingsProfile(loaded, spec)
		case !switching:
			return agentSettingsLaunch{}, err
		}
	}
	if switching {
		in.Switch, in.SwitchSource = true, request.source
		if *request.profile != "" {
			loaded, spec, err := resolveSwitchProfile(homeDir, lookupEnv, provider, *request.profile)
			if err != nil {
				return agentSettingsLaunch{}, err
			}
			in.NewProfile = settingsProfile(loaded, spec)
		}
	}
	if request.instructions != nil {
		in.Instructions = &agentsettings.Override{Value: *request.instructions, Source: request.source}
	}
	if request.model != "" {
		in.Model = &agentsettings.Override{Value: request.model, Source: request.source}
	}
	if request.effort != "" {
		in.Effort = &agentsettings.Override{Value: request.effort, Source: request.source}
	}
	in.Guidance, in.ProjectGuidance, in.LinkRules = request.guidance, request.projectGuidance, request.linkRules
	var store persona.Store
	var storeErr error
	if paths, err := configPaths(homeDir, lookupEnv); err == nil {
		store = persona.NewDefaultStore(paths)
	} else {
		storeErr = err
	}
	contents := map[string][]byte{}
	var instructionsErr error
	in.InstructionsDigest = func(name string) (string, bool) {
		if storeErr != nil {
			instructionsErr = storeErr
			return "", false
		}
		loaded, err := store.Load(name)
		if err != nil {
			instructionsErr = err
			return "", false
		}
		contents[name] = loaded.Content
		return persona.Digest(loaded.Content), true
	}
	launch := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolve(in), instructionsErr: instructionsErr}
	if name := launch.resolution.New.Instructions.Value; name != "" && launch.resolution.InstructionsChanged() {
		content := contents[name]
		launch.snapshot = func() error {
			_, err := store.WriteSnapshot(content)
			return err
		}
	}
	return launch, nil
}

// writeSnapshot writes the snapshot of the new instructions content the
// launch passes, if any, and keeps why it could not.
func (l agentSettingsLaunch) writeSnapshot() agentSettingsLaunch {
	if l.snapshot != nil {
		l.snapshotErr = l.snapshot()
	}
	return l
}

// launchAnnotations are the annotations the resume seam reads for this
// launch: base with the profile, the effort and the instructions the layers
// resolved, and
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
	if r.ProfileSwitched {
		// The resume seam re-reads the profile by name for its permissions.
		setOrDelete(out, coremetadata.AnnotationAgentProfile, r.New.Profile.Name)
		setOrDelete(out, coremetadata.AnnotationAgentProfileDigest, r.New.Profile.Digest)
		setOrDelete(out, coremetadata.AnnotationAgentProfileSource, r.New.Profile.Source)
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
// the rebind transaction: the profile it switched to, every value it launched
// with and the source it resolved. A setting the launch left as it was is left
// as it was, and one kept as an override of unknown source keeps no source
// (O-1). An item left with no layer at all (no profile, no override) keeps
// neither value nor source.
func (l agentSettingsLaunch) record(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
	if err := l.projectGuidance.record(registry, mutator, agentUID); err != nil {
		return err
	}
	if !l.layered {
		return nil
	}
	r := l.resolution
	if r.ProfileSwitched {
		if _, err := mutator.SetAgentProfile(registry, agentUID, r.New.Profile.Name, r.New.Profile.Digest, r.New.Profile.Source); err != nil {
			return MapMetadataError(err)
		}
	}
	if next, current := r.New.Effort, r.Current.Effort; next.Value != current.Value || next.Source != current.Source {
		var err error
		switch next.Source {
		case "":
			_, err = mutator.ClearAgentEffort(registry, agentUID)
		case coremetadata.SettingSourceProfile:
			_, err = mutator.SetAgentEffortFromProfile(registry, agentUID, next.Value)
		default:
			_, err = mutator.SetAgentEffort(registry, agentUID, next.Value, next.Source)
		}
		if err != nil {
			return MapMetadataError(err)
		}
	}
	if next, current := r.New.Model, r.Current.Model; next.Source != "" || current.Value != "" || current.Source != "" {
		var err error
		switch {
		case r.PassModel && next.Source == coremetadata.SettingSourceProfile:
			_, err = mutator.SetAgentModelFromProfile(registry, agentUID, next.Value)
		case r.PassModel:
			_, err = mutator.SetAgentModel(registry, agentUID, next.Value, next.Source)
		case next.Source == "" && next.Value == "" && next != current:
			// No layer gives a model any more.
			_, err = mutator.ClearAgentModel(registry, agentUID)
		case next.Source != "" && next.Source != current.Source:
			// The model is not passed, so the provider keeps the last one
			// requested; only its layer is recorded, and a layer without a
			// model drops the recorded one.
			_, err = mutator.SetAgentModelFromProfile(registry, agentUID, next.Value)
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
