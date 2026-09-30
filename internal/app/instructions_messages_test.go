package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// TestInstructionsSpellingsSayInstructions pins the text the public spellings
// print about a named instructions file: `instructions`, `--instructions`,
// `agent relaunch`, `agent resume`, and create call it instructions. The
// reason tokens keep their persona- spelling, and the deprecated `persona`
// spellings keep the old noun (TestDeprecatedSpellingsRunAsBeforeWithOneNotice
// pins those).
//
// No t.Parallel: the environment swaps process environment, working directory
// and stdin.
func TestInstructionsSpellingsSayInstructions(t *testing.T) {
	env := newSettingsLayerGuardEnv(t)
	for _, test := range []struct {
		argv []string
		// errText is a substring of the returned error.
		errText string
		usage   bool
	}{
		{[]string{"instructions", "show", "absent"},
			"instructions show: " + persona.ReasonNotFound + `: instructions "absent" does not exist at `, false},
		{[]string{"instructions", "delete", "absent", "--yes"},
			"instructions delete: " + persona.ReasonNotFound + `: instructions "absent" does not exist at `, false},
		{[]string{"instructions", "show", "-x"},
			"instructions show: " + persona.ReasonNameInvalid + `: instructions "-x" `, true},
		{[]string{"agent", "relaunch", "no-such-agent", "--instructions", "../x"},
			"--instructions: " + persona.ReasonNameInvalid + `: instructions "../x" `, true},
		// `agent instructions attach` is deprecated, but its text already said
		// instructions where `agent persona attach` says persona.
		{[]string{"agent", "instructions", "attach", "no-such-agent", "../x"},
			"agent instructions attach: " + persona.ReasonNameInvalid + `: instructions "../x" `, true},
		{[]string{"agent", "persona", "attach", "no-such-agent", "../x"},
			"agent persona attach: " + persona.ReasonNameInvalid + `: persona "../x" `, true},
	} {
		_, _, err := runDeprecatedSpellingRoute(env, test.argv...)
		if err == nil || !strings.Contains(err.Error(), test.errText) || IsUsageError(err) != test.usage {
			t.Errorf("%q = %v, want a usage=%t error containing %q", test.argv, err, test.usage, test.errText)
		}
	}
}

// TestInstructionsEditKeepsItsSpellingInARefusal pins the noun of the one
// refusal `edit` prints itself: instructions on `instructions edit`, persona
// on the deprecated `persona edit`. Both keep the edited copy under a
// projmux-instructions- temporary directory.
func TestInstructionsEditKeepsItsSpellingInARefusal(t *testing.T) {
	t.Parallel()
	for noun, want := range map[string]string{
		"instructions": `instructions edit: ` + persona.ReasonTooLarge + `: instructions "reviewer" is `,
		"persona":      `persona edit: ` + persona.ReasonTooLarge + `: persona "reviewer" is `,
	} {
		cmd, _ := newPersonaTestCommand(t, map[string]string{"EDITOR": "ed"}, "")
		cmd.noun = noun
		var seen string
		cmd.editorRunner = editWith(bytes.Repeat([]byte("x"), persona.MaxSize+1), &seen)
		_, _, err := runPersona(cmd, "edit", "reviewer")
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s edit = %v, want it to start %q", noun, err, want)
		}
		// The kept copy is in a temporary directory named for instructions.
		if !strings.HasPrefix(filepath.Base(filepath.Dir(seen)), "projmux-instructions-") || !strings.Contains(err.Error(), seen) {
			t.Errorf("%s edit kept %q, error %v", noun, seen, err)
		}
		_ = os.RemoveAll(filepath.Dir(seen))
	}
}

// TestCreateInstructionsRefusalsNameTheOptionAndTheNoun pins the create text:
// the option the caller used, and the noun of its spelling. A launch that
// names no option is --instructions.
func TestCreateInstructionsRefusalsNameTheOptionAndTheNoun(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	create := &createCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	for option, want := range map[string]string{
		"":             `create agent --instructions: ` + persona.ReasonNotFound + `: instructions "absent" does not exist at `,
		"instructions": `create agent --instructions: ` + persona.ReasonNotFound + `: instructions "absent" does not exist at `,
		"profile":      `create agent --profile: ` + persona.ReasonNotFound + `: instructions "absent" does not exist at `,
		"persona":      `create agent --persona: ` + persona.ReasonNotFound + `: persona "absent" does not exist at `,
	} {
		_, err := create.preparePersonaLaunch("create agent", "absent", option)
		if err == nil || !strings.HasPrefix(err.Error(), want) || !IsUsageError(err) {
			t.Errorf("preparePersonaLaunch(%q) = %v, want a usage error starting %q", option, err, want)
		}
	}

	err := requirePersonaLane("create agent", "antigravity", resourceCreateFlags{persona: "reviewer"})
	want := "create agent --instructions applies only to --provider claude and --provider codex (" + persona.ReasonProviderUnsupported + "); nothing was created"
	if err == nil || err.Error() != want {
		t.Errorf("requirePersonaLane without an option = %v, want %q", err, want)
	}
}

// TestLaunchAndResumeTextSaysInstructions pins the launch planner refusals, the
// resume disclosure, the resume-picker ambiguity subject, and the missing
// snapshot error.
func TestLaunchAndResumeTextSaysInstructions(t *testing.T) {
	t.Parallel()
	ai := &aiCommand{}
	if _, _, err := ai.PlanAgentLaunchWithSettings("antigravity", coremetadata.AgentWorkspace{}, nil, "", "", "", ""); err == nil ||
		err.Error() != `provider "antigravity" does not accept --model, --effort, or --instructions` {
		t.Errorf("antigravity launch = %v", err)
	}
	if _, _, err := ai.PlanAgentLaunchWithSettings(aiModeCodex, coremetadata.AgentWorkspace{}, nil, "", "", "/x.md", ""); err == nil ||
		err.Error() != `provider "codex" does not accept Claude instructions or settings options` {
		t.Errorf("codex launch with an instructions file = %v", err)
	}

	_, unavailable := ai.resumePersonaSnapshot("antigravity", map[string]string{coremetadata.AnnotationAgentPersona: "go-reviewer"})
	notice := agentResumeLaunch{personaUnavailable: unavailable}.personaNotice("main/reviewer")
	if want := "projmux: agent/main/reviewer resumed without its instructions go-reviewer (" + persona.ReasonUnavailable +
		"): is not re-passed: instructions apply only to --provider claude"; notice != want {
		t.Errorf("resume notice = %q, want %q", notice, want)
	}

	if got := ambiguousLaunchValueSubject(aiModeCodex); got != "instructions values" {
		t.Errorf("codex ambiguity subject = %q", got)
	}
	if got := ambiguousLaunchValueSubject(aiModeClaude); got != "instructions, system prompt snapshot or effort values" {
		t.Errorf("claude ambiguity subject = %q", got)
	}

	missing := filepath.Join(t.TempDir(), "gone", "sha256-00.md")
	if _, err := readPersonaSnapshot(missing); err == nil || err.Error() != "no instructions snapshot at "+missing {
		t.Errorf("readPersonaSnapshot(missing dir) = %v", err)
	}
	missing = filepath.Join(t.TempDir(), "sha256-00.md")
	if _, err := readPersonaSnapshot(missing); err == nil || err.Error() != "no instructions snapshot at "+missing {
		t.Errorf("readPersonaSnapshot(missing file) = %v", err)
	}
}
