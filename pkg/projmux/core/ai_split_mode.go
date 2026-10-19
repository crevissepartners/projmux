package core

// AISplitModeState is the legacy AI new-window mode and its winning layer.
// Mode is claude, codex, antigravity, selective, resume or shell.
// Source is tui, central or default.
type AISplitModeState struct {
	Mode   string
	Source string
}
