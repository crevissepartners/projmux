package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNotificationIconUsesProjmuxPNGForAnyAgent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	wantPath := filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png")
	for _, agent := range []string{"codex", "claude", "openai", "", "  arbitrary  "} {
		if got := cmd.notificationIcon(agent); got != wantPath {
			t.Fatalf("notificationIcon(%q) = %q, want %q", agent, got, wantPath)
		}
	}

	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read notification icon %q: %v", wantPath, err)
	}
	if !bytes.Equal(content, projmuxNotificationIconPNG) {
		t.Fatalf("notification icon content len = %d, want embedded projmux PNG len %d", len(content), len(projmuxNotificationIconPNG))
	}
}

func TestNotificationIconUsesWSLOverrideDirForAnyAgent(t *testing.T) {
	home := t.TempDir()
	override := filepath.Join(home, "windows-readable")
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_WSL_TOAST_ICON_DIR":
			return override
		case "WSL_DISTRO_NAME":
			return "Ubuntu-24.04"
		default:
			return ""
		}
	}

	wantPath := filepath.Join(override, "projmux", "icons", "projmux.png")
	for _, agent := range []string{"codex", "claude", "custom-agent"} {
		if got := cmd.notificationIcon(agent); got != wantPath {
			t.Fatalf("notificationIcon(%q) = %q, want %q", agent, got, wantPath)
		}
	}

	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read WSL notification icon %q: %v", wantPath, err)
	}
	if !bytes.Equal(content, projmuxNotificationIconPNG) {
		t.Fatalf("WSL notification icon content len = %d, want embedded projmux PNG len %d", len(content), len(projmuxNotificationIconPNG))
	}
}
