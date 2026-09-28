package app

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// agentGuidanceAgentLauncher is the create fake whose launch planning, label
// link rules and agent guidance are all the real planner's, so a create test
// asserts the exact provider argv.
type agentGuidanceAgentLauncher struct {
	*exactArgvAgentLauncher
}

func (l *agentGuidanceAgentLauncher) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	return l.planner.PlanProjectLinks(provider, project, recorded)
}

func (l *agentGuidanceAgentLauncher) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	return l.planner.PlanAgentGuidance(provider, recorded)
}

// agentGuidanceResumeLauncher is the exact-argv resume recorder with the
// production rules and guidance seams of its planner.
type agentGuidanceResumeLauncher struct {
	*projectLinksResumeLauncher
}

func (l *agentGuidanceResumeLauncher) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	return l.planner.PlanAgentGuidance(provider, recorded)
}

// turnAgentGuidanceOff writes a whitespace-only guidance file into planner's
// home, for a test about something other than the guidance that must keep the
// argv it had before the guidance existed.
func turnAgentGuidanceOff(t *testing.T, planner *aiCommand) {
	t.Helper()
	writeAgentGuidance(t, linkRulesPaths(t, planner), []byte(" \n"))
}

func writeAgentGuidance(t *testing.T, paths config.Paths, text []byte) {
	t.Helper()
	if err := agentguidance.NewDefaultStore(paths).Save(text); err != nil {
		t.Fatal(err)
	}
}

// agentGuidanceSnapshot is the guidance snapshot path and digest a launch
// with text is expected to use.
func agentGuidanceSnapshot(t *testing.T, paths config.Paths, text []byte) (string, string) {
	t.Helper()
	digest := agentguidance.Digest(text)
	path, err := agentguidance.NewDefaultStore(paths).SnapshotPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}

// joinSystemPrompt is the one system prompt file parts make.
func joinSystemPrompt(parts ...[]byte) []byte {
	return bytes.Join(parts, []byte(projectlinks.CompositeSeparator))
}

type agentGuidanceCreateFixture struct {
	create   *createCommand
	launcher *agentGuidanceAgentLauncher
	store    *fakeResourceStore
	planner  *aiCommand
	paths    config.Paths
	alpha    string
}

// newAgentGuidanceCreate is `create agent` over the re-UID'd fixture with the
// real launch planner, rules seam and guidance seam, all on one home.
func newAgentGuidanceCreate(t *testing.T) agentGuidanceCreateFixture {
	t.Helper()
	store := newFakeResourceStore(t)
	alpha, _ := reUIDLinkRulesProjects(t, store)
	create, fake := newTestAgentCreateCommand(t, store, newFakeTmux())
	planner := agentLaunchArgvTestCommand(t)
	create.homeDir, create.lookupEnv = planner.homeDir, planner.lookupEnv
	launcher := &agentGuidanceAgentLauncher{&exactArgvAgentLauncher{fakeAgentLauncher: fake, planner: planner}}
	create.agents = launcher
	return agentGuidanceCreateFixture{create: create, launcher: launcher, store: store, planner: planner,
		paths: linkRulesPaths(t, planner), alpha: alpha}
}

// run creates one Claude Agent in alpha and returns its exec argv tail, its
// annotations and stderr.
func (f agentGuidanceCreateFixture) run(t *testing.T, extra ...string) ([]string, map[string]string, string) {
	t.Helper()
	args := append([]string{"agent", "--provider", "claude"}, extra...)
	args = append(args, "--project", "alpha", "--window", "review", "--", "review this")
	_, stderr, err := runRoute(t, f.create, args...)
	if err != nil {
		t.Fatalf("create: stderr=%q err=%v", stderr, err)
	}
	if len(f.launcher.argv) != 1 {
		t.Fatalf("planned %d argv values, want 1", len(f.launcher.argv))
	}
	agent := agentNamed(t, f.store, "win-alpha-review", "agent-test-1")
	return execArgvTail(t, f.launcher.argv[0], aiModeClaude), agent.Metadata.Annotations, stderr
}

