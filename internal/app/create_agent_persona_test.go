package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// personaTestHome points one create command at an isolated HOME and returns
// the persona store that HOME resolves to and its snapshot directory.
func personaTestHome(t *testing.T, create *createCommand) (persona.Store, string) {
	t.Helper()
	home := t.TempDir()
	create.homeDir = func() (string, error) { return home, nil }
	create.lookupEnv = func(string) string { return "" }
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	return persona.NewDefaultStore(paths), filepath.Join(paths.StateDir, persona.DirName)
}

func tmuxCallsMention(tmux *fakeTmux, text string) bool {
	return slices.ContainsFunc(tmux.calls, func(call []string) bool {
		return strings.Contains(strings.Join(call, " "), text)
	})
}

// TestCreateClaudeAgentWithPersonaLaunchesTheSnapshotAndAnnotatesTheAgent is
// C-1's Guarantee: the launch is given the snapshot path (never the content),
// the snapshot's bytes hash to the recorded digest, and the same create writes
// both annotation keys on the Agent and neither on its Pane.
func TestCreateClaudeAgentWithPersonaLaunchesTheSnapshotAndAnnotatesTheAgent(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	personas, snapshotDir := personaTestHome(t, create)
	content := []byte("PERSONA-BODY-MARKER: you review diffs tersely.\n")
	if _, err := personas.Write("reviewer", content); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--persona", "reviewer", "--model", "sonnet",
		"--project", "alpha", "--window", "review", "--", "review this"); err != nil {
		t.Fatal(err)
	}

	digest := persona.Digest(content)
	wantPath, err := personas.SnapshotPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(wantPath, filepath.Join(snapshotDir, "sha256-")) {
		t.Fatalf("snapshot path %q is not below %s", wantPath, snapshotDir)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].personaFile != wantPath || launcher.plans[0].model != "sonnet" {
		t.Fatalf("plans = %+v, want persona file %s", launcher.plans, wantPath)
	}
	snapshot, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot, content) || persona.Digest(snapshot) != digest {
		t.Fatalf("snapshot bytes = %q", snapshot)
	}

	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	want := map[string]string{
		coremetadata.AnnotationAgentPersona:       "reviewer",
		coremetadata.AnnotationAgentPersonaDigest: persona.Digest(snapshot),
	}
	if len(agent.Metadata.Annotations) != len(want) {
		t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
	}
	for key, value := range want {
		if agent.Metadata.Annotations[key] != value {
			t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
		}
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatalf("Agent pane %q missing", agent.Status.PaneRef)
	}
	for key := range want {
		if _, found := pane.Metadata.Annotations[key]; found {
			t.Fatalf("Pane carries %s: %v", key, pane.Metadata.Annotations)
		}
	}

	// A7: the provider sees the path; the content never reaches argv.
	if !tmuxCallsMention(tmux, "--append-system-prompt-file "+wantPath) {
		t.Fatalf("no tmux launch carried the snapshot path; calls = %q", tmux.calls)
	}
	if tmuxCallsMention(tmux, "PERSONA-BODY-MARKER") {
		t.Fatal("persona content reached a tmux argv")
	}
	raw, err := json.Marshal(store.registry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PERSONA-BODY-MARKER") {
		t.Fatal("persona content persisted in the Registry")
	}

	// A4: editing the persona afterwards does not touch the started snapshot.
	if _, err := personas.Write("reviewer", []byte("edited later")); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(wantPath); !bytes.Equal(after, content) {
		t.Fatalf("editing the persona changed the snapshot: %q", after)
	}
}

