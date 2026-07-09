package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/integrations/mux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// User-facing status messages. They are registered in uiTextKeys +
// default_catalog.go so localizeUIText resolves them for en-US and ko-KR. The
// error variants are prefixes; the offending source name is appended at
// runtime.
const (
	insertMessageNotFound       = "insert source not found:"
	insertMessageUnreadable     = "insert source unreadable:"
	insertMessageEmpty          = "insert source empty:"
	insertMessageNoneConfigured = "no insert-file-text source configured"
)

// insertMuxRunner is the mux boundary the command needs: the literal pane-insert
// primitive, a status-line message, popup hosting for the N-source picker, and
// active-pane resolution. mux.Runner satisfies it; tests inject a mux.Runner
// built over a recording backend so clipboard non-use is directly asserted.
type insertMuxRunner interface {
	SendKeysLiteral(ctx context.Context, paneTarget, text string) error
	ShowStatusMessage(ctx context.Context, target, message string) error
	DisplayPopup(ctx context.Context, command string, options mux.PopupOptions) error
	DisplayMessageTrimmed(ctx context.Context, opts mux.DisplayMessageOptions) (string, error)
}

type insertFileTextCommand struct {
	runner       insertMuxRunner
	nativePicker intpicker.Runner
	loadSources  func() (map[string]hooks.InsertFileTextSource, error)
	readFile     func(string) ([]byte, error)
	homeDir      func() (string, error)
	lookupEnv    func(string) string
	executable   func() (string, error)
}

func newInsertFileTextCommand() *insertFileTextCommand {
	return &insertFileTextCommand{
		runner:       mux.DefaultRunner(),
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		loadSources:  loadInsertFileTextSources,
		readFile:     os.ReadFile,
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		executable:   os.Executable,
	}
}

