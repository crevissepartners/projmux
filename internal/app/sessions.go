package app

import (
	"context"
	"flag"
	"fmt"
	"io"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
	intrender "github.com/es5h/projmux/internal/ui/render"
)

type sessionsRecentResolver interface {
	RecentSessions(ctx context.Context) ([]string, error)
}

type sessionsOpener interface {
	OpenSession(ctx context.Context, sessionName string) error
}

type sessionsRunner interface {
	Run(options intfzf.Options) (intfzf.Result, error)
}

type sessionsCommand struct {
	recent sessionsRecentResolver
	opener sessionsOpener
	runner sessionsRunner
}

func newSessionsCommand() *sessionsCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	return &sessionsCommand{
		recent: client,
		opener: client,
		runner: intfzf.NewRunner(),
	}
}

// Run manages the recent-session picker surface.
func (c *sessionsCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSessionsUsage(stderr)
	}

	ui := fs.String(switchUIFlag, switchUIPopup, "recent-session surface to prepare")
	if err := fs.Parse(args); err != nil {
		printSessionsUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printSessionsUsage(stderr)
		return fmt.Errorf("sessions does not accept positional arguments")
	}
	if err := validateSwitchUI(*ui); err != nil {
		printSessionsUsage(stderr)
		return err
	}

	if c.recent == nil {
		return fmt.Errorf("recent tmux session resolver is not configured")
	}
	sessionNames, err := c.recent.RecentSessions(context.Background())
	if err != nil {
		return fmt.Errorf("resolve recent tmux sessions: %w", err)
	}
	if len(sessionNames) == 0 {
		return nil
	}

	if c.runner == nil {
		return fmt.Errorf("sessions runner is not configured")
	}
	rows := intrender.BuildSessionRows(sessionNames)
	result, err := c.runner.Run(intfzf.Options{
		UI:      *ui,
		Entries: rowsToEntries(rows),
	})
	if err != nil {
		return fmt.Errorf("run sessions picker: %w", err)
	}
	if result.Value == "" {
		return nil
	}

	if c.opener == nil {
		return fmt.Errorf("sessions opener is not configured")
	}
	if err := c.opener.OpenSession(context.Background(), result.Value); err != nil {
		return fmt.Errorf("open tmux session %q: %w", result.Value, err)
	}

	return nil
}

func rowsToEntries(rows []intrender.SessionRow) []intfzf.Entry {
	entries := make([]intfzf.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, intfzf.Entry{
			Label: row.Label,
			Value: row.Value,
		})
	}
	return entries
}

func printSessionsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux sessions [--ui=popup|sidebar]")
}