// TestClaudePersonaLaunchPutsOnlyTheSnapshotPathBeforeTheWorkspace pins the
// real launcher's argv: the path goes with --model/--effort, ahead of Claude's
// variadic --add-dir, and nothing else is added.
func TestClaudePersonaLaunchPutsOnlyTheSnapshotPathBeforeTheWorkspace(t *testing.T) {
	t.Parallel()
	cmd := agentLaunchArgvTestCommand(t)
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}
	snapshot := "/state/projmux/personas/sha256-" + strings.Repeat("a", 64) + ".md"
	_, argv, err := cmd.PlanAgentLaunchWithOptions(aiModeClaude, workspace, []string{"do the thing"}, "sonnet", "low", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got := execArgvTail(t, argv, aiModeClaude)
	want := []string{"--model", "sonnet", "--effort", "low", "--append-system-prompt-file", snapshot, "--add-dir", "/work/extra", "--", "do the thing"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv tail = %q, want %q", got, want)
	}
	_, argv, err = cmd.PlanAgentLaunchWithOptions(aiModeClaude, workspace, nil, "", "", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got = execArgvTail(t, argv, aiModeClaude)
	want = []string{"--append-system-prompt-file", snapshot, "--add-dir", "/work/extra"}
	if !slices.Equal(got, want) {
		t.Fatalf("persona-only argv tail = %q, want %q", got, want)
	}
	if _, _, err := cmd.PlanAgentLaunchWithOptions(aiModeCodex, workspace, nil, "", "", snapshot); err == nil {
		t.Fatal("codex accepted a Claude persona file")
	}
}

// TestCreateAgentPersonaRefusalsLeaveNoTrace is C-1's and C-5's refusal half:
// every refusal names its reason token, is a usage error, and leaves nothing
// in the Registry, in tmux, or in StateDir.
func TestCreateAgentPersonaRefusalsLeaveNoTrace(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"codex provider", []string{"agent", "--provider", "codex", "--persona", "reviewer"}, persona.ReasonProviderUnsupported},
		{"codex interactive-only", []string{"agent", "--provider", "codex", "--interactive-only", "--persona", "reviewer"}, persona.ReasonProviderUnsupported},
		{"codex shortcut", []string{"codex", "--persona", "reviewer"}, persona.ReasonProviderUnsupported},
		{"antigravity provider", []string{"agent", "--provider", "antigravity", "--persona", "reviewer"}, persona.ReasonProviderUnsupported},
		{"dialogue reply-only", []string{"agent", "--provider", "claude", "--" + claudeDialogueReplyOnlyFlag, "--persona", "reviewer"}, persona.ReasonProviderUnsupported},
		{"missing persona", []string{"agent", "--provider", "claude", "--persona", "absent"}, persona.ReasonNotFound},
		{"case differs", []string{"agent", "--provider", "claude", "--persona", "Reviewer"}, persona.ReasonNotFound},
		{"oversized persona", []string{"claude", "--persona", "huge"}, persona.ReasonTooLarge},
		{"path traversal", []string{"agent", "--provider", "claude", "--persona", "../reviewer"}, persona.ReasonNameInvalid},
		{"hidden name", []string{"agent", "--provider", "claude", "--persona", ".reviewer"}, persona.ReasonNameInvalid},
		{"flag-like name", []string{"agent", "--provider", "claude", "--persona", "-reviewer"}, persona.ReasonNameInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, launcher := newTestAgentCreateCommand(t, store, tmux)
			personas, snapshotDir := personaTestHome(t, create)
			if _, err := personas.Write("reviewer", []byte("valid persona")); err != nil {
				t.Fatal(err)
			}
			hugePath := filepath.Join(personas.Dir(), "huge.md")
			if err := os.WriteFile(hugePath, bytes.Repeat([]byte("h"), persona.MaxSize+1), 0o600); err != nil {
				t.Fatal(err)
			}
			before, panes := store.snapshot(), tmux.paneCount()

			argv := append(append([]string(nil), test.args...), "--project", "alpha", "--window", "review")
			stdout, _, err := runRoute(t, create, argv...)
			if err == nil {
				t.Fatal("create succeeded")
			}
			if !IsUsageError(err) || !strings.Contains(err.Error(), test.reason) || !strings.Contains(err.Error(), "nothing was created") {
				t.Fatalf("err = %v, want a usage error carrying %s", err, test.reason)
			}
			if stdout != "" {
				t.Fatalf("refused create wrote %q", stdout)
			}
			if store.snapshot() != before || store.writes != 0 || tmux.paneCount() != panes ||
				tmux.argvContains("split-window") || tmux.argvContains("new-window") || tmux.argvContains("new-session") {
				t.Fatalf("refused create mutated state: writes=%d registry=%s", store.writes, store.snapshot())
			}
			if len(launcher.plans) != 0 {
				t.Fatalf("refused create planned a launch: %+v", launcher.plans)
			}
			if _, err := os.Stat(snapshotDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused create touched %s: %v", snapshotDir, err)
			}
		})
	}
}

// TestCreateAgentWithoutPersonaIsUnchanged is C-1's non-regression: without
// --persona no annotation map is created, the plain launch is planned, and
// nothing is read from or written to the persona directories.
func TestCreateAgentWithoutPersonaIsUnchanged(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	personas, snapshotDir := personaTestHome(t, create)

	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--project", "alpha", "--window", "review", "--", "review this"); err != nil {
		t.Fatal(err)
	}
	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	if agent.Metadata.Annotations != nil {
		t.Fatalf("Agent annotations = %#v, want nil", agent.Metadata.Annotations)
	}
	raw, err := json.Marshal(agent.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "annotations") || strings.Contains(string(raw), "persona") {
		t.Fatalf("Agent metadata gained persona state: %s", raw)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].personaFile != "" {
		t.Fatalf("plans = %+v", launcher.plans)
	}
	for _, dir := range []string{personas.Dir(), snapshotDir} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a create without --persona touched %s: %v", dir, err)
		}
	}
}

