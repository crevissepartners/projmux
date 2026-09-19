package app

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
)

// pickerLaunchValuesFixture is a split/Window intent fixture whose resume seam
// is the real Claude resume planner, so the argv a picker create launches is
// the exact provider argv, recorded once per plan.
type pickerLaunchValuesFixture struct {
	canonicalRootFixture
	planner  *aiCommand
	launcher *exactArgvResumeLauncher
}

func newPickerLaunchValuesFixture(t *testing.T, planner *aiCommand) pickerLaunchValuesFixture {
	t.Helper()
	fx := canonicalFixture(t, false)
	if planner == nil {
		planner = agentLaunchArgvTestCommand(t)
	}
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), planner: planner}
	fx.create.resumes = launcher
	return pickerLaunchValuesFixture{canonicalRootFixture: fx, planner: planner, launcher: launcher}
}

// hold makes the fixture Agent uid a recorded holder of one provider
// conversation carrying annotations and labels.
func (f pickerLaunchValuesFixture) hold(t *testing.T, uid, provider, conversation string, annotations, labels map[string]string) {
	t.Helper()
	agent, ok := f.store.registry.Agent(uid)
	if !ok {
		t.Fatalf("fixture Agent %q missing", uid)
	}
	ref, ok := coremetadata.NewAgentSessionRef(pickerResumeSessionObservation(provider, conversation), resourceFixtureClock)
	if !ok {
		t.Fatalf("%s conversation %q was rejected", provider, conversation)
	}
	agent.Spec.Provider = provider
	agent.Status.SessionRef = ref
	agent.Metadata.Annotations = maps.Clone(annotations)
	agent.Metadata.Labels = maps.Clone(labels)
}

// pick runs one resume-picker selection through the split producer, or the
// Window producer when window is set, and returns the one Agent it created,
// the one argv it planned, and its stderr.
func (f pickerLaunchValuesFixture) pick(t *testing.T, window bool, provider, conversation, source string) (coremetadata.Agent, []string, string) {
	t.Helper()
	before := map[string]bool{}
	for _, agent := range f.store.registry.Agents {
		before[agent.Metadata.UID] = true
	}
	intent := agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: provider, placement: "right",
		conversationID: conversation, resumeSource: source,
	}
	var stdout, stderr bytes.Buffer
	var err error
	if window {
		_, err = f.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: f.originID, answer: intent}, &stdout, &stderr)
	} else {
		intent.anchorPaneID = f.originID
		_, err = f.create.createFromIntent(intent, &stdout, &stderr)
	}
	if err != nil {
		t.Fatalf("resume-picker create: %v (stderr=%q)", err, stderr.String())
	}
	var created []coremetadata.Agent
	for _, agent := range f.store.registry.Agents {
		if !before[agent.Metadata.UID] {
			created = append(created, agent)
		}
	}
	if len(created) != 1 {
		t.Fatalf("resume-picker create made %d Agents, want 1", len(created))
	}
	if len(f.launcher.argv) != 1 {
		t.Fatalf("planned %d resume argv values, want 1", len(f.launcher.argv))
	}
	planned := f.launcher.argv[0]
	calls := splitWindowCalls(f.tmux)
	if len(calls) == 0 || !strings.Contains(strings.Join(calls[len(calls)-1], "\x00"), strings.Join(planned, "\x00")) {
		t.Fatalf("last split-window %q does not launch the planned argv %q", calls, planned)
	}
	return created[0], planned, stderr.String()
}

// agentRecord is the stored form of one Agent's metadata and spec, for a
// byte-identity comparison between two creates.
func agentRecord(t *testing.T, agent coremetadata.Agent) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Metadata coremetadata.ObjectMeta
		Spec     coremetadata.AgentSpec
	}{agent.Metadata, agent.Spec})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// claudeLaunchBundle is the full launch-value bundle an old Agent records:
// persona, digest, snapshot mode off, and an effort.
func claudeLaunchBundle(personaAnnotations map[string]string, effort string) map[string]string {
	out := maps.Clone(personaAnnotations)
	out[coremetadata.AnnotationAgentSystemPromptSnapshot] = coremetadata.SystemPromptSnapshotOff
	out[coremetadata.AnnotationAgentEffort] = effort
	return out
}

