package processhost

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// TmuxConversationSource identifies a retired interactive Claude writer. It
// deliberately has no process Host or Connection: those identities did not
// exist on the source host. Verify must re-observe retirement and the exact
// source recipe before a new owned child is admitted.
type TmuxConversationSource struct {
	Project, Window, Agent, Pane, Generation, Operation, RuntimeID, Session string
}

type ClaudeTransfer struct {
	Source TmuxConversationSource
	Verify func(context.Context, TmuxConversationSource, Binding) error
}

// TransferClaude resumes a retired tmux conversation on a different managed
// Pane. Normal process resume retains its same-Pane and old-control guards.
func (h *Host) TransferClaude(ctx context.Context, launch Launch, transfer ClaudeTransfer, turn, prompt string) (*Handle, error) {
	s, b := transfer.Source, launch.Binding
	if transfer.Verify == nil || s.Project == "" || s.Window == "" || s.Agent == "" || s.Pane == "" || s.Generation == "" || s.Operation == "" || !strings.HasPrefix(s.RuntimeID, "%") ||
		s.Session == "" || strings.TrimSpace(s.Session) != s.Session || len(s.Session) > 256 ||
		!b.valid(h.instance) || b.Project != s.Project || b.Window != s.Window || b.Agent != s.Agent || b.Pane == s.Pane || b.Generation == s.Generation || b.Operation == s.Operation ||
		turn == "" || len(turn) > 256 || strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("%w: invalid tmux conversation transfer identity", ErrResumeRefused)
	}
	for _, arg := range launch.Command.Args {
		for _, reserved := range []string{"--resume", "-r", "--continue", "-c", "--fork-session", "--session-id", "--no-session-persistence"} {
			if arg == reserved || strings.HasPrefix(arg, reserved+"=") {
				return nil, fmt.Errorf("%w: conflicting session argument", ErrResumeRefused)
			}
		}
	}
	if err := transfer.Verify(ctx, s, b); err != nil {
		return nil, fmt.Errorf("%w: source retirement is unconfirmed: %w", ErrResumeRefused, err)
	}
	launch.transfer = &transfer
	launch.resumeTurn, launch.resumePrompt = turn, prompt
	launch.Command.Args = append(slices.Clone(launch.Command.Args), "--resume", s.Session)
	handle, err := h.Start(ctx, launch)
	if err != nil {
		return handle, fmt.Errorf("%w: %w", ErrResumeRefused, err)
	}
	return handle, nil
}

func (l Launch) expectedResumeSession() string {
	if l.resume != nil {
		return l.resume.Session
	}
	if l.transfer != nil {
		return l.transfer.Source.Session
	}
	return ""
}
