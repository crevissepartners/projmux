package runtime

// agentSettingsLaunch is the layered settings one `agent resume`, `agent
// relaunch`, Continue replay or resume-picker create launches an Agent with: the resolution, and the snapshot
// of the new instructions content when the launch passes one. The zero value
// means the launch is not layered and reads the Agent's annotations as they
// are.
type AgentSettingsLaunch struct {
	// projectGuidance is recorded with an ordinary process resume reservation.
	ProjectGuidance ProjectGuidanceLaunch
	Layered         bool
	Resolution      agentsettings.Resolution
	// instructionsErr is why the instructions the layers ask for cannot be
	// read now (resolution.InstructionsUnavailable names them).
	InstructionsErr error
	// snapshot writes the snapshot of the new instructions content; nil when
	// the launch passes none. It is written only by a launch, never by a
	// preflight or a dry run.
	Snapshot func() error
	// snapshotErr is why the new instructions snapshot could not be written.
	SnapshotErr error
}

// agentSettingsRequest is what one launch asks of an Agent's layers: the
// explicit overrides (model, effort and instructions, recorded under source),
// the items whose override it removes (reset), and the profile it switches to
// (profile; "" for none). The zero value asks nothing: the launch runs what
// the layers resolve to now.
type AgentSettingsRequest struct {
	ProjectUID    string
	Model, Effort string
	// instructions and profile are nil when not asked; a pointer to "" is an
	// explicit none.
	Instructions *string
	Profile      *string
	Reset        []string
	Source       string
	// guidance and linkRules are the digests of the agent guidance and the
	// Project's label link rules this launch passes (withPromptParts), nil
	// when it passes none of its own. They change no layer; the resolver
	// only reports that they differ from the recorded ones.
	Guidance, ProjectGuidance, LinkRules *string
}

// changesLayers reports a request that changes the layers themselves rather
// than only the values of this launch: a profile, instructions, or a reset.
func (q AgentSettingsRequest) ChangesLayers() bool {
	return q.Profile != nil || q.Instructions != nil || len(q.Reset) > 0
}

// withPromptParts is request with the digests of the agent guidance and the
// Project's label link rules the launch passes, as planAgentGuidanceWith and
// planProjectLinksWith read them. A part the launch does not pass, or cannot
// read now, stays nil: it gives the resolver nothing to compare.
func (q AgentSettingsRequest) WithPromptParts(Guidance AgentGuidanceLaunch, links ProjectLinksLaunch) AgentSettingsRequest {
	if Guidance.active && Guidance.unavailable == nil {
		q.Guidance = &Guidance.digest
	}
	if links.Project.active && links.Project.unavailable == nil {
		q.ProjectGuidance = &links.Project.digest
	}
	if links.active && links.unavailable == nil {
		q.LinkRules = &links.digest
	}
	return q
}

// relaunchProfileError is a profile an `agent relaunch --profile` cannot
// switch to: gone, invalid, or for another provider. Reason is the store's
// own token, or profile-provider-mismatch.
type RelaunchProfileError struct {
	Name   string
	Reason string
	Detail string
}

func (e *RelaunchProfileError) Error() string {
	return fmt.Sprintf("profile %q cannot be applied (%s): %s", e.Name, e.Reason, e.Detail)
}

// resolveSwitchProfile reads the profile a relaunch switches to, by name, the
// way a create resolves an explicit --profile, and refuses one for another
// provider.
func ResolveSwitchProfile(homeDir func() (string, error), lookupEnv func(string) string, Provider, Name string, ProjectUID ...string) (Profile.Profile, Profile.Spec, error) {
	_, loaded, spec, err := resolveRecordedProfile(homeDir, lookupEnv, Name, ProjectUID...)
	if err != nil {
		var unavailable *profileResumeError
		if errors.As(err, &unavailable) {
			return Profile.Profile{}, Profile.Spec{}, &RelaunchProfileError{Name: Name, Reason: unavailable.Reason, Detail: unavailable.Detail}
		}
		return Profile.Profile{}, Profile.Spec{}, err
	}
	if spec.Provider != "" && spec.Provider != Provider {
		return Profile.Profile{}, Profile.Spec{}, &RelaunchProfileError{Name: Name, Reason: profileReasonProviderMismatch,
			Detail: fmt.Sprintf("it is for provider %s, not %s", spec.Provider, Provider)}
	}
	return loaded, spec, nil
}

