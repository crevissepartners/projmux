package app

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexGuidanceContent is a custom guidance file, distinctive so a tmux call
// or a Registry document that leaked it is impossible to miss.
const codexGuidanceContent = "CODEX-GUIDANCE-BODY-MARKER: delegate through projmux.\n"

// codexGuidanceAgentLauncher is the Codex create fake whose Codex guidance
// seam is the real planner's, on the create's own home.
type codexGuidanceAgentLauncher struct {
	*fakeAgentLauncher
	planner *aiCommand
}

func (l *codexGuidanceAgentLauncher) PlanCodexAgentGuidance() agentGuidanceLaunch {
	return l.planner.PlanCodexAgentGuidance()
}

// codexGuidanceRoute is one way a Codex fresh create reaches the native lane:
// the typed `create agent` route or the UI create intent.
type codexGuidanceRoute struct {
	name string
	// run creates one prompted Codex Agent, with the persona "reviewer" when
	// withPersona, and returns its native creates, the Agent, stderr, and
	// whether the guidance marker reached a tmux call or the Registry.
	run func(t *testing.T, prepare func(paths config.Paths, personas persona.Store), withPersona bool) codexGuidanceResult
}

type codexGuidanceResult struct {
	creates []fakeNativeCreate
	agent   coremetadata.Agent
	stderr  string
	leaked  func(marker string) bool
}

// withCodexGuidanceSeam gives create the real Codex guidance seam on its home
// and returns that home's paths.
func withCodexGuidanceSeam(t *testing.T, create *createCommand) config.Paths {
	t.Helper()
	planner := agentLaunchArgvTestCommand(t)
	planner.homeDir, planner.lookupEnv = create.homeDir, create.lookupEnv
	create.agents = &codexGuidanceAgentLauncher{fakeAgentLauncher: create.agents.(*fakeAgentLauncher), planner: planner}
	return linkRulesPaths(t, planner)
}

func codexGuidanceLeakCheck(tmux *fakeTmux, store *fakeResourceStore) func(string) bool {
	return func(marker string) bool {
		raw, err := json.Marshal(store.registry)
		return tmuxCallsMention(tmux, marker) || err != nil || strings.Contains(string(raw), marker)
	}
}

var codexGuidanceRoutes = []codexGuidanceRoute{
	{name: "create agent", run: func(t *testing.T, prepare func(config.Paths, persona.Store), withPersona bool) codexGuidanceResult {
		t.Helper()
		create, store, tmux, native, personas := newCodexPersonaCreate(t)
		prepare(withCodexGuidanceSeam(t, create), personas)
		var flags []string
		if withPersona {
			flags = []string{"--persona", "reviewer"}
		}
		_, stderr, err := runRoute(t, create, codexNativeCreateArgs(flags...)...)
		if err != nil {
			t.Fatalf("create: stderr=%q err=%v", stderr, err)
		}
		return codexGuidanceResult{creates: native.creates, agent: agentNamed(t, store, "win-alpha-main", "agent-test-1"),
			stderr: stderr, leaked: codexGuidanceLeakCheck(tmux, store)}
	}},
	{name: "create intent", run: func(t *testing.T, prepare func(config.Paths, persona.Store), withPersona bool) codexGuidanceResult {
		t.Helper()
		fx := canonicalFixture(t, false)
		native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-intent-guidance", TurnID: "turn-intent-guidance"}}
		fx.create.codexNative = native
		fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
		personas, _ := personaTestHome(t, fx.create)
		paths := withCodexGuidanceSeam(t, fx.create)
		prepare(paths, personas)
		flags := resourceCreateFlags{payload: []string{"review this"}}
		if withPersona {
			// The UI create takes a persona only through its profile.
			writeCodexProfile(t, profile.NewDefaultStore(paths), "instructed", "provider = \"codex\"\ninstructions = \"reviewer\"\n")
			flags.profile = "instructed"
		}
		intent := agentPaneIntent{producer: canonicalProducerDirectProvider, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID}
		scope, err := fx.create.resolveCanonicalIntentScope(intent)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if _, err := fx.create.createCanonicalIntentAgent(scope, intent, aiModeCodex, "", flags, &stdout, &stderr); err != nil {
			t.Fatalf("create: stderr=%q err=%v", stderr.String(), err)
		}
		agents := fx.store.registry.AgentsOf(fx.windowUID)
		return codexGuidanceResult{creates: native.creates, agent: agents[len(agents)-1],
			stderr: stderr.String(), leaked: codexGuidanceLeakCheck(fx.tmux, fx.store)}
	}},
}