// TestResumePickerLaunchValueLookupTable is the lookup table of what a picked
// conversation inherits: holders 0/1/2, bundles equal or different (an absent
// key differs from a present one), a Codex or other-conversation Agent never
// counting for Claude, holders anywhere in the Registry counting, and a
// non-Claude pick inheriting nothing.
func TestResumePickerLaunchValueLookupTable(t *testing.T) {
	t.Parallel()
	const id = "0b6f3f0e-6c2d-4c1e-9a55-7d1f2c3b4a59"
	bundle := map[string]string{
		coremetadata.AnnotationAgentPersona:              "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        "sha256:abc",
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
		coremetadata.AnnotationAgentEffort:               "low",
	}
	withExtras := maps.Clone(bundle)
	maps.Copy(withExtras, coremetadata.CreatorAnnotations("agt-creator", "pan-creator"))
	withExtras[coremetadata.AnnotationAgentTopic] = "unrelated"
	withoutEffort := maps.Clone(bundle)
	delete(withoutEffort, coremetadata.AnnotationAgentEffort)
	emptyEffort := maps.Clone(bundle)
	emptyEffort[coremetadata.AnnotationAgentEffort] = ""
	highEffort := maps.Clone(bundle)
	highEffort[coremetadata.AnnotationAgentEffort] = "high"

	type holder struct {
		uid, window, provider, conversation string
		annotations                         map[string]string
	}
	claudeRef := func(provider, conversation string) *coremetadata.AgentSessionRef {
		ref, _ := coremetadata.NewAgentSessionRef(pickerResumeSessionObservation(provider, conversation), resourceFixtureClock)
		return ref
	}
	for _, test := range []struct {
		name      string
		provider  string
		holders   []holder
		want      map[string]string
		ambiguous bool
	}{
		{name: "no holder", provider: aiModeClaude},
		{name: "one holder", provider: aiModeClaude, holders: []holder{{"agt-a", "win-a", aiModeClaude, id, bundle}}, want: bundle},
		{name: "one holder with creator and topic keys", provider: aiModeClaude, holders: []holder{{"agt-a", "win-a", aiModeClaude, id, withExtras}}, want: bundle},
		{name: "one holder without launch values", provider: aiModeClaude, holders: []holder{{"agt-a", "win-a", aiModeClaude, id, map[string]string{coremetadata.AnnotationAgentTopic: "t"}}}},
		{name: "two holders in different Projects agree", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-alpha", aiModeClaude, id, bundle}, {"agt-b", "win-beta", aiModeClaude, id, withExtras}}, want: bundle},
		{name: "two holders with no launch values agree on nothing", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, nil}, {"agt-b", "win-b", aiModeClaude, id, map[string]string{}}}},
		{name: "two holders differ in a value", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, bundle}, {"agt-b", "win-b", aiModeClaude, id, highEffort}}, ambiguous: true},
		{name: "an absent key differs from a present one", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, bundle}, {"agt-b", "win-b", aiModeClaude, id, withoutEffort}}, ambiguous: true},
		{name: "an absent key differs from an empty one", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, withoutEffort}, {"agt-b", "win-b", aiModeClaude, id, emptyEffort}}, ambiguous: true},
		{name: "a Codex Agent with the same id does not count", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, bundle}, {"agt-codex", "win-a", aiModeCodex, id, highEffort}}, want: bundle},
		{name: "another conversation does not count", provider: aiModeClaude, holders: []holder{
			{"agt-a", "win-a", aiModeClaude, id, bundle}, {"agt-b", "win-a", aiModeClaude, "another-conversation", highEffort}}, want: bundle},
		{name: "only a Codex holder", provider: aiModeClaude, holders: []holder{{"agt-codex", "win-a", aiModeCodex, id, bundle}}},
		{name: "a Codex pick inherits nothing", provider: aiModeCodex, holders: []holder{{"agt-codex", "win-a", aiModeCodex, id, bundle}}},
		{name: "an Antigravity pick inherits nothing", provider: aiModeAntigravity, holders: []holder{{"agt-agy", "win-a", aiModeAntigravity, id, bundle}}},
	} {
		registry := coremetadata.NewRegistry()
		for _, h := range test.holders {
			registry.Agents = append(registry.Agents, coremetadata.Agent{
				Metadata: coremetadata.ObjectMeta{UID: h.uid, Name: h.uid, Annotations: h.annotations,
					OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: h.window}},
				Status: coremetadata.AgentStatus{SessionRef: claudeRef(h.provider, h.conversation)},
			})
		}
		got, notice := inheritedResumeLaunchValues(&registry, test.provider, id)
		if !maps.Equal(got, test.want) || (got == nil) != (test.want == nil) {
			t.Errorf("%s: inherited %v, want %v", test.name, got, test.want)
		}
		if gotAmbiguous := strings.Contains(notice, "("+launchValuesReasonAmbiguous+")"); gotAmbiguous != test.ambiguous {
			t.Errorf("%s: notice %q, want ambiguous=%t", test.name, notice, test.ambiguous)
		}
		if test.ambiguous && (!strings.HasPrefix(notice, "claude conversation "+id+" ") ||
			!strings.Contains(notice, "agent/agt-a (uid:agt-a), agent/agt-b (uid:agt-b)")) {
			t.Errorf("%s: notice %q does not open with the conversation (and no projmux: prefix) and name both holders in uid order", test.name, notice)
		}
	}
}