// TestCreateWindowDoesNotAcceptPersona keeps --persona off the Window route,
// which has no Agent tuning flags at all.
func TestCreateWindowDoesNotAcceptPersona(t *testing.T) {
	t.Parallel()
	_, err := parseResourceCreateFlags(canonicalCreateWindow,
		[]string{"--provider", "claude", "--persona", "reviewer"}, &bytes.Buffer{}, resourceCreateShape{initialProvider: true})
	if err == nil || !strings.Contains(err.Error(), "persona") {
		t.Fatalf("create window accepted --persona: %v", err)
	}
}

// TestPersonaAnnotationsMergeWithTheCreatorKeys is the merge table: neither
// stays nil, each alone keeps its own keys, and both together record both sets
// without writing into the creator's map.
func TestPersonaAnnotationsMergeWithTheCreatorKeys(t *testing.T) {
	t.Parallel()
	creator := coremetadata.CreatorAnnotations("agent-creator", "pane-creator")
	withPersona := personaLaunch{name: "reviewer", snapshot: persona.Snapshot{Digest: persona.Digest([]byte("x"))}}
	personaKeys := map[string]string{
		coremetadata.AnnotationAgentPersona:       "reviewer",
		coremetadata.AnnotationAgentPersonaDigest: persona.Digest([]byte("x")),
	}

	if got := (personaLaunch{}).withAnnotations(nil); got != nil {
		t.Fatalf("no creator, no persona = %#v, want nil", got)
	}
	if got := (personaLaunch{}).withAnnotations(creator); !maps.Equal(got, creator) {
		t.Fatalf("creator only = %v, want %v", got, creator)
	}
	if got := withPersona.withAnnotations(nil); !maps.Equal(got, personaKeys) {
		t.Fatalf("persona only = %v, want %v", got, personaKeys)
	}
	before := maps.Clone(creator)
	both := withPersona.withAnnotations(creator)
	want := maps.Clone(creator)
	maps.Copy(want, personaKeys)
	if !maps.Equal(both, want) {
		t.Fatalf("creator and persona = %v, want %v", both, want)
	}
	if !maps.Equal(creator, before) {
		t.Fatalf("merging wrote into the creator map: %v", creator)
	}
}

// TestPersonaCreateFromAnAgentPaneRecordsCreatorAndPersona runs a persona
// create inside an Agent Pane, where the creator is recorded: the new Agent
// carries both key sets, and its Pane carries the creator keys only.
func TestPersonaCreateFromAnAgentPaneRecordsCreatorAndPersona(t *testing.T) {
	t.Parallel()
	fx := newCreatorFixture(t)
	home := t.TempDir()
	fx.command.homeDir = func() (string, error) { return home, nil }
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("creator and persona\n")
	if _, err := persona.NewDefaultStore(paths).Write("reviewer", content); err != nil {
		t.Fatal(err)
	}
	before := fx.agentUIDs()
	stdout, stderr, err := runRoute(t, fx.command,
		"agent", "--provider", "claude", "--persona", "reviewer",
		"--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id")
	if err != nil || stderr != "" || stdout == "" {
		t.Fatalf("create = stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	agents, panes := fx.newAgentsSince(t, before)
	if len(agents) != 1 {
		t.Fatalf("new Agents = %d, want 1", len(agents))
	}
	creator := coremetadata.CreatorAnnotations(fx.creatorAgent, fx.creatorPane)
	want := maps.Clone(creator)
	want[coremetadata.AnnotationAgentPersona] = "reviewer"
	want[coremetadata.AnnotationAgentPersonaDigest] = persona.Digest(content)
	if got := agents[0].Metadata.Annotations; !maps.Equal(got, want) {
		t.Fatalf("new Agent annotations = %v, want %v", got, want)
	}
	if got := creatorKeysOf(panes[0].Metadata); !maps.Equal(got, creator) {
		t.Fatalf("new Agent Pane creator keys = %v, want %v", got, creator)
	}
	for _, key := range []string{coremetadata.AnnotationAgentPersona, coremetadata.AnnotationAgentPersonaDigest} {
		if _, found := panes[0].Metadata.Annotations[key]; found {
			t.Fatalf("Pane carries %s: %v", key, panes[0].Metadata.Annotations)
		}
	}
}
