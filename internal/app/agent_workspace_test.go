package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestAgentLaunchPassesExactCallerWorkspaceToCodexAndClaude(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{
		"codex":  writeExecutable(t, filepath.Join(binDir, "codex")),
		"claude": writeExecutable(t, filepath.Join(binDir, "claude")),
	}
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			return []byte(binaries[args[1]] + "\n"), nil
		}
		return nil, os.ErrNotExist
	}
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra-a", "/work/extra-b"}}
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		_, argv, err := cmd.PlanAgentLaunch(provider, workspace, []string{"task-token"})
		if err != nil {
			t.Fatalf("%s plan: %v", provider, err)
		}
		joined := strings.Join(argv, " ")
		for _, required := range []string{"/work/owner", "--add-dir", "/work/extra-a", "/work/extra-b", "task-token"} {
			if !strings.Contains(joined, required) {
				t.Fatalf("%s argv = %q, missing %q", provider, joined, required)
			}
		}
		if provider == aiModeCodex && !strings.Contains(joined, "-C") {
			t.Fatalf("codex argv = %q, missing -C", joined)
		}
		if provider == aiModeClaude && strings.Contains(joined, " -C ") {
			t.Fatalf("claude argv received Codex-only -C: %q", joined)
		}
		for _, forbidden := range []string{"/work/implicit", "--dangerously-skip-permissions"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("%s argv widened with %q: %q", provider, forbidden, joined)
			}
		}
	}
}
