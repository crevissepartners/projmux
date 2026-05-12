package app

import (
	"errors"
	"fmt"
	"io"
)

func (c *tmuxCommand) runSessionStateStatus(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		printTmuxUsage(stderr)
		return fmt.Errorf("tmux sessionstate-status accepts no arguments")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	statusbar := &statusbarCommand{
		runner:         c.runner,
		executable:     c.executable,
		sessionStoreFn: c.sessionStore,
		lookupEnv:      c.lookupEnv,
		homeDir:        c.homeDir,
		now:            c.now,
	}
	return statusbar.handleSessionState(statusbarClickOptions{}, nil, stderr)
}