// TestResumePickerInheritsTheLaunchValuesOfTheAgentThatHadTheConversation is
// the central case on both UI producers: one old Claude Agent in another
// Project recorded the picked conversation with a persona, snapshot mode off
// and effort low. The new Agent launches with the same options the old one
// would resume with and records exactly that bundle -- no creator, topic or
// label of the old Agent -- and the old Agent is left as it was.
func TestResumePickerInheritsTheLaunchValuesOfTheAgentThatHadTheConversation(t *testing.T) {
	t.Parallel()
	for _, window := range []bool{false, true} {
		name := "split"
		if window {
			name = "new Window"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newPickerLaunchValuesFixture(t, nil)
			personaAnnotations, snapshot := createPersonaForResume(t, f.planner, "go-reviewer", []byte(personaResumeContent))
			bundle := claudeLaunchBundle(personaAnnotations, "low")
			old := maps.Clone(bundle)
			maps.Copy(old, coremetadata.CreatorAnnotations("agt-creator", "pan-creator"))
			old[coremetadata.AnnotationAgentTopic] = "reviewing the parser"
			f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, old, map[string]string{"team": "review"})
			holderBefore, _ := f.store.registry.Agent("agt-beta-codex")
			holderRecord := agentRecord(t, *holderBefore)

			agent, argv, stderr := f.pick(t, window, aiModeClaude, personaResumeConversation, "")

			want := []string{"--effort", "low", "--append-system-prompt-file", snapshot,
				"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
			if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
				t.Fatalf("exec argv tail = %q, want %q", got, want)
			}
			if !maps.Equal(agent.Metadata.Annotations, bundle) {
				t.Fatalf("new Agent annotations = %v, want exactly the bundle %v", agent.Metadata.Annotations, bundle)
			}
			if agent.Metadata.Labels != nil {
				t.Fatalf("new Agent labels = %v, want none", agent.Metadata.Labels)
			}
			if agent.Status.SessionRef == nil || agent.Status.SessionRef.ConversationID() != personaResumeConversation {
				t.Fatalf("new Agent sessionRef = %#v", agent.Status.SessionRef)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want nothing to disclose", stderr)
			}
			holder, _ := f.store.registry.Agent("agt-beta-codex")
			if got := agentRecord(t, *holder); got != holderRecord {
				t.Fatalf("the old Agent changed:\n got %s\nwant %s", got, holderRecord)
			}
		})
	}
}

