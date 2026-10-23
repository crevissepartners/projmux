package app

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexPersonaContent is deliberately distinctive so an argv, a tmux call or a
// Registry document that leaked it is impossible to miss.
const codexPersonaContent = "CODEX-PERSONA-BODY-MARKER: you review diffs tersely.\n"

// newCodexPersonaCreate wires one canonical Agent create on the native Codex
// lane, with an isolated persona store.
func newCodexPersonaCreate(t *testing.T) (*createCommand, *fakeResourceStore, *fakeTmux, *fakeNativeThreadController, persona.Store) {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{
		createBinding: codexappserver.ThreadBinding{ThreadID: "thread-codex-persona", TurnID: "turn-codex-persona"},
	}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{
		fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{},
	}
	personas, _ := personaTestHome(t, create)
	return create, store, tmux, native, personas
}

// TestCreateCodexAgentWithPersonaSendsItAsDeveloperInstructions is acceptance
// 1: on the native fresh lane the persona snapshot content goes to
// thread/start as the thread's developer instructions, and the same create
// transaction records the two persona annotations on the Agent.
//
// The content is sent over the app-server connection and nowhere else. It must
// not reach the Pane argv (`ps` publishes that -- decision A2) nor the
// Registry, which is why only the name and the digest are recorded.
func TestCreateCodexAgentWithPersonaSendsItAsDeveloperInstructions(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "canonical", args: []string{"agent", "--provider", "codex", "--persona", "reviewer"}},
		{name: "provider shortcut", args: []string{"codex", "--persona", "reviewer"}},
		{name: "instructions canonical", args: []string{"agent", "--provider", "codex", "--instructions", "reviewer"}},
		{name: "instructions shortcut", args: []string{"codex", "--instructions", "reviewer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			create, store, tmux, native, personas := newCodexPersonaCreate(t)
			if _, err := personas.Write("reviewer", []byte(codexPersonaContent)); err != nil {
				t.Fatal(err)
			}

			argv := append(append([]string(nil), test.args...),
				"--project", "alpha", "--window", "main", "--", "review this")
			stdout, stderr, err := runRoute(t, create, argv...)
			// The deprecated spelling adds its one notice line to stderr and
			// changes nothing else.
			wantStderr := ""
			if slices.Contains(test.args, "--persona") {
				wantStderr = cli.DeprecatedPersonaFlagNotice + "\n"
			}
			if err != nil || stderr != wantStderr || strings.TrimSpace(stdout) == "" {
				t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}

			if len(native.creates) != 1 {
				t.Fatalf("native creates = %+v, want exactly one", native.creates)
			}
			if got := native.creates[0].instructions; got != codexPersonaContent {
				t.Fatalf("developer instructions = %q, want the snapshot content %q", got, codexPersonaContent)
			}
			if native.creates[0].prompt != "review this" {
				t.Fatalf("native create prompt = %q", native.creates[0].prompt)
			}

			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			digest := persona.Digest([]byte(codexPersonaContent))
			want := map[string]string{
				coremetadata.AnnotationAgentPersona:            "reviewer",
				coremetadata.AnnotationAgentPersonaDigest:      digest,
				coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceFlag,
			}
			want = confirmedCodexBaseAnnotations(want, codexPersonaContent)
			if len(agent.Metadata.Annotations) != len(want) {
				t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
			}
			for key, value := range want {
				if agent.Metadata.Annotations[key] != value {
					t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
				}
			}
			// The snapshot is still written, so `persona show` and a later
			// audit can find exactly what the thread was started with.
			snapshotPath, err := personas.SnapshotPath(digest)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(snapshotPath); err != nil || string(got) != codexPersonaContent {
				t.Fatalf("snapshot at %s = %q (%v)", snapshotPath, got, err)
			}

			// A2: the content reaches the app-server and nothing else.
			if tmuxCallsMention(tmux, "CODEX-PERSONA-BODY-MARKER") {
				t.Fatalf("persona content reached a tmux argv: %q", tmux.calls)
			}
			if tmuxCallsMention(tmux, "developer_instructions") ||
				tmuxCallsMention(tmux, "--append-system-prompt-file") {
				t.Fatalf("Codex persona reached the command line: %q", tmux.calls)
			}
			raw, err := json.Marshal(store.registry)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "CODEX-PERSONA-BODY-MARKER") {
				t.Fatal("persona content persisted in the Registry")
			}
		})
	}
}