// TestCreateClaudeAgentWithoutAGuidanceFileLaunchesTheDefaultAndRecordsItsDigest
// is acceptance 1: no guidance file, and the one system prompt file starts
// with the default text; the Agent records its digest.
func TestCreateClaudeAgentWithoutAGuidanceFileLaunchesTheDefaultAndRecordsItsDigest(t *testing.T) {
	t.Parallel()
	f := newAgentGuidanceCreate(t)
	tail, annotations, stderr := f.run(t)
	path, digest := agentGuidanceSnapshot(t, f.paths, agentguidance.Default())
	if want := []string{"--append-system-prompt-file", path, "--", "review this"}; !slices.Equal(tail, want) || strings.Contains(stderr, agentGuidanceReasonUnavailable) {
		t.Fatalf("exec argv tail = %q stderr = %q, want %q", tail, stderr, want)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(content, agentguidance.Default()) {
		t.Fatalf("system prompt file = %q, %v; want the default guidance", content, err)
	}
	if want := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: digest}; !maps.Equal(annotations, want) {
		t.Fatalf("Agent annotations = %v, want %v", annotations, want)
	}
}

// TestCreateClaudeAgentWithAGuidanceFileLaunchesItsContentAndRecordsItsDigest
// is acceptance 2, first half: the file replaces the default verbatim and the
// digest follows it.
func TestCreateClaudeAgentWithAGuidanceFileLaunchesItsContentAndRecordsItsDigest(t *testing.T) {
	t.Parallel()
	f := newAgentGuidanceCreate(t)
	custom := []byte("Only ever talk to other agents through projmux.\n")
	writeAgentGuidance(t, f.paths, custom)
	tail, annotations, _ := f.run(t)
	path, digest := agentGuidanceSnapshot(t, f.paths, custom)
	if got := systemPromptFileOf(t, tail); got != path {
		t.Fatalf("exec argv tail = %q, want the custom guidance %s", tail, path)
	}
	if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, custom) {
		t.Fatalf("guidance snapshot = %q, %v; want the file verbatim", content, err)
	}
	if want := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: digest}; !maps.Equal(annotations, want) {
		t.Fatalf("Agent annotations = %v, want %v", annotations, want)
	}
}

// TestCreateClaudeAgentWithWhitespaceGuidanceIsTheArgvOfBefore is acceptance
// 2, second half: a whitespace-only file turns the guidance off, records
// nothing, and without a persona and rules the argv is exactly the one a
// launcher without the guidance seam -- the launch before this change --
// builds: no --append-system-prompt-file at all.
func TestCreateClaudeAgentWithWhitespaceGuidanceIsTheArgvOfBefore(t *testing.T) {
	t.Parallel()
	f := newAgentGuidanceCreate(t)
	writeAgentGuidance(t, f.paths, []byte("\n  \t\n"))
	tail, annotations, stderr := f.run(t)

	before := newAgentGuidanceCreate(t)
	before.create.agents = before.launcher.exactArgvAgentLauncher
	beforeTail, _, _ := before.run(t)
	if !slices.Equal(tail, beforeTail) || slices.Contains(tail, "--append-system-prompt-file") || strings.Contains(stderr, agentGuidanceReasonUnavailable) {
		t.Fatalf("exec argv tail = %q stderr = %q, want the argv of before %q", tail, stderr, beforeTail)
	}
	if annotations != nil {
		t.Fatalf("Agent annotations = %v, want none", annotations)
	}
	if entries, err := os.ReadDir(filepath.Join(f.paths.StateDir, agentguidance.DirName)); err == nil && len(entries) != 0 {
		t.Fatalf("guidance off wrote snapshots %v", entries)
	}
}

// agentGuidanceGoldenProjectUID is a fixed, validly shaped Project UID, so the
// rendered rules in the golden files never change between runs.
const agentGuidanceGoldenProjectUID = "proj-vyp7l4eev42ocufusci2aybg3a"

