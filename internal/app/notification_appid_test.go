package app

import (
	"strings"
	"testing"
)

func TestLegacyToastCleanupScriptScrubsBothArtifacts(t *testing.T) {
	script := buildLegacyToastCleanupPowerShell()
	for _, want := range []string{
		"Get-StartApps",
		"projmux Tmux Codex",
		"projmux Tmux Codex.lnk",
		`Remove-Item -Path 'HKCU:\Software\Classes\AppUserModelId\projmux.TmuxCodex'`,
		"ErrorAction SilentlyContinue",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("cleanup script missing %q: %s", want, script)
		}
	}
}