// TestCreateCodexAgentWithoutPersonaSendsNoDeveloperInstructions is acceptance
// 2: a Codex create with no --persona is byte-for-byte the create it was
// before Codex could take one -- no developer instructions, no annotations.
func TestCreateCodexAgentWithoutPersonaSendsNoDeveloperInstructions(t *testing.T) {
	t.Parallel()
	create, store, _, native, _ := newCodexPersonaCreate(t)

	if _, _, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "review this"); err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 1 || native.creates[0].instructions != "" {
		t.Fatalf("native creates = %+v, want one carrying no developer instructions", native.creates)
	}
	if agent := agentNamed(t, store, "win-alpha-main", "agent-test-1"); !maps.Equal(agent.Metadata.Annotations, confirmedCodexBaseAnnotations(nil, "")) {
		t.Fatalf("Agent annotations = %#v, want nil", agent.Metadata.Annotations)
	}
}

// Plain creation carries the same role privately without changing control lanes.
func TestCodexPlainPersonaUsesPrivateBaseFileAndKeepsThePlainLane(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no prompt", args: []string{
			"agent", "--provider", "codex", "--persona", "reviewer", "--project", "alpha", "--window", "main"}},
		{name: "shortcut with no prompt", args: []string{
			"codex", "--persona", "reviewer", "--project", "alpha", "--window", "main"}},
		{name: "interactive-only with a prompt", args: []string{
			"agent", "--provider", "codex", "--interactive-only", "--persona", "reviewer",
			"--project", "alpha", "--window", "main", "--", "review this"}},
		{name: "shortcut interactive-only with a prompt", args: []string{
			"codex", "--interactive-only", "--persona", "reviewer",
			"--project", "alpha", "--window", "main", "--", "review this"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			create, store, tmux, native, personas := newCodexPersonaCreate(t)
			if _, err := personas.Write("reviewer", []byte(codexPersonaContent)); err != nil {
				t.Fatal(err)
			}

			withCodexGuidanceSeam(t, create)
			stdout, _, err := runRoute(t, create, test.args...)
			if err != nil {
				t.Fatalf("plain managed create: %v %s", err, stdout)
			}
			if len(native.creates) != 0 {
				t.Fatalf("plain lane became native: %+v", native.creates)
			}
			if tmuxCallsMention(tmux, codexPersonaContent) {
				t.Fatal("instruction content leaked into argv")
			}
			if !tmuxCallsMention(tmux, "model_instructions_file=") {
				t.Fatal("plain base snapshot not passed")
			}
			found := false
			for _, agent := range store.registry.Agents {
				if agent.Metadata.Annotations[coremetadata.AnnotationAgentPersona] == "reviewer" {
					found = true
				}
			}
			if !found {
				t.Fatal("plain role recipe missing")
			}

		})
	}
}

// TestPersonaLaneGateTable is the argv-only decision itself, read as a table.
// It runs before the persona file is read, so it is the one place the whole
// lane rule is visible at once.
func TestPersonaLaneGateTable(t *testing.T) {
	t.Parallel()
	prompted := resourceCreateFlags{payload: []string{"review this"}}
	for _, test := range []struct {
		name     string
		provider string
		flags    resourceCreateFlags
		refused  bool
	}{
		{name: "claude fresh", provider: aiModeClaude, flags: prompted},
		{name: "claude with no prompt", provider: aiModeClaude},
		{name: "claude reply-only", provider: aiModeClaude,
			flags: resourceCreateFlags{dialogueReplyOnly: true}, refused: true},
		{name: "codex reply-only with a prompt", provider: aiModeCodex,
			flags: resourceCreateFlags{payload: prompted.payload, dialogueReplyOnly: true}, refused: true},
		{name: "codex native fresh", provider: aiModeCodex, flags: prompted},
		{name: "codex with no prompt", provider: aiModeCodex},
		{name: "codex interactive-only", provider: aiModeCodex,
			flags: resourceCreateFlags{payload: prompted.payload, interactiveOnly: true}},
		{name: "codex resume", provider: aiModeCodex,
			flags:   resourceCreateFlags{payload: prompted.payload, resumeConversation: resumeFixtureConversation},
			refused: true},
		{name: "antigravity fresh", provider: aiModeAntigravity, flags: prompted, refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags := test.flags
			flags.persona = "reviewer"
			err := requirePersonaLane(canonicalCreateAgent, test.provider, flags)
			if test.refused {
				wantReason := persona.ReasonProviderUnsupported
				if test.name == "codex resume" {
					wantReason = personaReasonCodexInstructionsImmutable
				}
				if err == nil || !IsUsageError(err) ||
					!strings.Contains(err.Error(), wantReason) {
					t.Fatalf("err = %v, want a usage error carrying %s", err, wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the lane accepted", err)
			}
			// Without --persona the gate is never anything but silent.
			flags.persona = ""
			if err := requirePersonaLane(canonicalCreateAgent, test.provider, flags); err != nil {
				t.Fatalf("a create without --persona was refused: %v", err)
			}
		})
	}
}