func writeCodexGuidancePersona(t *testing.T, personas persona.Store) {
	t.Helper()
	if _, err := personas.Write("reviewer", []byte(codexPersonaContent)); err != nil {
		t.Fatal(err)
	}
}

// onlyNativeInstructions is the developer instructions of the one native
// create result made.
func onlyNativeInstructions(t *testing.T, result codexGuidanceResult) string {
	t.Helper()
	if len(result.creates) != 1 || result.creates[0].prompt != "review this" {
		t.Fatalf("native creates = %+v, want exactly one carrying the prompt", result.creates)
	}
	return result.creates[0].instructions
}

// TestCreateCodexAgentWithoutAGuidanceFileSendsTheDefaultAsDeveloperInstructions
// is Codex acceptance 1: with no guidance file a Codex fresh create starts its
// thread with the default text, ahead of the persona and separated from it by
// the separator a Claude composite uses, and the Agent records its digest.
// The text reaches the app-server and nothing else (A2).
func TestCreateCodexAgentWithoutAGuidanceFileSendsTheDefaultAsDeveloperInstructions(t *testing.T) {
	for _, route := range codexGuidanceRoutes {
		for _, withPersona := range []bool{false, true} {
			name := route.name + "/no persona"
			if withPersona {
				name = route.name + "/persona"
			}
			t.Run(name, func(t *testing.T) {
				result := route.run(t, func(_ config.Paths, personas persona.Store) {
					if withPersona {
						writeCodexGuidancePersona(t, personas)
					}
				}, withPersona)
				want := string(agentguidance.Default())
				if withPersona {
					want = string(joinSystemPrompt(agentguidance.Default(), []byte(codexPersonaContent)))
				}
				if got := onlyNativeInstructions(t, result); got != want {
					t.Fatalf("developer instructions = %q, want %q", got, want)
				}
				digest := agentguidance.Digest(agentguidance.Default())
				if got := result.agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]; got != digest {
					t.Fatalf("Agent annotations = %v, want guidance digest %s", result.agent.Metadata.Annotations, digest)
				}
				if withPersona && result.agent.Metadata.Annotations[coremetadata.AnnotationAgentPersonaDigest] != persona.Digest([]byte(codexPersonaContent)) {
					t.Fatalf("Agent annotations = %v, want the persona digest kept", result.agent.Metadata.Annotations)
				}
				if result.leaked("Working with other agents through projmux") {
					t.Fatal("guidance text reached a tmux call or the Registry")
				}
				if lines := guidanceNoticeLines(result.stderr); len(lines) != 0 {
					t.Fatalf("stderr = %q, want no guidance notice", result.stderr)
				}
			})
		}
	}
}

// TestCreateCodexAgentWithAGuidanceFileSendsItsContentAndRecordsItsDigest is
// Codex acceptance 1 with a guidance file: its content replaces the default
// in the developer instructions, byte for byte, and the digest follows it.
func TestCreateCodexAgentWithAGuidanceFileSendsItsContentAndRecordsItsDigest(t *testing.T) {
	for _, route := range codexGuidanceRoutes {
		t.Run(route.name, func(t *testing.T) {
			result := route.run(t, func(paths config.Paths, personas persona.Store) {
				writeAgentGuidance(t, paths, []byte(codexGuidanceContent))
				writeCodexGuidancePersona(t, personas)
			}, true)
			want := codexGuidanceContent + projectlinks.CompositeSeparator + codexPersonaContent
			if got := onlyNativeInstructions(t, result); got != want {
				t.Fatalf("developer instructions = %q, want %q", got, want)
			}
			digest := agentguidance.Digest([]byte(codexGuidanceContent))
			if got := result.agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]; got != digest {
				t.Fatalf("Agent annotations = %v, want guidance digest %s", result.agent.Metadata.Annotations, digest)
			}
			if result.leaked("CODEX-GUIDANCE-BODY-MARKER") || result.leaked("CODEX-PERSONA-BODY-MARKER") {
				t.Fatal("guidance or persona text reached a tmux call or the Registry")
			}
		})
	}
}

