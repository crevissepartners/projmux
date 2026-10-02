package app

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// codexIdentityHeading opens the identity paragraph and appears nowhere else
// in the developer instructions.
const codexIdentityHeading = "# Your projmux identity"

// TestCodexAgentIdentityIsThePinnedParagraph pins the identity paragraph byte
// for byte, ending with one newline after the last bullet like the default
// guidance.
func TestCodexAgentIdentityIsThePinnedParagraph(t *testing.T) {
	t.Parallel()
	want := "# Your projmux identity\n" +
		"\n" +
		"Your projmux Agent UID is `agent-abc`.\n" +
		"\n" +
		"- When you create an agent with `projmux create agent`, add `--creator uid:agent-abc` so projmux records you as its creator.\n" +
		"- When you send a message with `projmux agent message send`, add `--source uid:agent-abc` so the message is sent from you.\n" +
		"- Your shell commands run outside your own pane, in an app server shared with other agents, so projmux cannot tell your pane or agent from the environment: name the pane or agent a command acts on. Add `--socket projmux` only to a command that defines that flag, such as `projmux delete pane` or `projmux agent relaunch`; do not add it to `projmux create agent`, which has no such flag and uses the `projmux` app socket itself.\n"
	if got := codexAgentIdentity("agent-abc"); got != want {
		t.Fatalf("codexAgentIdentity = %q, want %q", got, want)
	}
}

// TestCodexIdentitySocketAdviceMatchesTheCommandFlags holds the identity
// paragraph's `--socket` advice to the public flag parser: every command it
// names as taking `--socket projmux` parses it, and the command it names as
// not taking it refuses it. Both lists come from the constants the paragraph
// is built from. A trailing undefined flag stops every argv in flag parsing,
// so no case reaches the Registry or tmux, and the error names the first
// undefined flag: the trailing one when `--socket` parsed, `-socket` when it
// did not.
func TestCodexIdentitySocketAdviceMatchesTheCommandFlags(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	const end = "--zz-socket-probe-end"
	parse := func(command string) error {
		t.Helper()
		argv := append(strings.Fields(command), "--socket", "projmux", end)
		err := New().Run(argv, io.Discard, io.Discard)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("projmux %s --socket projmux %s error = %v, want a flag parse usage error", command, end, err)
		}
		return err
	}
	identity := codexAgentIdentity("agent-abc")
	for _, command := range codexSocketCommands {
		if !strings.Contains(identity, "`projmux "+command+"`") {
			t.Fatalf("identity = %q, want it to name `projmux %s`", identity, command)
		}
		if err := parse(command); !strings.Contains(err.Error(), "flag provided but not defined: -"+strings.TrimPrefix(end, "--")) {
			t.Errorf("projmux %s refused --socket projmux (%v), but the Codex identity paragraph tells agents to pass it", command, err)
		}
	}
	if !strings.Contains(identity, "do not add it to `projmux "+codexNoSocketCommand+"`") {
		t.Fatalf("identity = %q, want it to tell agents not to pass --socket to `projmux %s`", identity, codexNoSocketCommand)
	}
	if err := parse(codexNoSocketCommand); !strings.Contains(err.Error(), "flag provided but not defined: -socket") {
		t.Errorf("projmux %s accepted --socket projmux (%v), but the Codex identity paragraph tells agents it has no such flag", codexNoSocketCommand, err)
	}
}

// TestCodexFreshCreateSendsItsOwnAgentUIDRightAfterTheGuidance is the
// acceptance of the identity: on both native fresh create routes, with the
// guidance on, the thread starts with the guidance, then the identity naming
// that Agent's own Registry UID, then the instructions and the Project's
// rules when it has them, byte for byte.
func TestCodexFreshCreateSendsItsOwnAgentUIDRightAfterTheGuidance(t *testing.T) {
	for _, route := range codexLinkRulesRoutes {
		for _, withRules := range []bool{false, true} {
			name := route.name + "/no rules"
			if withRules {
				name = route.name + "/rules"
			}
			t.Run(name, func(t *testing.T) {
				var snapshotPath string
				result := route.run(t, func(setup codexLinkRulesSetup) {
					writeCodexGuidancePersona(t, setup.personas)
					if withRules {
						writeLinkRules(t, setup.paths, setup.alpha, linkRulesAlpha)
						snapshotPath, _ = linkRulesSnapshot(t, setup.paths, linkRulesAlpha, setup.project)
					}
				}, true)
				uid := result.agent.Metadata.UID
				if uid == "" {
					t.Fatalf("Agent %+v has no UID", result.agent.Metadata)
				}
				identity := codexAgentIdentity(uid)
				parts := [][]byte{agentguidance.Default(), []byte(identity), []byte(codexPersonaContent)}
				if withRules {
					rules, err := os.ReadFile(snapshotPath)
					if err != nil || len(rules) == 0 {
						t.Fatalf("rules snapshot %s = %q, %v", snapshotPath, rules, err)
					}
					parts = append(parts, rules)
				}
				want := string(joinSystemPrompt(parts...))
				if got := onlyNativeInstructions(t, result); got != want {
					t.Fatalf("developer instructions = %q, want %q", got, want)
				}
				for _, needle := range []string{"`" + uid + "`", "--creator uid:" + uid, "--source uid:" + uid, "--socket projmux"} {
					if !strings.Contains(identity, needle) {
						t.Fatalf("identity = %q, want it to carry %q", identity, needle)
					}
				}
				if result.leaked(codexIdentityHeading) {
					t.Fatal("identity text reached a tmux call or the Registry")
				}
			})
		}
	}
}