// TestResumePickerWithNoRecordedHolderLaunchesAndStoresWhatItDidBefore pins
// the unchanged cell: with no Agent holding the picked conversation -- or
// only Agents of another conversation or of another provider with the same id
// string -- the argv is the unannotated resume argv and the stored Agent is
// byte-identical to the one a Registry without those Agents produces.
func TestResumePickerWithNoRecordedHolderLaunchesAndStoresWhatItDidBefore(t *testing.T) {
	t.Parallel()
	for _, window := range []bool{false, true} {
		control := newPickerLaunchValuesFixture(t, nil)
		controlAgent, controlArgv, controlStderr := control.pick(t, window, aiModeClaude, personaResumeConversation, "")
		unannotated, err := control.planner.PlanAgentResume(aiModeClaude, controlAgent.Spec.Workspace, personaResumeConversation, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(controlArgv, unannotated.argv) || controlAgent.Metadata.Annotations != nil || controlStderr != "" {
			t.Fatalf("window=%t control: argv=%q annotations=%v stderr=%q, want the unannotated resume", window, controlArgv, controlAgent.Metadata.Annotations, controlStderr)
		}

		f := newPickerLaunchValuesFixture(t, control.planner)
		bundle := claudeLaunchBundle(map[string]string{coremetadata.AnnotationAgentPersona: "p", coremetadata.AnnotationAgentPersonaDigest: "sha256:0"}, "high")
		f.hold(t, "agt-beta-codex", aiModeClaude, "another-claude-conversation", bundle, nil)
		f.hold(t, "agt-alpha-codex", aiModeCodex, personaResumeConversation, bundle, nil)
		agent, argv, stderr := f.pick(t, window, aiModeClaude, personaResumeConversation, "")
		if !slices.Equal(argv, controlArgv) {
			t.Fatalf("window=%t argv = %q, want the control %q", window, argv, controlArgv)
		}
		if got, want := agentRecord(t, agent), agentRecord(t, controlAgent); got != want {
			t.Fatalf("window=%t stored Agent:\n got %s\nwant %s", window, got, want)
		}
		if stderr != "" {
			t.Fatalf("window=%t stderr = %q, want nothing", window, stderr)
		}
	}
}

// TestResumePickerInheritsNothingWhenTheHoldersDisagree pins the ambiguity
// rule: two holders whose bundles differ -- in a value, or one lacking a key
// the other records -- give the new Agent nothing and one
// launch-values-ambiguous line, never the bundle of either; two holders that
// agree give it their bundle.
func TestResumePickerInheritsNothingWhenTheHoldersDisagree(t *testing.T) {
	t.Parallel()
	planner := agentLaunchArgvTestCommand(t)
	personaAnnotations, snapshot := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	bundle := claudeLaunchBundle(personaAnnotations, "low")
	highEffort := claudeLaunchBundle(personaAnnotations, "high")
	withoutEffort := maps.Clone(bundle)
	delete(withoutEffort, coremetadata.AnnotationAgentEffort)

	control := newPickerLaunchValuesFixture(t, planner)
	controlAgent, controlArgv, _ := control.pick(t, false, aiModeClaude, personaResumeConversation, "")

	for _, test := range []struct {
		name  string
		other map[string]string
	}{
		{"different effort", highEffort},
		{"effort absent on one", withoutEffort},
	} {
		f := newPickerLaunchValuesFixture(t, planner)
		f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, bundle, nil)
		f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, test.other, nil)
		agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
		if !slices.Equal(argv, controlArgv) {
			t.Fatalf("%s: argv = %q, want the uninherited %q", test.name, argv, controlArgv)
		}
		for _, option := range []string{"--effort", "--append-system-prompt-file", "--system-prompt-snapshot"} {
			if slices.Contains(execArgvTail(t, argv, aiModeClaude), option) {
				t.Fatalf("%s: argv %q carries %s", test.name, argv, option)
			}
		}
		if got, want := agentRecord(t, agent), agentRecord(t, controlAgent); got != want {
			t.Fatalf("%s: stored Agent:\n got %s\nwant %s", test.name, got, want)
		}
		// No `projmux: ` prefix: the split funnel adds it to the client line.
		want := "claude conversation " + personaResumeConversation +
			" opened without inherited launch values (launch-values-ambiguous): " +
			"agent/codex (uid:agt-alpha-codex), agent/codex (uid:agt-beta-codex) record different persona, system prompt snapshot or effort values\n"
		if stderr != want {
			t.Fatalf("%s: stderr = %q, want exactly %q", test.name, stderr, want)
		}
	}

	f := newPickerLaunchValuesFixture(t, planner)
	f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, bundle, nil)
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, bundle, map[string]string{"x": "y"})
	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	want := []string{"--effort", "low", "--append-system-prompt-file", snapshot,
		"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("agreeing holders: exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(agent.Metadata.Annotations, bundle) || stderr != "" {
		t.Fatalf("agreeing holders: annotations = %v stderr = %q, want %v and nothing", agent.Metadata.Annotations, stderr, bundle)
	}
}

