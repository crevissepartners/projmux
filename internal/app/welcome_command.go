package app

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/version"
)

type welcomeCommand struct {
	lookupEnv func(string) string
	update    *updateCommand
}

func newWelcomeCommand(update *updateCommand) *welcomeCommand {
	return &welcomeCommand{
		lookupEnv: os.Getenv,
		update:    update,
	}
}

func (c *welcomeCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("welcome", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printWelcomeUsage(stderr)
		return errors.New("welcome does not accept positional arguments")
	}
	status, hasStatus := resolveWelcomeUpdateStatus(c.update)
	return writeShellWelcome(stdout, strings.TrimSpace(version.String()), status, hasStatus, false, false, welcomeWidthFromEnv(c.lookupEnv))
}

func printWelcomeUsage(w io.Writer) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, "Usage:\n  projmux welcome\n")
}
