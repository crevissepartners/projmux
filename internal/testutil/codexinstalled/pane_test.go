package codexinstalled

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRemoteNewPaneArgvIsContentFree(t *testing.T) {
	got := remoteNewTMUXArgs("/private/workspace", "/exact/codex", "/private/app-server.sock")
	want := []string{
		"-L", capabilityTmuxSocket, "new-session", "-d",
		"-s", "installed-capability-remote-new", "-c", "/private/workspace",
		"-P", "-F", "#{pane_id}", "/exact/codex", "--remote", "unix:///private/app-server.sock",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remote-new content-free argv = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"resume", "send-keys", "prompt", "turn/start"} {
		if slices.Contains(got, forbidden) {
			t.Fatalf("remote-new content-free argv includes forbidden operand %q", forbidden)
		}
	}
}

func TestPayloadFreeSmokeRootRejectsOverlongPrivatePathWithoutDisclosingIt(t *testing.T) {
	if err := validatePayloadFreeSmokeRoot("/tmp/projmux-payload-free-XXXXXX"); err != nil {
		t.Fatalf("documented payload-free smoke root exceeded private tmux path budget: %v", err)
	}
	privateRoot := "/tmp/" + strings.Repeat("private-", 20)
	err := validatePayloadFreeSmokeRoot(privateRoot)
	if err == nil {
		t.Fatal("overlong private tmux path was accepted")
	}
	if strings.Contains(err.Error(), privateRoot) {
		t.Fatal("private tmux path was disclosed in validation error")
	}
}

func TestTurnFreeAttachPaneCommandExcludesCredentialsAndAmbientIdentity(t *testing.T) {
	binDir := t.TempDir()
	tmuxPath := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\n/usr/bin/env\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	pane := IsolatedPane{environment: tmuxCapabilityEnvironment([]string{
		"PATH=" + binDir,
		"PROJMUX_SAFE=retained",
		"CODEX_HOME=/ambient/codex",
		"TMUX=/ambient/tmux,1,0",
		"TMUX_PANE=%42",
		"TMUX_TMPDIR=/ambient/tmux-root",
		"OPENAI_API_KEY=openai-secret",
		"CODEX_API_KEY=codex-secret",
		"CODEX_TOKEN=codex-token-secret",
	}, "/isolated/codex", "/isolated/tmux-root")}
	output, err := pane.run(context.Background(), "display-message")
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]string, len(pane.environment))
	for entry := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("environment entry %q has no value", entry)
		}
		got[key] = value
		if strings.Contains(value, "secret") || strings.Contains(value, "/ambient/") || value == "%42" {
			t.Fatalf("caller identity reached capability Pane environment: %q", entry)
		}
	}
	for _, key := range []string{"TMUX", "TMUX_PANE", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_TOKEN"} {
		if _, ok := got[key]; ok {
			t.Fatalf("capability Pane inherited %s", key)
		}
	}
	if got["CODEX_HOME"] != "/isolated/codex" || got["TMUX_TMPDIR"] != "/isolated/tmux-root" {
		t.Fatalf("capability Pane isolation roots = %+v", got)
	}
	if got["PATH"] != binDir || got["PROJMUX_SAFE"] != "retained" {
		t.Fatalf("non-sensitive environment was not preserved: %+v", got)
	}
}