// TestAgentGuidanceComposedSystemPromptFileGolden is acceptance 3: the bytes
// of the one system prompt file a fresh Claude create passes, for every mix
// of guidance, persona and rules, pinned in testdata. The order is always
// guidance, persona, rules, joined by projectlinks.CompositeSeparator, and the
// argv carries exactly one --append-system-prompt-file.
// UPDATE_GOLDEN=1 rewrites the files.
func TestAgentGuidanceComposedSystemPromptFileGolden(t *testing.T) {
	t.Parallel()
	project := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: agentGuidanceGoldenProjectUID, Name: "golden"},
	}
	personaContent := []byte("PERSONA: you review diffs tersely.\n")
	for _, test := range []struct {
		name                     string
		guidance, persona, rules bool
	}{
		{"guidance", true, false, false},
		{"guidance-persona", true, true, false},
		{"guidance-rules", true, false, true},
		{"guidance-persona-rules", true, true, true},
		{"persona-rules", false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			planner := agentLaunchArgvTestCommand(t)
			paths := linkRulesPaths(t, planner)
			if !test.guidance {
				turnAgentGuidanceOff(t, planner)
			}
			if test.rules {
				writeLinkRules(t, paths, project.Metadata.UID, linkRulesAlpha)
			}
			create := &createCommand{homeDir: planner.homeDir, lookupEnv: planner.lookupEnv,
				agents: &agentGuidanceAgentLauncher{&exactArgvAgentLauncher{fakeAgentLauncher: newFakeAgentLauncher(), planner: planner}}}
			var flags resourceCreateFlags
			var parts [][]byte
			if test.guidance {
				parts = append(parts, agentguidance.Default())
			}
			if test.persona {
				if _, err := persona.NewDefaultStore(paths).Write("reviewer", personaContent); err != nil {
					t.Fatal(err)
				}
				launch, err := create.preparePersonaLaunch("create agent", "reviewer")
				if err != nil {
					t.Fatal(err)
				}
				flags.personaLaunch = launch
				parts = append(parts, personaContent)
			}
			if test.rules {
				parts = append(parts, projectlinks.Render(linkRulesAlpha, projectlinks.ProjectOf(project)))
			}
			create.prepareProjectLinks(aiModeClaude, project, &flags)
			create.prepareAgentGuidance(aiModeClaude, &flags)
			_, argv, err := create.planAgentPaneLaunch(aiModeClaude, coremetadata.AgentWorkspace{CWD: "/work/owner"}, flags)
			if err != nil {
				t.Fatal(err)
			}
			tail := execArgvTail(t, argv, aiModeClaude)
			file := systemPromptFileOf(t, tail)
			if file == "" {
				t.Fatalf("exec argv tail = %q passes no system prompt file", tail)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if want := joinSystemPrompt(parts...); !bytes.Equal(got, want) {
				t.Fatalf("system prompt file =\n%s\nwant\n%s", got, want)
			}
			golden := filepath.Join("testdata", "agent-guidance", test.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s golden mismatch (UPDATE_GOLDEN=1 rewrites it)\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
			}
		})
	}
}

// resumeClaudeAgentWithGuidance is resumeClaudeAgentWithLinkRules with the
// guidance seam on the resume launcher too.
func resumeClaudeAgentWithGuidance(t *testing.T, planner *aiCommand, store *fakeResourceStore, annotations map[string]string) ([]string, string) {
	t.Helper()
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	target.Metadata.Annotations = maps.Clone(annotations)

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	launcher := &agentGuidanceResumeLauncher{&projectLinksResumeLauncher{&exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}}}
	command.rebind.launcher = launcher
	stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
	if err != nil || !strings.Contains(stdout, "resumed") {
		t.Fatalf("resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(launcher.argv) != 1 {
		t.Fatalf("planned %d provider argv values, want 1", len(launcher.argv))
	}
	return execArgvTail(t, launcher.argv[0], aiModeClaude), stderr
}