func SettingsProfile(loaded Profile.Profile, spec Profile.Spec) *agentsettings.Profile {
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
func ResolveAgentSettings(homeDir func() (string, error), lookupEnv func(string) string, Provider string, Annotations map[string]string, request AgentSettingsRequest) (AgentSettingsLaunch, error) {
	if Provider != aiModeClaude && Provider != aiModeCodex {
		return AgentSettingsLaunch{}, nil
	}
	in := agentsettings.Input{Annotations: Annotations, InstructionsFixed: Provider == aiModeCodex, Reset: request.Reset}
	recordedName := Annotations[coremetadata.AnnotationAgentProfile]
	switching := request.Profile != nil && *request.Profile != recordedName
	if recordedName != "" {
		_, loaded, spec, err := resolveRecordedProfile(homeDir, lookupEnv, recordedName, request.ProjectUID)
		switch {
		case err == nil:
			in.Profile = SettingsProfile(loaded, spec)
		case !switching || Profile.ReasonOf(err) == Profile.ReasonOutOfScope:
			return AgentSettingsLaunch{}, err
		}
	}
	if switching {
		in.Switch, in.SwitchSource = true, request.Source
		if *request.Profile != "" {
			loaded, spec, err := ResolveSwitchProfile(homeDir, lookupEnv, Provider, *request.Profile, request.ProjectUID)
			if err != nil {
				return AgentSettingsLaunch{}, err
			}
			in.NewProfile = SettingsProfile(loaded, spec)
		}
	}
	if request.Instructions != nil {
		in.Instructions = &agentsettings.Override{Value: *request.Instructions, Source: request.Source}
	}
	if request.Model != "" {
		in.Model = &agentsettings.Override{Value: request.Model, Source: request.Source}
	}
	if request.Effort != "" {
		in.Effort = &agentsettings.Override{Value: request.Effort, Source: request.Source}
	}
	in.Guidance, in.ProjectGuidance, in.LinkRules = request.Guidance, request.ProjectGuidance, request.LinkRules
	var store persona.Store
	var storeErr error
	if paths, err := configPaths(homeDir, lookupEnv); err == nil {
		store = persona.NewDefaultStore(paths)
	} else {
		storeErr = err
	}
	contents := map[string][]byte{}
	var InstructionsErr error
	in.InstructionsDigest = func(Name string) (string, bool) {
		if storeErr != nil {
			InstructionsErr = storeErr
			return "", false
		}
		loaded, err := store.Load(Name)
		if err != nil {
			InstructionsErr = err
			return "", false
		}
		contents[Name] = loaded.Content
		return persona.Digest(loaded.Content), true
	}
	launch := AgentSettingsLaunch{Layered: true, Resolution: agentsettings.Resolve(in), InstructionsErr: InstructionsErr}
	if Name := launch.Resolution.New.Instructions.Value; Name != "" && launch.Resolution.InstructionsChanged() {
		content := contents[Name]
		launch.Snapshot = func() error {
			_, err := store.WriteSnapshot(content)
			return err
		}
	}
	return launch, nil
}

// writeSnapshot writes the snapshot of the new instructions content the
// launch passes, if any, and keeps why it could not.
func (l AgentSettingsLaunch) WriteSnapshot() AgentSettingsLaunch {
	if l.Snapshot != nil {
		l.SnapshotErr = l.Snapshot()
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
func (l AgentSettingsLaunch) LaunchAnnotations(base map[string]string) map[string]string {
	if !l.Layered {
		return base
	}
	r := l.Resolution
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	if r.ProfileSwitched {
		// The resume seam re-reads the profile by name for its permissions.
		SetOrDelete(out, coremetadata.AnnotationAgentProfile, r.New.Profile.Name)
		SetOrDelete(out, coremetadata.AnnotationAgentProfileDigest, r.New.Profile.Digest)
		SetOrDelete(out, coremetadata.AnnotationAgentProfileSource, r.New.Profile.Source)
	}
	SetOrDelete(out, coremetadata.AnnotationAgentEffort, r.New.Effort.Value)
	if r.InstructionsChanged() {
		SetOrDelete(out, coremetadata.AnnotationAgentPersona, r.New.Instructions.Value)
		SetOrDelete(out, coremetadata.AnnotationAgentPersonaDigest, r.InstructionsDigest)
		out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	}
	if maps.Equal(out, base) {
		return base
	}
	return out
}

func SetOrDelete(Annotations map[string]string, key, value string) {
	if value == "" {
		delete(Annotations, key)
		return
	}
	Annotations[key] = value
}

// model is the model the launch passes, "" for none: an explicit override, or
// the layers' model when it differs from the recorded one (U1).
func (l AgentSettingsLaunch) Model(override string) string {
	if !l.Layered {
		return override
	}
	if l.Resolution.PassModel {
		return l.Resolution.New.Model.Value
	}
	return ""
}

// notice is the one-line disclosure of instructions the layers ask for that
// this launch does not pass, or "".
func (l AgentSettingsLaunch) Notice(label string) string {
	r := l.Resolution
	switch {
	case !l.Layered:
		return ""
	case r.InstructionsNotApplied:
		return fmt.Sprintf("projmux: agent/%s resumed with the instructions its Codex thread started with; instructions %s were not applied (%s): %s",
			label, AgentSettingsInstructionsWord(r.WantedInstructions), personaReasonCodexInstructionsImmutable,
			"its thread replays the developer message recorded when it started")
	case r.InstructionsUnavailable != "":
		return fmt.Sprintf("projmux: agent/%s resumed with its recorded instructions; instructions %s cannot be read now (%s)",
			label, r.InstructionsUnavailable, persona.ReasonUnavailable)
	}
	return ""
}

// agentSettingsInstructionsWord names the instructions a Codex launch could
// not apply, "none" for no instructions.
func AgentSettingsInstructionsWord(Name string) string {
	if Name == "" {
		return "none"
	}
	return Name
}

// record writes the layered settings this launch runs with onto the Agent, in
// the rebind transaction: the profile it switched to, every value it launched
// with and the source it resolved. A setting the launch left as it was is left
// as it was, and one kept as an override of unknown source keeps no source
// (O-1). An item left with no layer at all (no profile, no override) keeps
// neither value nor source.
func (l AgentSettingsLaunch) Record(registry *coremetadata.Registry, mutator coremetadata.Mutator, AgentUID string) error {
	if err := l.ProjectGuidance.Record(registry, mutator, AgentUID); err != nil {
		return err
	}
	if !l.Layered {
		return nil
	}
	r := l.Resolution
	if r.ProfileSwitched {
		if _, err := mutator.SetAgentProfile(registry, AgentUID, r.New.Profile.Name, r.New.Profile.Digest, r.New.Profile.Source); err != nil {
			return MapMetadataError(err)
		}
	}
	if next, current := r.New.Effort, r.Current.Effort; next.Value != current.Value || next.Source != current.Source {
		var err error
		switch next.Source {
		case "":
			_, err = mutator.ClearAgentEffort(registry, AgentUID)
		case coremetadata.SettingSourceProfile:
			_, err = mutator.SetAgentEffortFromProfile(registry, AgentUID, next.Value)
		default:
			_, err = mutator.SetAgentEffort(registry, AgentUID, next.Value, next.Source)
		}
		if err != nil {
			return MapMetadataError(err)
		}
	}
	if next, current := r.New.Model, r.Current.Model; next.Source != "" || current.Value != "" || current.Source != "" {
		var err error
		switch {
		case r.PassModel && next.Source == coremetadata.SettingSourceProfile:
			_, err = mutator.SetAgentModelFromProfile(registry, AgentUID, next.Value)
		case r.PassModel:
			_, err = mutator.SetAgentModel(registry, AgentUID, next.Value, next.Source)
		case next.Source == "" && next.Value == "" && next != current:
			// No layer gives a model any more.
			_, err = mutator.ClearAgentModel(registry, AgentUID)
		case next.Source != "" && next.Source != current.Source:
			// The model is not passed, so the provider keeps the last one
			// requested; only its layer is recorded, and a layer without a
			// model drops the recorded one.
			_, err = mutator.SetAgentModelFromProfile(registry, AgentUID, next.Value)
		}
		if err != nil {
			return MapMetadataError(err)
		}
	}
	current := coremetadata.PersonaAnnotationsOf(coremetadata.Agent{Metadata: coremetadata.ObjectMeta{Annotations: RegistryAgentAnnotations(registry, AgentUID)}})
	want := current
	if r.InstructionsChanged() {
		want.Persona, want.PersonaDigest = r.New.Instructions.Value, r.InstructionsDigest
		want.SystemPromptSnapshot = coremetadata.SystemPromptSnapshotOff
	}
	want.InstructionsSource = r.New.Instructions.Source
	if want != current {
		if _, err := mutator.SetAgentPersona(registry, AgentUID, want); err != nil {
			return MapMetadataError(err)
		}
	}
	return nil
}

func RegistryAgentAnnotations(registry *coremetadata.Registry, AgentUID string) map[string]string {
	if agent, ok := registry.Agent(AgentUID); ok {
		return agent.Metadata.Annotations
	}
	return nil
}
