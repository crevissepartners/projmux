package mutations

import (
	"os"
	"strings"
)

// ProjectDefaults captures the CLI-compatible registration inputs.
type ProjectDefaults struct {
	Shell   string
	HomeDir string
}

// CaptureProjectDefaults reads process defaults once, honoring explicit inputs.
// Shell uses configuredShell's whitespace rule; HomeDir uses the same home
// resolution as the CLI session namer. It neither writes nor retains environment.
func CaptureProjectDefaults(shell, home string) ProjectDefaults {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return ProjectDefaults{Shell: strings.TrimSpace(shell), HomeDir: home}
}