// TestCreateCodexAgentWithWhitespaceGuidanceSendsWhatItSentBefore is Codex
// acceptance 2: with the guidance off a Codex fresh create sends the persona
// alone, or no developer instructions at all, and records exactly what it
// recorded before the guidance reached Codex.
func TestCreateCodexAgentWithWhitespaceGuidanceSendsWhatItSentBefore(t *testing.T) {
	for _, route := range codexGuidanceRoutes {
		for _, withPersona := range []bool{false, true} {
			name := route.name + "/no persona"
			if withPersona {
				name = route.name + "/persona"
			}
			t.Run(name, func(t *testing.T) {
				result := route.run(t, func(paths config.Paths, personas persona.Store) {
					writeAgentGuidance(t, paths, []byte(" \n\t\n"))
					if withPersona {
						writeCodexGuidancePersona(t, personas)
					}
				}, withPersona)
				want := ""
				if withPersona {
					want = codexPersonaContent
				}
				if got := onlyNativeInstructions(t, result); got != want {
					t.Fatalf("developer instructions = %q, want %q", got, want)
				}
				annotations := result.agent.Metadata.Annotations
				if _, ok := annotations[coremetadata.AnnotationAgentGuidanceDigest]; ok {
					t.Fatalf("Agent annotations = %v, want no guidance digest", annotations)
				}
				if route.name == "create agent" {
					var wantAnnotations map[string]string
					if withPersona {
						wantAnnotations = map[string]string{
							coremetadata.AnnotationAgentPersona:            "reviewer",
							coremetadata.AnnotationAgentPersonaDigest:      persona.Digest([]byte(codexPersonaContent)),
							coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceFlag,
						}
					}
					if !maps.Equal(annotations, wantAnnotations) || (wantAnnotations == nil) != (annotations == nil) {
						t.Fatalf("Agent annotations = %#v, want %#v", annotations, wantAnnotations)
					}
				}
			})
		}
	}
}

// TestUnreadableCodexAgentGuidanceCreatesWithoutItWithOneNoticeAndRecordsNothing
// is Codex acceptance 3: a guidance file that is a directory or past the size
// limit never stops a Codex create. The thread starts with the persona alone
// (or nothing), one notice names the reason, and no digest is recorded.
func TestUnreadableCodexAgentGuidanceCreatesWithoutItWithOneNoticeAndRecordsNothing(t *testing.T) {
	for breakName, breakFile := range unreadableAgentGuidance {
		for _, route := range codexGuidanceRoutes {
			for _, withPersona := range []bool{false, true} {
				name := breakName + "/" + route.name + "/no persona"
				if withPersona {
					name = breakName + "/" + route.name + "/persona"
				}
				t.Run(name, func(t *testing.T) {
					result := route.run(t, func(paths config.Paths, personas persona.Store) {
						breakFile(t, agentguidance.NewDefaultStore(paths).Path())
						if withPersona {
							writeCodexGuidancePersona(t, personas)
						}
					}, withPersona)
					want := ""
					if withPersona {
						want = codexPersonaContent
					}
					if got := onlyNativeInstructions(t, result); got != want {
						t.Fatalf("developer instructions = %q, want %q", got, want)
					}
					wantNotice := "agent/" + result.agent.Metadata.Name + " launched without the agent guidance (" + agentGuidanceReasonUnavailable + "): "
					if lines := guidanceNoticeLines(result.stderr); len(lines) != 1 || !strings.Contains(lines[0], wantNotice) {
						t.Fatalf("stderr = %q, want one line carrying %q", result.stderr, wantNotice)
					}
					if _, ok := result.agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]; ok {
						t.Fatalf("Agent annotations = %v, want no guidance digest", result.agent.Metadata.Annotations)
					}
				})
			}
		}
	}
}

// TestCodexCreatesOffTheNativeFreshLaneGetNoGuidance is Codex acceptance 4: a
// Codex create without a prompt, or with --interactive-only, starts no thread
// of its own, so it plans no guidance and records no digest even though the
// guidance is on.
func TestCodexCreatesOffTheNativeFreshLaneGetNoGuidance(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no prompt", args: []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main"}},
		{name: "interactive-only", args: []string{"agent", "--provider", "codex", "--interactive-only",
			"--project", "alpha", "--window", "main", "--", "review this"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			create, store, tmux, native, _ := newCodexPersonaCreate(t)
			withCodexGuidanceSeam(t, create)
			_, stderr, err := runRoute(t, create, test.args...)
			if err != nil {
				t.Fatalf("create: stderr=%q err=%v", stderr, err)
			}
			if len(native.creates) != 0 {
				t.Fatalf("native creates = %+v, want none", native.creates)
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			if _, ok := agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]; ok {
				t.Fatalf("Agent annotations = %v, want no guidance digest", agent.Metadata.Annotations)
			}
			if codexGuidanceLeakCheck(tmux, store)("Working with other agents through projmux") || len(guidanceNoticeLines(stderr)) != 0 {
				t.Fatalf("guidance reached the create: stderr=%q", stderr)
			}
		})
	}
}
