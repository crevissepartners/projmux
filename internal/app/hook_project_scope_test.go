package app

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
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

// hookScopeNoteText is the #1253 en-US scope note for a child and its root.
func hookScopeNoteText(child, root string) string {
	return "note: sessions created in " + child + " do not run this file's pre-create, post-create, or post-attach hooks, [startup], or [env]; only sessions created in " + root + " do"
}

// runHookScopeCommandWith is runHookScopeCommand with stdin and extra env,
// for the edit routes.
func runHookScopeCommandWith(t *testing.T, home, wd, stdin string, env map[string]string, args ...string) string {
	t.Helper()
	cmd, _, _ := newHookTestCommand(t, home, "", stdin)
	cmd.getwd = func() (string, error) { return wd, nil }
	base := cmd.lookupEnv
	cmd.lookupEnv = func(name string) string {
		if value, ok := env[name]; ok {
			return value
		}
		return base(name)
	}
	cmd.editorRunner = func(string, []string, io.Writer, io.Writer) error { return nil }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run(args, &stdout, &stderr); err != nil {
		t.Fatalf("hook %v error = %v (stderr=%q)", args, err, stderr.String())
	}
	return stdout.String()
}

// TestHookTrustUntrustEditFromChildShowScopeNote pins C-1 for the routes that
// act on the project context: from a child directory without its own config,
// trust, untrust, and project edit still target the root and print the same
// scope note list and validate print, exactly once.
func TestHookTrustUntrustEditFromChildShowScopeNote(t *testing.T) {
	t.Parallel()
	home, root, child := hookScopeFixture(t)
	rootConfig := filepath.Join(root, ".projmux", "config.toml")
	note := hookScopeNoteText(child, root)
	editor := map[string]string{"EDITOR": "true"}

	for _, tc := range []struct {
		args   []string
		stdin  string
		env    map[string]string
		target string
	}{
		{args: []string{"trust"}, target: "trusted " + root + "\n"},
		{args: []string{"untrust"}, target: "untrusted " + root + "\n"},
		{args: []string{"untrust"}, target: "no trust entry for " + root + "\n"},
		{args: []string{"edit", "post-create"}, stdin: "echo edited\n", target: "wrote " + rootConfig + "\n"},
		{args: []string{"edit", "--project", "pre-create"}, stdin: "echo forced\n", target: "wrote " + rootConfig + "\n"},
		{args: []string{"edit", "post-create"}, target: "no change\n"},
		{args: []string{"edit", "--editor", "post-create"}, env: editor, target: "edited " + rootConfig + "\n"},
		{args: []string{"edit", "--project", "--editor", "post-attach"}, env: editor, target: "edited " + rootConfig + "\n"},
	} {
		out := runHookScopeCommandWith(t, home, child, tc.stdin, tc.env, tc.args...)
		if !strings.Contains(out, tc.target) {
			t.Fatalf("hook %v stdout does not act on the root (want %q):\n%s", tc.args, tc.target, out)
		}
		if got := strings.Count(out, note); got != 1 {
			t.Fatalf("hook %v stdout has %d scope notes, want 1 (%q):\n%s", tc.args, got, note, out)
		}
		if !strings.HasSuffix(out, note+"\n") {
			t.Fatalf("hook %v stdout does not end with the scope note:\n%s", tc.args, out)
		}
	}
}

// TestHookTrustUntrustEditNamedTargetsHaveNoScopeNote keeps C-1's
// Non-Guarantee: from the root, under PROJMUX_CWD, with an explicit
// <project>, or with --global the user already named the target, so trust,
// untrust, and edit print no scope note.
func TestHookTrustUntrustEditNamedTargetsHaveNoScopeNote(t *testing.T) {
	t.Parallel()
	home, root, child := hookScopeFixture(t)
	editor := map[string]string{"EDITOR": "true"}
	atRoot := map[string]string{"PROJMUX_CWD": root}

	for _, tc := range []struct {
		name  string
		wd    string
		stdin string
		env   map[string]string
		args  []string
	}{
		{name: "root trust", wd: root, args: []string{"trust"}},
		{name: "root untrust", wd: root, args: []string{"untrust"}},
		{name: "root edit", wd: root, stdin: "echo root\n", args: []string{"edit", "post-create"}},
		{name: "root edit --editor", wd: root, env: editor, args: []string{"edit", "--editor", "post-create"}},
		{name: "PROJMUX_CWD trust", wd: child, env: atRoot, args: []string{"trust"}},
		{name: "PROJMUX_CWD untrust", wd: child, env: atRoot, args: []string{"untrust"}},
		{name: "PROJMUX_CWD edit", wd: child, env: atRoot, stdin: "echo env\n", args: []string{"edit", "post-create"}},
		{name: "explicit trust", wd: child, args: []string{"trust", root}},
		{name: "explicit untrust", wd: child, args: []string{"untrust", root}},
		{name: "global edit", wd: child, stdin: "echo global\n", args: []string{"edit", "--global", "post-create"}},
		{name: "global edit --editor", wd: child, env: editor, args: []string{"edit", "--global", "--editor", "post-create"}},
	} {
		out := runHookScopeCommandWith(t, home, tc.wd, tc.stdin, tc.env, tc.args...)
		if strings.Contains(out, "note:") {
			t.Fatalf("%s (hook %v) printed a scope note:\n%s", tc.name, tc.args, out)
		}
	}
}

// TestHookScopeNoteRendersFromCatalog pins the note behind
// i18n.KeyHookProjectScopeNote: the en-US entry is byte-identical to the
// #1253 text, and a ko-KR locale renders the translated entry with the same
// paths.
func TestHookScopeNoteRendersFromCatalog(t *testing.T) {
	t.Parallel()
	text, err := i18n.NewLocalizer(i18n.FallbackLocale).Text(i18n.KeyHookProjectScopeNote)
	if err != nil {
		t.Fatalf("en-US %s: %v", i18n.KeyHookProjectScopeNote, err)
	}
	if got := text.String(); got != hookProjectScopeNoteFallback {
		t.Fatalf("en-US %s = %q, want the fallback %q", i18n.KeyHookProjectScopeNote, got, hookProjectScopeNoteFallback)
	}

	home, root, child := hookScopeFixture(t)
	if got, want := runHookScopeCommand(t, home, child, "validate"), hookScopeNoteText(child, root)+"\n"; !strings.HasSuffix(got, want) {
		t.Fatalf("en-US validate note changed; stdout:\n%s\nwant suffix %q", got, want)
	}

	korean := map[string]string{"LANG": "ko_KR.UTF-8"}
	out := runHookScopeCommandWith(t, home, child, "", korean, "trust")
	want := "참고: " + child + "에서 만든 세션은 이 파일의 pre-create, post-create, post-attach hook과 [startup], [env]를 실행하지 않습니다. " + root + "에서 만든 세션만 실행합니다\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("ko-KR trust note:\n%s\nwant suffix %q", out, want)
	}
}
