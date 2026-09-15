package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// flagShapedWindowNameRealTmuxEnv makes tmux mandatory for the real-tmux
// boundary test. test/integration/flag-shaped-window-names.sh sets it so a
// missing tmux fails the integration suite instead of skipping.
const flagShapedWindowNameRealTmuxEnv = "PMX_TEST_FLAG_NAME_REAL_TMUX"

// TestMaterializerCreatesFlagShapedWindowNamesThroughRealTmux drives the
// materializer's Window create and stable-name mirror through an isolated real
// tmux server. tmux reads the token after new-window -n and the value after a
// set-option option name as values, so a Registry-valid name spelled like a
// tmux flag must land verbatim in both #{window_name} and the mirror.
func TestMaterializerCreatesFlagShapedWindowNamesThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(flagShapedWindowNameRealTmuxEnv) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", flagShapedWindowNameRealTmuxEnv, err)
		}
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "pfn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "s")
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	sessionID, err := tmux("new-session", "-d", "-s", "flag-names", "-P", "-F", "#{session_id}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, sessionID)
	}
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	for _, option := range [][2]string{{tmuxopts.AppGlobal, "1"}, {runtimeMutationSocketNameOption, "flag-names"}} {
		if out, err := tmux("set-option", "-g", option[0], option[1]); err != nil {
			t.Fatalf("mark isolated tmux app-owned: %v: %s", err, out)
		}
	}

	target := tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}
	routed := explicitTmuxRunner{runner: shellTmuxExecRunner{env: func() []string { return environment }}, target: target}
	runtime := &materializer{runner: routed, mirror: intmetadata.NewMirror(routed), target: target}
	if err := runtime.guardExactRoute(ctx, false); err != nil {
		t.Fatalf("bind isolated app route: %v", err)
	}

	for index, name := range []string{"-L", "-t", "-Lx"} {
		if err := coremetadata.ValidateName(name); err != nil {
			t.Fatalf("Registry refuses %q: %v", name, err)
		}
		created, err := runtime.newWindow(ctx, sessionID, name, root, []string{"tail", "-f", "/dev/null"})
		if err != nil {
			t.Fatalf("create Window %q: %v", name, err)
		}
		window := coremetadata.Window{Metadata: coremetadata.ObjectMeta{UID: "win-flag-" + string(rune('a'+index)), Name: name}}
		if _, err := runtime.claimRuntimeUID(ctx, runtimeWindow, created.WindowID, window.Metadata.UID); err != nil {
			t.Fatalf("claim Window %q: %v", name, err)
		}
		if err := runtime.mirrorWindow(ctx, created.WindowID, window); err != nil {
			t.Fatalf("mirror Window %q: %v", name, err)
		}
		for _, format := range []string{"#{window_name}", "#{" + tmuxopts.WindowName + "}"} {
			got, err := tmux("display-message", "-p", "-t", created.WindowID, "-F", format)
			if err != nil || got != name {
				t.Fatalf("Window %s %s = %q (err %v), want %q", created.WindowID, format, got, err, name)
			}
		}
	}
}
