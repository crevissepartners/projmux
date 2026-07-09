package mux

import (
	"context"
	"strings"
)

// SendKeysLiteral injects literal text into a pane's input using tmux
// `send-keys -l`. It is the thin, reusable primitive for "put literal text into
// a pane" that higher layers (e.g. insert-file-text) build on.
//
// Contract, by design:
//   - Literal only: `-l` sends the bytes verbatim; tmux does not interpret them
//     as key names. `--` terminates option parsing so text starting with `-` is
//     still delivered literally.
//   - No Enter: the primitive never appends a submit key. Callers that want a
//     newline include it in text (the MVP consumer intentionally does not).
//   - No clipboard: this never passes `-w` (OSC52) or any set-buffer/clipboard
//     command, so the OS clipboard is never touched.
//
// An empty paneTarget sends to the active pane.
func (r Runner) SendKeysLiteral(ctx context.Context, paneTarget, text string) error {
	args := []string{"send-keys"}
	args = appendPaneTargetArgs(args, paneTarget)
	args = append(args, "-l", "--", text)
	return r.Run(ctx, args...)
}

// ShowStatusMessage displays a transient message in the tmux status line via
// `display-message` (no `-p`, so it renders instead of printing). An empty
// target scopes the message to the current client.
func (r Runner) ShowStatusMessage(ctx context.Context, target, message string) error {
	args := []string{"display-message"}
	args = appendPaneTargetArgs(args, target)
	args = append(args, "--", strings.TrimRight(message, "\r\n"))
	return r.Run(ctx, args...)
}