// loadInsertFileTextSources reads the configured sources from the global
// projmux config. Insert-file-text is global-only for the MVP.
func loadInsertFileTextSources() (map[string]hooks.InsertFileTextSource, error) {
	path, err := hooks.GlobalConfigPath(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg.InsertFileText, nil
}

// Run inserts a configured text source into a tmux pane.
//
//	insert-file-text <name> [--pane <id>]   direct insert of a named source
//	insert-file-text [--pane <id>]          resolve at runtime: 0 sources -> a
//	                                        status message, 1 -> direct insert,
//	                                        N -> a popup source picker
//	insert-file-text --pick [--pane <id>]   render the picker inside an open
//	                                        popup (has a TTY) and insert
func (c *insertFileTextCommand) Run(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("insert-file-text", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pane := fs.String("pane", "", "target tmux pane id (defaults to the active pane)")
	pick := fs.Bool("pick", false, "render the source picker inside an open popup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Accept the optional source name before or after flags: flag.Parse stops at
	// the first non-flag token, so a leading name (`insert-file-text NAME
	// --pane X`) leaves the flags unparsed. Peel the name and re-parse the rest.
	name := ""
	rest := fs.Args()
	if len(rest) > 0 {
		name = strings.TrimSpace(rest[0])
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
	}
	if fs.NArg() != 0 {
		return usageError("insert-file-text accepts at most one source name")
	}

	ctx := context.Background()
	target := strings.TrimSpace(*pane)

	if *pick {
		return c.runPick(ctx, target)
	}
	if name != "" {
		return c.insertNamed(ctx, target, name)
	}
	return c.resolveAndInsert(ctx, target)
}

// insertNamed inserts an explicitly named source (the CLI contract).
func (c *insertFileTextCommand) insertNamed(ctx context.Context, target, name string) error {
	sources, err := c.loadSources()
	if err != nil {
		return fmt.Errorf("load insert-file-text sources: %w", err)
	}
	source, ok := sources[name]
	if !ok {
		return c.notify(ctx, target, insertMessageNotFound, name)
	}
	return c.insertSource(ctx, target, name, source)
}

// resolveAndInsert enumerates configured sources and branches 0/1/N. This is the
// keybinding path: it runs in a run-shell (no TTY), so the N branch delegates to
// a popup that can host the interactive picker.
func (c *insertFileTextCommand) resolveAndInsert(ctx context.Context, target string) error {
	sources, err := c.loadSources()
	if err != nil {
		return fmt.Errorf("load insert-file-text sources: %w", err)
	}
	names := sortedInsertSourceNames(sources)
	switch len(names) {
	case 0:
		return c.notify(ctx, target, insertMessageNoneConfigured, "")
	case 1:
		return c.insertSource(ctx, target, names[0], sources[names[0]])
	default:
		return c.openPicker(ctx, target, names)
	}
}

// runPick renders the picker inside an already-open popup and inserts the chosen
// source into the originating pane.
func (c *insertFileTextCommand) runPick(ctx context.Context, target string) error {
	if target == "" {
		target = strings.TrimSpace(c.lookupEnv("TMUX_SPLIT_TARGET_PANE"))
	}
	sources, err := c.loadSources()
	if err != nil {
		return fmt.Errorf("load insert-file-text sources: %w", err)
	}
	names := sortedInsertSourceNames(sources)
	switch len(names) {
	case 0:
		return c.notify(ctx, target, insertMessageNoneConfigured, "")
	case 1:
		return c.insertSource(ctx, target, names[0], sources[names[0]])
	}
	name, err := c.pickSource(names)
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return err
	}
	if name == "" {
		return nil
	}
	source, ok := sources[name]
	if !ok {
		return c.notify(ctx, target, insertMessageNotFound, name)
	}
	return c.insertSource(ctx, target, name, source)
}

// insertSource resolves a source to text and injects it as literal pane input.
// Any failure to produce non-empty text surfaces a short status message; the
// clipboard is never touched.
func (c *insertFileTextCommand) insertSource(ctx context.Context, target, name string, source hooks.InsertFileTextSource) error {
	path := expandInsertHomePath(c.homeDir, source.Path)
	if strings.TrimSpace(path) == "" {
		return c.notify(ctx, target, insertMessageUnreadable, name)
	}
	data, err := c.readFile(path)
	if err != nil {
		return c.notify(ctx, target, insertMessageUnreadable, name)
	}
	text := string(data)
	if source.Trim {
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return c.notify(ctx, target, insertMessageEmpty, name)
	}
	return c.runner.SendKeysLiteral(ctx, target, text)
}

// openPicker resolves the active pane (so the popup does not target itself) and
// opens a tmux popup that re-invokes this command with --pick.
func (c *insertFileTextCommand) openPicker(ctx context.Context, target string, _ []string) error {
	binaryPath, err := c.executable()
	if err != nil {
		return err
	}
	if target == "" {
		if resolved, rerr := c.runner.DisplayMessageTrimmed(ctx, mux.DisplayMessageOptions{Format: mux.TmuxFormat("pane_id")}); rerr == nil {
			target = strings.TrimSpace(resolved)
		}
	}
	command := shellQuote(binaryPath) + " insert-file-text --pick"
	if target != "" {
		command += " --pane " + shellQuote(target)
	}
	err = c.runner.DisplayPopup(ctx, command, mux.PopupOptions{
		Target:        target,
		Width:         "60%",
		Height:        "50%",
		CloseBehavior: mux.PopupCloseOnExit,
	})
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

// pickSource shows the source-name picker and returns the accepted source name
// (empty when the user cancelled).
func (c *insertFileTextCommand) pickSource(names []string) (string, error) {
	if c.nativePicker == nil {
		return "", errors.New("native picker is not configured")
	}
	entries := make([]intpickercompat.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, intpickercompat.Entry{Label: name, Value: name, SearchKey: name})
	}
	result, err := runPickerOptionBackend(c.homeDir, c.lookupEnv, c.nativePicker, nil, intpickercompat.Options{
		UI:         "insert-file-text",
		Entries:    entries,
		Title:      "Insert File Text - Choose a source",
		Prompt:     "Insert > ",
		Footer:     projmuxFooter("Choose a text source to insert into the active pane."),
		ExpectKeys: []string{"enter"},
	})
	if err != nil {
		return "", err
	}
	if result.Key != "enter" || result.Value == "" {
		return "", nil
	}
	return result.Value, nil
}

// notify shows a short, localized status-line message. The error-variant
// prefixes append the source name. It always returns nil so a keybinding's
// run-shell does not surface a shell error for an expected, user-visible
// condition.
func (c *insertFileTextCommand) notify(ctx context.Context, target, fallback, name string) error {
	message := localizeUIText(c.locale(), fallback)
	if name != "" {
		message = message + " " + name
	}
	_ = c.runner.ShowStatusMessage(ctx, target, message)
	return nil
}

func (c *insertFileTextCommand) locale() i18n.Locale {
	return appLocale(c.homeDir, c.lookupEnv)
}

func sortedInsertSourceNames(sources map[string]hooks.InsertFileTextSource) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// expandInsertHomePath expands a leading `~` (MVP: home only, no env vars).
func expandInsertHomePath(homeDir func() (string, error), path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	if homeDir == nil {
		return path
	}
	home, err := homeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}