// TestResumeComparesTheAgentGuidanceWithItsRecordedDigest is acceptance 4 for
// `agent resume`: an old Agent without the annotation launches the current
// guidance with the snapshot off and records it; the same digest passes the
// same content and adds no snapshot mode; changed guidance and guidance
// turned off launch the current state with the snapshot off and update or
// remove the record.
func TestResumeComparesTheAgentGuidanceWithItsRecordedDigest(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	defaultPath, defaultDigest := agentGuidanceSnapshot(t, paths, agentguidance.Default())
	off := coremetadata.SystemPromptSnapshotOff

	// An Agent from before the guidance.
	store := newFakeResourceStore(t)
	tail, stderr := resumeClaudeAgentWithGuidance(t, planner, store, nil)
	if want := []string{"--append-system-prompt-file", defaultPath, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(tail, want) || stderr != "" {
		t.Fatalf("old Agent: exec argv tail = %q stderr = %q, want %q", tail, stderr, want)
	}
	want := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: defaultDigest, coremetadata.AnnotationAgentSystemPromptSnapshot: off}
	if got := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(got, want) {
		t.Fatalf("old Agent: recorded %v, want %v", got, want)
	}

	// The same digest: the same file, no snapshot mode, nothing recorded.
	same := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: defaultDigest}
	store = newFakeResourceStore(t)
	tail, _ = resumeClaudeAgentWithGuidance(t, planner, store, same)
	if want := []string{"--append-system-prompt-file", defaultPath, "--resume", personaResumeConversation}; !slices.Equal(tail, want) {
		t.Fatalf("same digest: exec argv tail = %q, want %q", tail, want)
	}
	if got := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(got, same) {
		t.Fatalf("same digest: recorded %v, want unchanged %v", got, same)
	}

	// Changed guidance.
	custom := []byte("Changed guidance.\n")
	writeAgentGuidance(t, paths, custom)
	customPath, customDigest := agentGuidanceSnapshot(t, paths, custom)
	store = newFakeResourceStore(t)
	tail, _ = resumeClaudeAgentWithGuidance(t, planner, store, same)
	if want := []string{"--append-system-prompt-file", customPath, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(tail, want) {
		t.Fatalf("changed: exec argv tail = %q, want %q", tail, want)
	}
	want = map[string]string{coremetadata.AnnotationAgentGuidanceDigest: customDigest, coremetadata.AnnotationAgentSystemPromptSnapshot: off}
	if got := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(got, want) {
		t.Fatalf("changed: recorded %v, want %v", got, want)
	}

	// Guidance turned off.
	turnAgentGuidanceOff(t, planner)
	store = newFakeResourceStore(t)
	tail, _ = resumeClaudeAgentWithGuidance(t, planner, store, same)
	if want := []string{"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(tail, want) {
		t.Fatalf("off: exec argv tail = %q, want %q", tail, want)
	}
	want = map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: off}
	if got := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(got, want) {
		t.Fatalf("off: recorded %v, want %v", got, want)
	}

	// Off, and an Agent without the annotation: exactly the argv of before.
	store = newFakeResourceStore(t)
	if tail, _ = resumeClaudeAgentWithGuidance(t, planner, store, nil); !slices.Equal(tail, []string{"--resume", personaResumeConversation}) {
		t.Fatalf("off without a record: exec argv tail = %q", tail)
	}
	if got := recordedAgentAnnotations(store, "agt-beta-codex"); len(got) != 0 {
		t.Fatalf("off without a record: recorded %v", got)
	}
}

// TestResumeWithGuidancePersonaAndRulesPassesOneFileInOrder pins the resume
// side of the order: guidance, persona, rules, in one file.
func TestResumeWithGuidancePersonaAndRulesPassesOneFileInOrder(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	withPersona, _ := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)

	tail, _ := resumeClaudeAgentWithGuidance(t, planner, store, withPersona)
	got, err := os.ReadFile(systemPromptFileOf(t, tail))
	if err != nil {
		t.Fatal(err)
	}
	want := joinSystemPrompt(agentguidance.Default(), []byte(personaResumeContent), projectlinks.Render(linkRulesBeta, linkRulesProject(t, store, beta)))
	if !bytes.Equal(got, want) {
		t.Fatalf("system prompt file =\n%s\nwant\n%s", got, want)
	}
	recorded := recordedAgentAnnotations(store, "agt-beta-codex")
	if recorded[coremetadata.AnnotationAgentGuidanceDigest] != agentguidance.Digest(agentguidance.Default()) ||
		recorded[coremetadata.AnnotationAgentPersonaDigest] != withPersona[coremetadata.AnnotationAgentPersonaDigest] {
		t.Fatalf("recorded %v", recorded)
	}
}

