package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Runtime diagnostics picker: the escape hatch's interactive half.
//
// It is a separate surface from `runtime sessions` on purpose. That picker
// answers "which session do I want to open", so it lists the sessions an
// operator would plausibly open and nothing else. This one answers "what is
// actually on this server, and why does projmux say that about it", so it lists
// everything -- the Home control session, scratch sessions, panes with no
// mirrored identity, objects on someone else's tmux, and the contradictions --
// and it offers no action that would change any of their identities.
//
// The action set is the whole safety boundary. Every action here forwards to a
// route that already exists and already owns its own refusals: `focus` moves a
// client and never materializes, `attach project` is the outside-tmux entry
// point into a Project runtime, and the Resource Inspector is read-only. There
// is deliberately no adopt, no import, no rename, and no kill: a diagnostic
// surface that could adopt what it found would be the heuristic merge the
// resolved graph refuses, wearing a menu.

const runtimeDiagnosticsPopupMode = "runtime-diagnostics"

// runtime picker action values. They are sentinels rather than argv so a
// selection can never be mistaken for a shell command.
const (
	runtimeActionFocus   = "__projmux_runtime_focus__"
	runtimeActionAttach  = "__projmux_runtime_attach__"
	runtimeActionInspect = "__projmux_runtime_inspect__"
)

// runtimeDiagnosticsCommand implements `projmux runtime diagnostics`.
type runtimeDiagnosticsCommand struct {
	reader    *runtimeDiagnosticsReader
	native    intpicker.Runner
	homeDir   func() (string, error)
	lookupEnv func(string) string
	// focus, attach, and inspect are the existing routes this surface hands an
	// exact handle to. They are fields rather than direct calls so a test can
	// record exactly which route an action reached, and so nothing here can
	// grow a second implementation of a shipped behavior.
	focus   rawArgvCommand
	attach  rawArgvCommand
	inspect rawArgvCommand
}

func newRuntimeDiagnosticsCommand(runner tmuxCommandRunner) *runtimeDiagnosticsCommand {
	return &runtimeDiagnosticsCommand{
		reader:    newRuntimeDiagnosticsReader(runner),
		native:    intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:   os.UserHomeDir,
		lookupEnv: os.Getenv,
	}
}

// Run opens the diagnostics picker for one exact server.
func (c *runtimeDiagnosticsCommand) Run(args []string, stdout, stderr io.Writer) error {
	return c.run(args, stdout, stderr, nativeUIThemeOwned)
}

// RunNested opens diagnostics inside an outer native surface. The caller owns
// the package-global theme apply/render/restore section, so this child must not
// reacquire the non-reentrant nativeUIThemeMu.
func (c *runtimeDiagnosticsCommand) RunNested(args []string, stdout, stderr io.Writer) error {
	return c.run(args, stdout, stderr, nativeUIThemeInherited)
}

func (c *runtimeDiagnosticsCommand) run(args []string, stdout, stderr io.Writer, themeOwnership nativeUIThemeOwnership) error {
	fs := flag.NewFlagSet("runtime diagnostics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printRuntimeDiagnosticsUsage(stderr) }
	ui := fs.String(switchUIFlag, switchUIPopup, "runtime diagnostics surface to prepare")
	var request runtimeTransportRequest
	fs.StringVar(&request.socket, "socket", "", "exact tmux socket name (tmux -L)")
	fs.StringVar(&request.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		printRuntimeDiagnosticsUsage(stderr)
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		printRuntimeDiagnosticsUsage(stderr)
		return usageError("runtime diagnostics does not accept positional arguments")
	}
	if err := validateSwitchUI(*ui); err != nil {
		printRuntimeDiagnosticsUsage(stderr)
		return usageError(err.Error())
	}
	if c.reader == nil {
		return errors.New("runtime diagnostics reader is not configured")
	}
	if c.native == nil {
		return errors.New("native picker is not configured")
	}

	defer applyNativeUIThemeForOwnership(themeOwnership, c.homeDir, c.lookupEnv, "")()
	locale := appLocale(c.homeDir, c.lookupEnv)

	transport, err := c.reader.transport(request)
	if err != nil {
		return usageError("runtime diagnostics: " + err.Error())
	}
	ctx := context.Background()
	graph, err := c.reader.resolve(ctx, transport)
	if err != nil {
		return err
	}
	rows := runtimediag.Rows(graph)
	socket, _ := c.reader.socketPath(ctx, transport)

	view := runtimeDiagnosticsView{
		locale:      locale,
		hostMode:    string(graph.HostMode),
		transport:   graph.Transport,
		unavailable: runtimediag.Unavailable(graph),
		rows:        rows,
	}
	for {
		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
			UI:            *ui,
			Entries:       view.entries(),
			Title:         "Runtime diagnostics",
			Prompt:        "Runtime > ",
			Footer:        runtimeDiagnosticsFooter(locale),
			ExpectKeys:    []string{"enter"},
			Bindings:      pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, runtimeDiagnosticsPopupMode, "esc"),
			DisableSearch: false,
		})
		if err != nil {
			return fmt.Errorf("run runtime diagnostics picker: %w", err)
		}
		value := strings.TrimSpace(result.Value)
		if value == "" || value == settingsBackValue {
			return nil
		}
		if value == settingsNoopValue {
			continue
		}
		row, ok := view.rowByValue(value)
		if !ok {
			continue
		}
		done, err := c.runActions(view, row, socket, *ui, stdout, stderr)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// runActions opens the safe-action menu of one runtime object.
