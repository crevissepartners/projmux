package runtime

// agentResumeLaunch is one planned resume launch.
type AgentResumeLaunch struct {
	Title string
	Argv  []string
	// personaUnavailable is set when the Agent records a persona this launch
	// could not re-pass. It never fails the resume: the Agent comes back
	// without its persona and the consumer discloses personaNotice.
	PersonaUnavailable *persona.Error
	// effortInvalid is the recorded effort this launch did not re-pass
	// because Claude would not take it. Like personaUnavailable it never
	// fails the resume: the consumer discloses effortNotice.
	// effortSkipped reports that effortInvalid holds such a value, which may
	// itself be empty.
	EffortInvalid string
	EffortSkipped bool
	// profileName and profileDigest are the profile this launch re-applied
	// and the digest of the content it applied, both empty when the Agent
	// records no profile. Every consumer records the digest on the Agent.
	ProfileName   string
	ProfileDigest string
	// projectLinksUnavailable is set when the launch annotations name Project
	// label link rules whose snapshot this launch could not pass. Like
	// personaUnavailable it never fails the resume: the consumer discloses
	// projectLinksNotice.
	ProjectLinksUnavailable error
	// agentGuidanceUnavailable is set when the launch annotations name agent
	// guidance whose snapshot this launch could not pass. Like
	// projectLinksUnavailable it never fails the resume: the consumer
	// discloses agentGuidanceNotice.
	AgentGuidanceUnavailable error
}

// agentGuidanceNotice is the one-line disclosure of agent guidance the resume
// could not pass, or "" when there is nothing to disclose.
func (l AgentResumeLaunch) AgentGuidanceNotice(label string) string {
	if l.AgentGuidanceUnavailable == nil {
		return ""
	}
	return AgentGuidanceNotice(label, l.AgentGuidanceUnavailable)
}

// projectLinksNotice is the one-line disclosure of Project label link rules
// the resume could not pass, or "" when there is nothing to disclose.
func (l AgentResumeLaunch) ProjectLinksNotice(label string) string {
	if l.ProjectLinksUnavailable == nil {
		return ""
	}
	return ProjectLinksNotice(label, l.ProjectLinksUnavailable)
}

// personaNotice is the one-line disclosure of a persona the resume could not
// re-pass, or "" when there is nothing to disclose. label names the Agent the
// way the consumer's neighbouring notices do.
func (l AgentResumeLaunch) PersonaNotice(label string) string {
	if l.PersonaUnavailable == nil {
		return ""
	}
	return fmt.Sprintf("projmux: agent/%s resumed without its instructions %s (%s): %s",
		label, l.PersonaUnavailable.Name, l.PersonaUnavailable.Reason, l.PersonaUnavailable.Detail)
}

// effortNotice is the one-line disclosure of a recorded effort the resume did
// not re-pass, or "" when there is nothing to disclose. label names the Agent
// the way personaNotice does.
func (l AgentResumeLaunch) EffortNotice(label string) string {
	if !l.EffortSkipped {
		return ""
	}
	return fmt.Sprintf("projmux: agent/%s resumed without its effort %q (%s): not one of %s",
		label, l.EffortInvalid, claudeEffortReasonInvalid, strings.Join(ClaudeEffortLevels, ", "))
}