// TestTopologyReplayComparesTheAgentGuidanceAndRecordsIt is acceptance 4 for
// the Continue/topology replay.
func TestTopologyReplayComparesTheAgentGuidanceAndRecordsIt(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	root := t.TempDir()
	path, digest := agentGuidanceSnapshot(t, paths, agentguidance.Default())

	work, plan := planClaudeTopologyReplay(t, planner, root, nil)
	if want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(execArgvTail(t, work.argv, aiModeClaude), want) || len(plan.notices) != 0 {
		t.Fatalf("replay exec argv = %q notices %v, want tail %q", work.argv, plan.notices, want)
	}
	registry := coremetadata.NewRegistry()
	registry.Agents = []coremetadata.Agent{{Metadata: coremetadata.ObjectMeta{UID: "agent-1", Name: "reviewer"}}}
	if err := work.guidance.record(&registry, coremetadata.Mutator{}, "agent-1"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: digest, coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	if got := registry.Agents[0].Metadata.Annotations; !maps.Equal(got, want) {
		t.Fatalf("replay recorded %v, want %v", got, want)
	}

	same, _ := planClaudeTopologyReplay(t, planner, root, map[string]string{coremetadata.AnnotationAgentGuidanceDigest: digest})
	if want := []string{"--append-system-prompt-file", path, "--resume", personaResumeConversation}; !slices.Equal(execArgvTail(t, same.argv, aiModeClaude), want) || same.guidance.changed() {
		t.Fatalf("same digest replay exec argv = %q, want tail %q", same.argv, want)
	}
}

// TestResumePickerLaunchesTheAgentGuidanceWithTheSnapshotOff is acceptance 4
// for the resume-picker create. The picked conversation's recorded system
// prompt cannot be proven to hold the guidance (the digest is not one of the
// inherited launch values, exactly like the rules digest), so guidance that
// is on launches with the snapshot off and is recorded; guidance that is off
// leaves the picker's argv and annotations exactly as before.
func TestResumePickerLaunchesTheAgentGuidanceWithTheSnapshotOff(t *testing.T) {
	for _, on := range []bool{true, false} {
		planner := agentLaunchArgvTestCommand(t)
		paths := linkRulesPaths(t, planner)
		if !on {
			turnAgentGuidanceOff(t, planner)
		}
		store := newFakeResourceStore(t)
		alpha, _ := reUIDLinkRulesProjects(t, store)
		tmux := newFakeTmux()
		session := tmux.addSession("alpha")
		seedOwnedSession(session, alpha, "/srv/alpha")
		seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
		create, _ := newTestAgentCreateCommand(t, store, tmux)
		launcher := &exactArgvResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), planner: planner}
		create.resumes = &agentGuidanceResumeLauncher{&projectLinksResumeLauncher{launcher}}
		originID := livePaneWithUID(t, tmux, "pan-alpha-zsh")
		withPopupOrigin(create, tmux, popupEnv(originID))
		f := pickerLaunchValuesFixture{
			canonicalRootFixture: canonicalRootFixture{store: store, tmux: tmux, create: create, originID: originID,
				windowUID: "win-alpha-main", rootKind: coremetadata.KindProject, rootUID: alpha},
			planner: planner, launcher: launcher,
		}

		agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
		tail := execArgvTail(t, argv, aiModeClaude)
		if !on {
			if want := []string{"--resume", personaResumeConversation}; !slices.Equal(tail, want) || len(agent.Metadata.Annotations) != 0 {
				t.Fatalf("off: picker exec argv tail = %q annotations %v, want %q and none", tail, agent.Metadata.Annotations, want)
			}
			continue
		}
		path, digest := agentGuidanceSnapshot(t, paths, agentguidance.Default())
		if want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(tail, want) || stderr != "" {
			t.Fatalf("picker exec argv tail = %q stderr = %q, want %q", tail, stderr, want)
		}
		want := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: digest, coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
		if !maps.Equal(agent.Metadata.Annotations, want) {
			t.Fatalf("picker Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
		}
	}
}

// unreadableAgentGuidance are the guidance files a launch cannot use: a
// directory where the file belongs, and a file past the size limit.
var unreadableAgentGuidance = map[string]func(t *testing.T, path string){
	"directory": func(t *testing.T, path string) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	},
	"oversize": func(t *testing.T, path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), agentguidance.MaxSize+1), 0o600); err != nil {
			t.Fatal(err)
		}
	},
}

