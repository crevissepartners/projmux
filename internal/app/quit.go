package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	quitActionQuit   = "quit:quit"
	quitActionCancel = "quit:cancel"
)

type quitCommand struct {
	lookupEnv    func(string) string
	runner       tmuxRunner
	nativePicker intpicker.Runner
}

func newQuitCommand() *quitCommand {
	return &quitCommand{
		lookupEnv:    os.Getenv,
		runner:       inttmux.ExecRunner{},
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
	}
}

func (c *quitCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("quit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "quit without opening the action picker")
	force := fs.Bool("force", false, "quit without opening the action picker")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		printQuitUsage(stderr)
		return errors.New("quit does not accept positional arguments")
	}

	if !*yes && !*force {
		action, err := c.pickAction()
		if err != nil {
			return err
		}
		switch action {
		case quitActionQuit:
		case "", quitActionCancel:
			return nil
		default:
			return fmt.Errorf("unknown quit action: %s", action)
		}
	}
	return c.shutdownAppRuntime(context.Background(), defaultAppSocket)
}

func (c *quitCommand) pickAction() (string, error) {
	result, err := runNativePickerOption(os.UserHomeDir, c.lookupEnv, c.nativePicker, quitActionOptions())
	if err != nil {
		if isNoSelectionExit(err) {
			return "", nil
		}
		return "", fmt.Errorf("run quit picker: %w", err)
	}
	if result.Key != "enter" {
		return "", nil
	}
	return strings.TrimSpace(result.Value), nil
}

func quitActionOptions() intpickercompat.Options {
	return intpickercompat.Options{
		UI:            "quit",
		Title:         "Quit projmux",
		Prompt:        "Quit > ",
		Footer:        projmuxFooter("Enter: choose  |  Esc: cancel"),
		ExpectKeys:    []string{"enter"},
		DisableSearch: true,
		Entries: []intpickercompat.Entry{
			{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Quit projmux", "terminate the app-owned mux runtime"),
				Value: quitActionQuit,
			},
			{
				Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Cancel", "keep projmux running"),
				Value: quitActionCancel,
			},
		},
		Bindings: pickerCloseBindings("esc", "ctrl-c"),
	}
}

func (c *quitCommand) shutdownAppRuntime(ctx context.Context, socketName string) error {
	if strings.TrimSpace(socketName) == "" {
		return errors.New("quit target socket is required")
	}
	if c.runner == nil {
		return errors.New("quit mux runner is not configured")
	}
	command := "tmux"
	target, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return err
	}
	routed := explicitTmuxRunner{runner: c.runner, target: target}
	pathOut, err := routed.Run(ctx, command, "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		if tmuxServerMissing(err) {
			return nil
		}
		return fmt.Errorf("resolve app %s runtime: %w", command, err)
	}
	path := strings.TrimSpace(string(pathOut))
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("quit app runtime: requested socket has no exact absolute physical identity")
	}
	owned, err := routed.Run(ctx, command, "show-options", "-gqv", "@projmux_app")
	if err != nil {
		return fmt.Errorf("check app %s runtime ownership: %w", command, err)
	}
	if strings.TrimSpace(string(owned)) != "1" {
		return nil
	}
	logical, err := routed.Run(ctx, command, "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil || strings.TrimSpace(string(logical)) != socketName {
		return errors.New("quit app runtime: requested server has no matching logical route marker")
	}
	condition := "#{&&:#{==:#{@projmux_app},1},#{==:#{" + runtimeMutationSocketNameOption + "}," + socketName + "}}"
	_, err = c.runner.Run(ctx, command, "-S", path, "if-shell", "-F", condition, "kill-server", "run-shell 'exit 73'")
	if err != nil {
		return fmt.Errorf("quit app %s runtime: %w", command, err)
	}
	return nil
}

func tmuxServerMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "can't find server") ||
		strings.Contains(msg, "failed to connect")
}

func printQuitUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux quit [--yes|--force]")
}