// TestResumePickerDisclosesInheritedValuesItCannotRepass pins that a bundle
// is inherited verbatim even when its snapshot is gone or its effort is one
// Claude would not take, and that the picker then discloses the same
// persona-unavailable and effort-invalid lines `agent resume` would, naming
// the new Agent.
func TestResumePickerDisclosesInheritedValuesItCannotRepass(t *testing.T) {
	t.Parallel()
	f := newPickerLaunchValuesFixture(t, nil)
	bundle := map[string]string{
		coremetadata.AnnotationAgentPersona:       "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest: persona.Digest([]byte("a snapshot that was never written")),
		coremetadata.AnnotationAgentEffort:        effortInvalidFixture,
	}
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, bundle, nil)
	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--resume", personaResumeConversation}; !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(agent.Metadata.Annotations, bundle) {
		t.Fatalf("new Agent annotations = %v, want the bundle verbatim %v", agent.Metadata.Annotations, bundle)
	}
	// The seam's lines lose their `projmux: ` prefix on this path: the split
	// funnel prefixes the one client line it shows.
	personaLine := "agent/" + agent.Metadata.Name + " resumed without its persona go-reviewer (" + persona.ReasonUnavailable + "): "
	effortLine := strings.TrimPrefix(wantEffortInvalidNotice(agent.Metadata.Name, effortInvalidFixture), "projmux: ") + "\n"
	if strings.Count(stderr, "\n") != 2 || !strings.HasPrefix(stderr, personaLine) || !strings.HasSuffix(stderr, effortLine) ||
		strings.Contains(stderr, "projmux: ") {
		t.Fatalf("stderr = %q, want one %q line then %q, neither prefixed", stderr, personaLine, effortLine)
	}
}

// TestResumePickerInheritedValuesSurviveTheNewAgentsOwnResume chains the
// inherited annotations into `agent resume`: the new Agent resumes with the
// same bundle argv it was created with.
func TestResumePickerInheritedValuesSurviveTheNewAgentsOwnResume(t *testing.T) {
	t.Parallel()
	f := newPickerLaunchValuesFixture(t, nil)
	personaAnnotations, snapshot := createPersonaForResume(t, f.planner, "go-reviewer", []byte(personaResumeContent))
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, claudeLaunchBundle(personaAnnotations, "max"), nil)
	agent, argv, _ := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	created := execArgvTail(t, argv, aiModeClaude)
	want := []string{"--effort", "max", "--append-system-prompt-file", snapshot,
		"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if !slices.Equal(created, want) {
		t.Fatalf("picker exec argv tail = %q, want %q", created, want)
	}
	_, resumed, stderr := resumeClaudeAgentWithAnnotations(t, f.planner, agent.Metadata.Annotations)
	if got := execArgvTail(t, resumed, aiModeClaude); !slices.Equal(got, created) {
		t.Fatalf("agent resume exec argv tail = %q, want the picker's %q", got, created)
	}
	if strings.Contains(stderr, persona.ReasonUnavailable) || strings.Contains(stderr, claudeEffortReasonInvalid) {
		t.Fatalf("agent resume disclosed a lost launch value: %q", stderr)
	}
}