// TestUnreadableAgentGuidanceLaunchesWithoutItWithOneNoticeAndRecordsNothing
// is acceptance 5: an unreadable or oversize guidance file never stops a
// create or a resume. The launch goes ahead without guidance, one notice
// names the reason, and nothing is recorded.
func TestUnreadableAgentGuidanceLaunchesWithoutItWithOneNoticeAndRecordsNothing(t *testing.T) {
	for name, breakFile := range unreadableAgentGuidance {
		t.Run(name, func(t *testing.T) {
			f := newAgentGuidanceCreate(t)
			breakFile(t, agentguidance.NewDefaultStore(f.paths).Path())
			tail, annotations, stderr := f.run(t)
			if slices.Contains(tail, "--append-system-prompt-file") {
				t.Fatalf("create exec argv tail = %q, want no system prompt file", tail)
			}
			// The fake pane never registers a Claude lease, so the create also
			// warns about that; the guidance notice is its own one line.
			wantNotice := "projmux: agent/agent-test-1 launched without the agent guidance (" + agentGuidanceReasonUnavailable + "): "
			if lines := guidanceNoticeLines(stderr); len(lines) != 1 || !strings.HasPrefix(lines[0], wantNotice) {
				t.Fatalf("create stderr = %q, want one line starting %q", stderr, wantNotice)
			}
			if annotations != nil {
				t.Fatalf("create recorded %v", annotations)
			}

			planner := agentLaunchArgvTestCommand(t)
			breakFile(t, agentguidance.NewDefaultStore(linkRulesPaths(t, planner)).Path())
			recorded := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: agentguidance.Digest(agentguidance.Default())}
			store := newFakeResourceStore(t)
			tail, stderr = resumeClaudeAgentWithGuidance(t, planner, store, recorded)
			if want := []string{"--resume", personaResumeConversation}; !slices.Equal(tail, want) {
				t.Fatalf("resume exec argv tail = %q, want %q", tail, want)
			}
			wantNotice = "projmux: agent/codex launched without the agent guidance (" + agentGuidanceReasonUnavailable + "): "
			if !strings.HasPrefix(stderr, wantNotice) || strings.Count(stderr, agentGuidanceReasonUnavailable) != 1 || strings.Count(stderr, "\n") != 1 {
				t.Fatalf("resume stderr = %q, want one line starting %q", stderr, wantNotice)
			}
			if got := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(got, recorded) {
				t.Fatalf("resume recorded %v, want unchanged %v", got, recorded)
			}
		})
	}
}

// TestAgentGuidanceLeavesCodexAndReplyOnlyLaunchesUnchanged is acceptance 7:
// the guidance is Claude-only. Codex, and the Claude reply-only lane, plan no
// guidance and launch with the argv they had before, even with a digest
// annotation recorded.
func TestAgentGuidanceLeavesCodexAndReplyOnlyLaunchesUnchanged(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	for _, provider := range []string{aiModeCodex, aiModeAntigravity} {
		if guidance := planner.PlanAgentGuidance(provider, nil); guidance.active {
			t.Fatalf("%s planned agent guidance: %+v", provider, guidance)
		}
	}
	if guidance := planner.PlanAgentGuidance(aiModeClaude, nil); guidance.digest != agentguidance.Digest(agentguidance.Default()) {
		t.Fatalf("claude guidance digest = %q", guidance.digest)
	}
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}
	annotations := map[string]string{coremetadata.AnnotationAgentGuidanceDigest: agentguidance.Digest(agentguidance.Default())}
	before, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, annotations)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.argv, before.argv) || after.agentGuidanceUnavailable != nil {
		t.Fatalf("codex resume argv with a guidance digest = %q, want %q", after.argv, before.argv)
	}

	create := &createCommand{agents: &agentGuidanceAgentLauncher{&exactArgvAgentLauncher{fakeAgentLauncher: newFakeAgentLauncher(), planner: planner}}}
	for name, flags := range map[string]resourceCreateFlags{
		"codex":      {},
		"reply-only": {dialogueReplyOnly: true},
	} {
		provider := aiModeClaude
		if name == "codex" {
			provider = aiModeCodex
		}
		create.prepareAgentGuidance(provider, &flags)
		if flags.agentGuidance.active || flags.resumeLaunchValues != nil {
			t.Fatalf("%s: prepared agent guidance %+v", name, flags.agentGuidance)
		}
	}
}

// guidanceNoticeLines are the stderr lines that disclose the guidance.
func guidanceNoticeLines(stderr string) []string {
	var lines []string
	for line := range strings.Lines(stderr) {
		if strings.Contains(line, agentGuidanceReasonUnavailable) {
			lines = append(lines, line)
		}
	}
	return lines
}