// TestCodexFreshCreateWithTheGuidanceOffSendsNoIdentity pins that the
// identity goes only with the guidance: with the guidance off both routes
// send the persona alone, or nothing, exactly as before.
func TestCodexFreshCreateWithTheGuidanceOffSendsNoIdentity(t *testing.T) {
	for _, route := range codexGuidanceRoutes {
		for _, withPersona := range []bool{false, true} {
			name := route.name + "/no persona"
			if withPersona {
				name = route.name + "/persona"
			}
			t.Run(name, func(t *testing.T) {
				result := route.run(t, func(paths config.Paths, personas persona.Store) {
					writeAgentGuidance(t, paths, []byte{})
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
			})
		}
	}
}

// TestCodexFreshCreateWithUnreadableGuidanceSendsNoIdentity pins that
// guidance a create cannot read leaves the identity out too, with the one
// guidance notice it always had.
func TestCodexFreshCreateWithUnreadableGuidanceSendsNoIdentity(t *testing.T) {
	for breakName, breakFile := range unreadableAgentGuidance {
		for _, route := range codexGuidanceRoutes {
			t.Run(breakName+"/"+route.name, func(t *testing.T) {
				result := route.run(t, func(paths config.Paths, personas persona.Store) {
					breakFile(t, agentguidance.NewDefaultStore(paths).Path())
					writeCodexGuidancePersona(t, personas)
				}, true)
				got := onlyNativeInstructions(t, result)
				if got != codexPersonaContent || strings.Contains(got, codexIdentityHeading) {
					t.Fatalf("developer instructions = %q, want %q", got, codexPersonaContent)
				}
				if lines := guidanceNoticeLines(result.stderr); len(lines) != 1 {
					t.Fatalf("stderr = %q, want one guidance notice", result.stderr)
				}
			})
		}
	}
}

// TestCodexDeveloperInstructionsPutTheIdentityBetweenTheGuidanceAndTheRest is
// the composition table: a launch that sends the guidance puts the identity
// right after it and rest last; any other launch returns rest itself.
func TestCodexDeveloperInstructionsPutTheIdentityBetweenTheGuidanceAndTheRest(t *testing.T) {
	t.Parallel()
	guidance := []byte("GUIDANCE\n")
	on := agentGuidanceLaunch{active: true, digest: "sha256-guidance", text: guidance}
	unavailable := on
	unavailable.unavailable = os.ErrPermission
	identity := codexAgentIdentity("agent-abc")
	sep := projectlinks.CompositeSeparator
	for _, test := range []struct {
		name   string
		launch agentGuidanceLaunch
		rest   string
		want   string
	}{
		{name: "not applicable", launch: agentGuidanceLaunch{}, rest: "REST", want: "REST"},
		{name: "inactive", launch: agentGuidanceLaunch{digest: "sha256-guidance", text: guidance}, rest: "REST", want: "REST"},
		{name: "unavailable", launch: unavailable, rest: "REST", want: "REST"},
		{name: "off", launch: agentGuidanceLaunch{active: true}, rest: "REST", want: "REST"},
		{name: "off without rest", launch: agentGuidanceLaunch{active: true}, want: ""},
		{name: "on without rest", launch: on, want: "GUIDANCE\n" + sep + identity},
		{name: "on with rest", launch: on, rest: "REST", want: "GUIDANCE\n" + sep + identity + sep + "REST"},
	} {
		if got := test.launch.codexDeveloperInstructions("agent-abc", test.rest); got != test.want {
			t.Errorf("%s: codexDeveloperInstructions(%q) = %q, want %q", test.name, test.rest, got, test.want)
		}
	}
}
