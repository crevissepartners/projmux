package app

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestResolveAgentWorkspaceUsesOnlyRegisteredProjectTrees(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	ownerRoot := filepath.Join(base, "owner")
	otherRoot := filepath.Join(base, "other")
	sibling := filepath.Join(base, "sibling")
	for _, path := range []string{ownerRoot, filepath.Join(ownerRoot, "sub"), otherRoot, sibling} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{
		{Metadata: coremetadata.ObjectMeta{UID: "owner"}, Spec: coremetadata.ProjectSpec{Root: ownerRoot}},
		{Metadata: coremetadata.ObjectMeta{UID: "other"}, Spec: coremetadata.ProjectSpec{Root: otherRoot}},
	}
	owner := registry.Projects[0]

	workspace, err := resolveAgentWorkspace(registry, owner, aiModeCodex, filepath.Join(ownerRoot, "sub", "..", "sub"), []string{otherRoot})
	if err != nil {
		t.Fatalf("valid workspace: %v", err)
	}
	if workspace.CWD != filepath.Join(ownerRoot, "sub") || !reflect.DeepEqual(workspace.AdditionalWritableRoots, []string{otherRoot}) {
		t.Fatalf("workspace = %+v", workspace)
	}

	escape := filepath.Join(ownerRoot, "escape")
	if err := os.Symlink(sibling, escape); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, provider, cwd string
		additional          []string
		want                string
	}{
		{name: "unregistered sibling", provider: aiModeCodex, cwd: sibling, want: "outside every registered Project root"},
		{name: "symlink escape", provider: aiModeCodex, cwd: escape, want: "outside every registered Project root"},
		{name: "nonexistent", provider: aiModeCodex, cwd: filepath.Join(ownerRoot, "missing"), want: "no such file"},
		{name: "unsupported provider", provider: aiModeAntigravity, cwd: ownerRoot, additional: []string{otherRoot}, want: "does not support additional writable roots"},
		{name: "duplicate effective root", provider: aiModeClaude, cwd: ownerRoot, additional: []string{ownerRoot}, want: "duplicates"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveAgentWorkspace(registry, owner, test.provider, test.cwd, test.additional)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAgentWorkspaceValidationPrecedesRegistryAndTmuxMutation(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	before := store.snapshot()
	_, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--cwd", "/definitely/not/a/project")
	if err == nil {
		t.Fatal("invalid explicit workspace succeeded")
	}
	if store.writes != 0 || store.snapshot() != before || tmuxMutationCallCount(tmux) != 0 || len(launcher.plans) != 0 {
		t.Fatalf("invalid workspace mutated state: writes=%d tmux-mutations=%d plans=%d", store.writes, tmuxMutationCallCount(tmux), len(launcher.plans))
	}
}

// TestAgentLaunchPassesExactCallerWorkspaceToCodexAndClaude asserts the launch
// delivers the caller's workspace and nothing else.
//
// It used to assert that the argv *contained* each expected token, which could
// not tell a delivered prompt from a prompt the provider absorbed as one more
// directory -- the installed regression this Phase repairs. The assertion is now
// the exact argv, and the provider-grammar replay in agent_launch_argv_test.go
// owns the parse-level guarantee.
func TestAgentLaunchPassesExactCallerWorkspaceToCodexAndClaude(t *testing.T) {
	t.Parallel()
	cmd := agentLaunchArgvTestCommand(t)
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra-a", "/work/extra-b"}}
	want := map[string][]string{
		aiModeCodex:  {"-C", "/work/owner", "--add-dir", "/work/extra-a", "--add-dir", "/work/extra-b", "task-token"},
		aiModeClaude: {"--add-dir", "/work/extra-a", "/work/extra-b", "--", "task-token"},
	}
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		_, argv, err := cmd.PlanAgentLaunch(provider, workspace, []string{"task-token"})
		if err != nil {
			t.Fatalf("%s plan: %v", provider, err)
		}
		got := execArgvTail(t, argv, provider)
		if !slices.Equal(got, want[provider]) {
			t.Fatalf("%s argv = %q, want %q", provider, got, want[provider])
		}
		// Nothing the caller did not ask for reaches the provider: no implicit
		// root, and no permission widening.
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"/work/implicit", "--dangerously-skip-permissions"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("%s argv widened with %q: %q", provider, forbidden, joined)
			}
		}
	}
}