// PlanAgentResume builds the provider resume launch for one stored conversation.
//
// It is the create seam's PlanAgentLaunch with exactly one substitution: the
// exec argv comes from the provider's own resume builder instead of the plain
// binary invocation. There is deliberately no fallback: if the provider cannot
// render a resume argv for this conversation id, the error is returned and the
// caller stops. Degrading to a fresh session here is what the interactive resume
// picker does (ai.go's runSelectedResumeSession), and it is precisely what this
// route must not do, because a resume that silently starts a new conversation
// loses the operator's context without telling them.
//
// A Claude Agent created with a persona is resumed with the snapshot its
// persona-digest annotation names. A snapshot that is gone does not stop the
// resume. The seam reads the annotations it is given: the `agent resume`
// rebind, Continue replay and a resume-picker create hand it the instructions
// the settings layers resolve to, with a new snapshot and the snapshot mode
// off when their name or current content changed
// (agentSettingsLaunch.launchAnnotations).
//
// A Claude Agent whose persona was attached or detached after its
// conversation started also records the system prompt snapshot mode `off`,
// and every resume of it passes `--system-prompt-snapshot off` so Claude
// rebuilds the system prompt instead of replaying the one it recorded before
// the attach. An Agent without that annotation gets exactly the argv it got
// before the annotation existed.
//
// An Agent created or resumed with --effort records it, and Claude and Codex
// resumes re-pass valid values. An invalid recorded value is skipped and
// disclosed. The model is not re-passed on ordinary resume: the conversation
// owns it. Only a layered launch passes one, through
// PlanAgentResumeWithModel: an `agent resume --model`, or the profile's model
// when it differs from the recorded one (agentsettings.Resolve); the launch
// records it on the Agent (AnnotationAgentModel), and later plain resumes do
// not re-pass it.
//
// A Claude Agent created with a profile is resumed with that profile's
// current permissions: the profile is re-read by name, its settings snapshot
// rebuilt and passed as --settings, and the launch reports the digest it
// applied. A profile that is gone or invalid fails the resume with
// profile-resume-unavailable; there is no resume without its permissions.
func (c *aiCommand) PlanAgentResume(Provider string, Workspace coremetadata.AgentWorkspace, ConversationID string, Annotations map[string]string) (AgentResumeLaunch, error) {
	return c.PlanAgentResumeWithModel(Provider, Workspace, ConversationID, Annotations, "")
}

// PlanAgentResumeWithModel is PlanAgentResume with model passed once, where
// create puts it: ahead of the effort. The model is validated by the
// `agent resume` preflight and recorded by the rebind transaction, not here.
func (c *aiCommand) PlanAgentResumeWithModel(Provider string, Workspace coremetadata.AgentWorkspace, ConversationID string, Annotations map[string]string, Model string) (AgentResumeLaunch, error) {
	mode := NormalizeAIMode(Provider)
	resumeArgv, err := ResumeArgsForAgent(mode, ConversationID)
	if err != nil {
		return AgentResumeLaunch{}, err
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		// No displayMessage here: this route is detached and non-interactive, so
		// the diagnostic belongs on the error the caller propagates rather than
		// in a tmux status line the operator may not be looking at.
		return AgentResumeLaunch{}, errors.New(c.missingAgentRunnerMessage(mode))
	}
	resumeArgv[0] = agentBin
	// The workspace half of the argv comes from the same provider grammar the
	// create seam uses, so a provider whose option arity is written down once
	// cannot be spelled two ways. There is no payload on this route -- the
	// conversation id is the provider's own resume option, not an operand -- so
	// no option terminator participates.
	workspaceArgs, err := ProviderLaunchArgs(mode, coremetadata.AgentWorkspace{
		CWD:                     strings.TrimSpace(Workspace.CWD),
		AdditionalWritableRoots: Workspace.AdditionalWritableRoots,
	}, nil)
	if err != nil {
		return AgentResumeLaunch{}, err
	}
	// The model, the effort and the persona go where create puts them, before the
	// workspace arguments, so Claude's variadic --add-dir cannot take them.
	// The system prompt snapshot mode an attached persona recorded goes right
	// after them, for the same reason.
	Effort, EffortInvalid, EffortSkipped := ClaudeResumeEffort(mode, Annotations)
	personaFile, PersonaUnavailable := c.ResumePersonaSnapshot(mode, Annotations)
	// A Claude Agent launched with its Project's label link rules gets the
	// snapshot its digest annotation names, alone or after the persona in one
	// composite file: Claude keeps only the last --append-system-prompt-file.
	systemPromptFile, ProjectLinksUnavailable := c.resumeProjectSystemPromptFile(mode, Annotations, personaFile)
	// The agent guidance its digest annotation names goes in front of that
	// file, in one composite, for the same reason.
	systemPromptFile, AgentGuidanceUnavailable := c.resumeGuidanceSystemPromptFile(mode, Annotations, systemPromptFile)
	ProfileName, ProfileDigest, settingsFile, codexPolicy, err := c.resumeProfileSettings(mode, Annotations)
	if err != nil {
		return AgentResumeLaunch{}, err
	}
	prefix := append(ClaudeLaunchOptionArgs(Model, Effort, systemPromptFile), ClaudeSettingsArgs(settingsFile)...)
	if mode == aiModeCodex {
		prefix = append(CodexLaunchOptionArgs(Model, Effort), CodexCLIProfileArgs(codexPolicy)...)
	}
	if prefix = append(prefix, ClaudeResumeSnapshotArgs(mode, Annotations)...); len(prefix) > 0 {
		workspaceArgs = append(prefix, workspaceArgs...)
	}
	resumeArgv = append(resumeArgv[:1], append(workspaceArgs, resumeArgv[1:]...)...)
	plan, err := c.planAgentLaunch(mode, Workspace.CWD, nil, resumeArgv, filepath.Dir(agentBin))
	if err != nil {
		return AgentResumeLaunch{}, err
	}
	return AgentResumeLaunch{
		Title: plan.Title, Argv: plan.commandArgs, PersonaUnavailable: PersonaUnavailable,
		EffortInvalid: EffortInvalid, EffortSkipped: EffortSkipped,
		ProfileName: ProfileName, ProfileDigest: ProfileDigest,
		ProjectLinksUnavailable: ProjectLinksUnavailable, AgentGuidanceUnavailable: AgentGuidanceUnavailable,
	}, nil
}

