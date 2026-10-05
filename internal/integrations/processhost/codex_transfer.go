package processhost

import (
	"context"
	"fmt"
	"strings"
)

// TmuxCodexSource describes a retired interactive writer without inventing a
// process-host binding or connection for it.
type TmuxCodexSource struct {
	Project, Window, Agent, Pane, Generation, Operation, RuntimeID, Thread string
	BrokerRuntime, Endpoint                                                string
	ConnectionEpoch, BindingEpoch                                          uint64
}

type CodexTransfer struct {
	Source TmuxCodexSource
	// Verify checks the still-held broker retirement reservation and fresh
	// source/target ownership. It runs before spawn and again before resume.
	Verify func(context.Context, TmuxCodexSource, Binding) error
}

func (h *Host) TransferCodex(ctx context.Context, launch Launch, config CodexConfig, transfer CodexTransfer) (*CodexHandle, error) {
	s, b := transfer.Source, launch.Binding
	if launch.resume != nil || launch.transfer != nil || launch.codexTransfer != nil || transfer.Verify == nil ||
		s.Project == "" || s.Window == "" || s.Agent == "" || s.Pane == "" || s.Generation == "" || s.Operation == "" || !strings.HasPrefix(s.RuntimeID, "%") ||
		s.Thread == "" || strings.TrimSpace(s.Thread) != s.Thread || len(s.Thread) > 256 || s.BrokerRuntime == "" || s.Endpoint == "" || s.ConnectionEpoch == 0 || s.BindingEpoch == 0 ||
		!b.valid(h.instance) || b.Project != s.Project || b.Window != s.Window || b.Agent != s.Agent || b.Pane == s.Pane || b.Generation == s.Generation || b.Operation == s.Operation {
		return nil, fmt.Errorf("%w: invalid tmux Codex transfer identity", ErrResumeRefused)
	}
	if err := transfer.Verify(ctx, s, b); err != nil {
		return nil, fmt.Errorf("%w: source retirement is unconfirmed: %w", ErrResumeRefused, err)
	}
	launch.codexTransfer = &transfer
	p, err := h.StartCodex(ctx, launch, config)
	if err != nil {
		return p, fmt.Errorf("%w: %w", ErrResumeRefused, err)
	}
	return p, nil
}