//
// It returns done=true when an action ran, because every action here moves the
// operator somewhere else -- another client, another Project runtime, another
// full-screen surface -- and returning to a list of rows observed before that
// happened would show a machine state that is no longer current.
func (c *runtimeDiagnosticsCommand) runActions(view runtimeDiagnosticsView, row runtimediag.Row, socket, ui string, stdout, stderr io.Writer) (bool, error) {
	for {
		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
			UI:            ui,
			Entries:       view.actionEntries(row, socket, c.insideTmux()),
			Title:         "Runtime > Actions",
			Prompt:        "Runtime > Actions > ",
			Footer:        runtimeDiagnosticsActionFooter(view.locale),
			ExpectKeys:    []string{"enter"},
			Bindings:      pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, runtimeDiagnosticsPopupMode, "esc"),
			DisableSearch: true,
		})
		if err != nil {
			return false, fmt.Errorf("run runtime diagnostics action picker: %w", err)
		}
		value := strings.TrimSpace(result.Value)
		switch value {
		case "", settingsBackValue:
			return false, nil
		case settingsNoopValue:
			continue
		case runtimeActionFocus:
			return true, c.runFocus(row, socket, stdout, stderr)
		case runtimeActionAttach:
			return true, c.runAttach(row, stdout, stderr)
		case runtimeActionInspect:
			return true, c.runInspect(stdout, stderr)
		default:
			continue
		}
	}
}

// runFocus hands the existing focus route the exact coordinate and the exact
// socket the observation was taken through.
//
// The socket is the server's own `#{socket_path}`, not the flag the operator
// typed: `focus --socket` is a `-S` path, and a `-L <name>` invocation has to be
// resolved to the path tmux itself reports or the action would land on whatever
// server the ambient environment points at.
func (c *runtimeDiagnosticsCommand) runFocus(row runtimediag.Row, socket string, stdout, stderr io.Writer) error {
	if c.focus == nil {
		return errors.New("runtime diagnostics: the focus handler is not configured")
	}
	target := strings.TrimSpace(row.Target)
	if target == "" {
		return fmt.Errorf("runtime diagnostics: %s %s has no exact coordinate to focus", row.Kind, row.ID)
	}
	args := []string{"--target", target}
	if socket != "" {
		args = append(args, "--socket", socket)
	}
	return c.focus.Run(args, stdout, stderr)
}

// runAttach forwards to the existing outside-tmux Project entry point.
//
// It is offered only for a session bound to a Registry Project, and the uid is
// what is handed over. A session with no binding has no Project to attach to,
// and naming one by its tmux session name is exactly the identity guess this
// surface exists to avoid.
func (c *runtimeDiagnosticsCommand) runAttach(row runtimediag.Row, stdout, stderr io.Writer) error {
	if c.attach == nil {
		return errors.New("runtime diagnostics: the attach handler is not configured")
	}
	if !row.Managed() || row.Kind != string(resourcegraph.ObjectSession) {
		return fmt.Errorf("runtime diagnostics: %s %s is not bound to a Registry Project", row.Kind, row.ID)
	}
	return c.attach.Run([]string{"project", "uid:" + row.Resource.UID}, stdout, stderr)
}

// runInspect opens the existing Resource Inspector unchanged.
func (c *runtimeDiagnosticsCommand) runInspect(stdout, stderr io.Writer) error {
	if c.inspect == nil {
		return errors.New("runtime diagnostics: the resource inspector is not configured")
	}
	return c.inspect.Run(nil, stdout, stderr)
}

func (c *runtimeDiagnosticsCommand) insideTmux() bool {
	if c.lookupEnv == nil {
		return false
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func printRuntimeDiagnosticsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]")
}

func runtimeDiagnosticsFooter(locale i18n.Locale) string {
	return localizeText(locale, "picker.runtime.footer", "Enter: actions | Esc: close")
}

func runtimeDiagnosticsActionFooter(locale i18n.Locale) string {
	return localizeText(locale, "picker.runtime.action_footer", "Read-only diagnostics; actions never adopt or delete.")
}