// resumePersonaSnapshot returns the persona snapshot a resumed Agent started
// with, or why it cannot be re-passed. An Agent without a persona annotation
// costs nothing; one with it costs the annotation read and one stat.
func (c *aiCommand) ResumePersonaSnapshot(mode string, Annotations map[string]string) (string, *persona.Error) {
	Name := Annotations[coremetadata.AnnotationAgentPersona]
	if Name == "" {
		return "", nil
	}
	if mode == aiModeCodex {
		// A Codex persona is the developer instructions its thread was started
		// with, and the thread replays them on every resume -- upstream neither
		// records nor applies a persona given to thread/resume. So there is
		// nothing to re-pass and nothing was lost: no argv, and no notice,
		// which would otherwise report a working persona as unavailable.
		return "", nil
	}
	if mode != aiModeClaude {
		return "", &persona.Error{Reason: persona.ReasonUnavailable, Name: Name,
			Detail: "is not re-passed: instructions apply only to --provider " + aiModeClaude}
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", &persona.Error{Reason: persona.ReasonUnavailable, Name: Name, Detail: "snapshot cannot be located: " + err.Error()}
	}
	path, err := persona.NewDefaultStore(paths).RecordedSnapshotPath(Annotations[coremetadata.AnnotationAgentPersonaDigest])
	if err != nil {
		var unavailable *persona.Error
		if !errors.As(err, &unavailable) {
			unavailable = &persona.Error{Reason: persona.ReasonUnavailable, Detail: err.Error()}
		}
		unavailable.Name = Name
		return "", unavailable
	}
	return path, nil
}

func (c *aiCommand) BindResumedAgentPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, Provider, contextDir, Title, ConversationID string,
) error {
	return c.BindResumedAgentPaneWithSourceOnRoute(ctx, runner, paneID, Provider, contextDir, Title, ConversationID, "")
}

func (c *aiCommand) BindResumedAgentPaneWithSourceOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, Provider, contextDir, Title, ConversationID, Source string,
) error {
	return c.BindAgentPaneOnRoute(ctx, runner, agentPaneBinding{
		PaneID: paneID, Provider: Provider, ContextDir: contextDir, Title: Title,
		ConversationID: ConversationID, ResumeSource: Source,
	})
}