// TestResumePickerOfCodexOrAntigravityInheritsNothing pins that inheritance
// is Claude-only: a same-provider holder of the picked conversation carrying
// every launch-value key changes neither the argv nor the stored Agent.
func TestResumePickerOfCodexOrAntigravityInheritsNothing(t *testing.T) {
	t.Parallel()
	bundle := claudeLaunchBundle(map[string]string{
		coremetadata.AnnotationAgentPersona: "go-reviewer", coremetadata.AnnotationAgentPersonaDigest: "sha256:0",
	}, "low")
	for _, test := range []struct {
		provider, conversation, source string
	}{
		{aiModeCodex, resumeFixtureConversation, aisessions.SourceCodexRollout},
		{aiModeAntigravity, personaResumeConversation, ""},
	} {
		control := newPickerLaunchValuesFixture(t, nil)
		controlAgent, controlArgv, _ := control.pick(t, false, test.provider, test.conversation, test.source)

		f := newPickerLaunchValuesFixture(t, control.planner)
		f.hold(t, "agt-beta-codex", test.provider, test.conversation, bundle, nil)
		agent, argv, stderr := f.pick(t, false, test.provider, test.conversation, test.source)
		if !slices.Equal(argv, controlArgv) {
			t.Fatalf("%s: argv = %q, want the control %q", test.provider, argv, controlArgv)
		}
		if got, want := agentRecord(t, agent), agentRecord(t, controlAgent); got != want || agent.Metadata.Annotations != nil {
			t.Fatalf("%s: stored Agent:\n got %s\nwant %s", test.provider, got, want)
		}
		if stderr != "" {
			t.Fatalf("%s: stderr = %q, want nothing", test.provider, stderr)
		}
	}
}

// TestResumePickerAmbiguityReachesTheClientOnceThroughTheSplitFunnel drives
// the real split-UI funnel, (*aiCommand).createPaneFromIntent, over a real
// canonical create with two disagreeing holders: the pressing client gets one
// `projmux: ` line carrying launch-values-ambiguous exactly once, never a
// doubled `projmux: projmux:` prefix.
func TestResumePickerAmbiguityReachesTheClientOnceThroughTheSplitFunnel(t *testing.T) {
	t.Parallel()
	f := newPickerLaunchValuesFixture(t, nil)
	bundle := claudeLaunchBundle(map[string]string{
		coremetadata.AnnotationAgentPersona: "go-reviewer", coremetadata.AnnotationAgentPersonaDigest: "sha256:0",
	}, "low")
	f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, bundle, nil)
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, claudeLaunchBundle(bundle, "high"), nil)

	const client = "/dev/pts/9"
	var displayed [][]string
	ai := &aiCommand{
		panes: f.create,
		lookupEnv: func(key string) string {
			switch key {
			case canonicalCreateTargetClientEnv:
				return client
			case "TMUX_SPLIT_TARGET_PANE":
				return f.originID
			}
			return ""
		},
		// No client is attached, so the focus step has nothing to move and the
		// notice is the one line the funnel shows.
		readCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		runCommand: func(_ context.Context, name string, args ...string) error {
			displayed = append(displayed, append([]string{name}, args...))
			return nil
		},
	}
	if err := ai.createPaneFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
		conversationID: personaResumeConversation,
	}); err != nil {
		t.Fatalf("split UI funnel failed: %v", err)
	}
	if len(f.launcher.argv) != 1 {
		t.Fatalf("planned %d resume argv values, want 1", len(f.launcher.argv))
	}
	if len(displayed) != 1 {
		t.Fatalf("funnel tmux calls = %v, want exactly one display-message", displayed)
	}
	argv := displayed[0]
	line := argv[len(argv)-1]
	if argv[0] != "tmux" || argv[1] != "display-message" || argv[2] != "-c" || argv[3] != client {
		t.Fatalf("funnel display = %v, want one display-message on %s", argv, client)
	}
	if !strings.HasPrefix(line, "projmux: ") || strings.Count(line, launchValuesReasonAmbiguous) != 1 ||
		strings.Contains(line, "projmux: projmux:") {
		t.Fatalf("client line = %q, want one `projmux: ` line carrying %s exactly once", line, launchValuesReasonAmbiguous)
	}
}
