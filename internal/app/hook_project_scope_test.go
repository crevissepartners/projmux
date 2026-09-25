package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// hookScopeFixture lays out <home>/root/.projmux/config.toml with a
// post-create hook and an empty <home>/root/child directory, the shape where
// the hook runner and the hook CLI used to disagree: the runner reads only the
// session directory's own .projmux/config.toml, while the CLI walks up to the
// nearest marker.
func hookScopeFixture(t *testing.T) (home, root, child string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, "root")
	child = filepath.Join(root, "child")
	mustMkdirAll(t, child)
	writeHookFile(t, filepath.Join(root, ".projmux", "config.toml"), `
[hooks.post-create]
run = "echo root-post-create"
`)
	return home, root, child
}

func runHookScopeCommand(t *testing.T, home, wd string, args ...string) string {
	t.Helper()
	cmd, _, _ := newHookTestCommand(t, home, "", "")
	cmd.getwd = func() (string, error) { return wd, nil }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run(args, &stdout, &stderr); err != nil {
		t.Fatalf("hook %v error = %v (stderr=%q)", args, err, stderr.String())
	}
	return stdout.String()
}

// TestHookCLIParentProjectConfigSaysChildSessionsDoNotRunIt pins C-2: from a
// child directory without its own config, every project view names the root
// file but says sessions created in the child do not run it, because the
// runner reads only <session dir>/.projmux/config.toml.
func TestHookCLIParentProjectConfigSaysChildSessionsDoNotRunIt(t *testing.T) {
	t.Parallel()
	home, root, child := hookScopeFixture(t)
	rootConfig := filepath.Join(root, ".projmux", "config.toml")

	for _, args := range [][]string{
		{"list"},
		{"list", "--project"},
		{"list", "--effective"},
		{"validate"},
	} {
		out := runHookScopeCommand(t, home, child, args...)
		if !strings.Contains(out, rootConfig) {
			t.Fatalf("hook %v stdout does not name the root config %q:\n%s", args, rootConfig, out)
		}
		want := "note: sessions created in " + child + " do not run this file's pre-create, post-create, or post-attach hooks, [startup], or [env]; only sessions created in " + root + " do"
		if !strings.Contains(out, want) {
			t.Fatalf("hook %v stdout missing scope note %q:\n%s", args, want, out)
		}
	}
}

// TestHookCLIScopeNoteLeavesSendNotiAlone keeps the note from claiming that
// send-noti skips the root file: the send-noti dispatcher resolves the
// project root on its own and does run the root's send-noti hook.
func TestHookCLIScopeNoteLeavesSendNotiAlone(t *testing.T) {
	t.Parallel()
	home, _, child := hookScopeFixture(t)
	out := runHookScopeCommand(t, home, child, "list", "--project")
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "note:") && strings.Contains(line, "send-noti") {
			t.Fatalf("scope note mentions send-noti: %q", line)
		}
	}
}

// TestHookCLIRootWorkingDirHasNoScopeNote keeps the root view unchanged: from
// the directory that owns the config there is nothing to explain.
func TestHookCLIRootWorkingDirHasNoScopeNote(t *testing.T) {
	t.Parallel()
	home, root, _ := hookScopeFixture(t)
	for _, args := range [][]string{
		{"list"},
		{"list", "--project"},
		{"list", "--effective"},
		{"validate"},
	} {
		out := runHookScopeCommand(t, home, root, args...)
		if !strings.Contains(out, filepath.Join(root, ".projmux", "config.toml")) {
			t.Fatalf("hook %v stdout does not name the root config:\n%s", args, out)
		}
		if strings.Contains(out, "note:") {
			t.Fatalf("hook %v from the root printed a scope note:\n%s", args, out)
		}
	}
}

// TestHookCLIChildOwnConfigIsTheContext pins the runner rule from the other
// side: a child with its own .projmux/config.toml is the context, because that
// is the file sessions created there read.
func TestHookCLIChildOwnConfigIsTheContext(t *testing.T) {
	t.Parallel()
	home, _, child := hookScopeFixture(t)
	childConfig := filepath.Join(child, ".projmux", "config.toml")
	writeHookFile(t, childConfig, `
[hooks.post-attach]
run = "echo child-post-attach"
`)
	out := runHookScopeCommand(t, home, child, "list", "--project")
	if !strings.Contains(out, "project config: "+childConfig) {
		t.Fatalf("stdout does not use the child config:\n%s", out)
	}
	if strings.Contains(out, "root-post-create") || strings.Contains(out, "note:") {
		t.Fatalf("stdout shows the parent config or a scope note:\n%s", out)
	}
}

// TestHookCLIExplicitProjmuxCWDHasNoScopeNote keeps the hook-child path
// unchanged: PROJMUX_CWD is taken as the context directory as is.
func TestHookCLIExplicitProjmuxCWDHasNoScopeNote(t *testing.T) {
	t.Parallel()
	home, root, child := hookScopeFixture(t)
	cmd, _, _ := newHookTestCommand(t, home, root, "")
	cmd.getwd = func() (string, error) { return child, nil }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--project"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook list --project error = %v (stderr=%q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "root-post-create") || strings.Contains(stdout.String(), "note:") {
		t.Fatalf("PROJMUX_CWD=root view changed:\n%s", stdout.String())
	}
}

// TestHookTrustFromChildStillTargetsRoot keeps the A′ trade-off: trust and
// edit keep finding the root from a child directory.
func TestHookTrustFromChildStillTargetsRoot(t *testing.T) {
	t.Parallel()
	home, root, child := hookScopeFixture(t)
	out := runHookScopeCommand(t, home, child, "trust")
	if !strings.HasPrefix(out, "trusted "+root+"\n") {
		t.Fatalf("hook trust from child = %q, want it to trust %s", out, root)
	}
}
