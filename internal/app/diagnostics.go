package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type diagnosticsCommand struct {
	lookupEnv func(string) string
	homeDir   func() (string, error)
	doctor    *doctorCommand
}

func newDiagnosticsCommand() *diagnosticsCommand {
	return &diagnosticsCommand{lookupEnv: os.Getenv, homeDir: os.UserHomeDir, doctor: newDoctorCommand()}
}

func (c *diagnosticsCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printDiagnosticsUsage(stderr)
		return usageError("diagnostics requires a subcommand")
	}
	switch args[0] {
	case "log":
		return c.runLog(args[1:], stdout, stderr)
	case "report":
		return c.runReport(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printDiagnosticsUsage(stdout)
		return nil
	default:
		printDiagnosticsUsage(stderr)
		return usageError(fmt.Sprintf("unknown diagnostics subcommand: %s", args[0]))
	}
}

func (c *diagnosticsCommand) runLog(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diagnostics log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tail := fs.Int("tail", 50, "number of recent records to print")
	jsonOut := fs.Bool("json", false, "print records as JSONL")
	level := fs.String("level", "", "filter by level")
	component := fs.String("component", "", "filter by component")
	pathOnly := fs.Bool("path", false, "print the operations log path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		printDiagnosticsUsage(stderr)
		return usageError("diagnostics log does not accept positional arguments")
	}
	if *tail < 0 {
		return usageError("diagnostics log --tail must be zero or greater")
	}
	*level = strings.ToLower(strings.TrimSpace(*level))
	if *level != "" && !diagnostics.ValidLevel(*level) {
		return usageError("diagnostics log --level must be info or error")
	}
	*component = strings.TrimSpace(*component)
	path, err := diagnostics.DefaultPath(c.lookupEnv, c.homeDir)
	if err != nil {
		return err
	}
	if *pathOnly {
		_, err := fmt.Fprintln(stdout, path)
		return err
	}
	events, err := diagnostics.NewStore(path).Read()
	if err != nil {
		return fmt.Errorf("read operational diagnostics: %w", err)
	}
	filtered := events[:0]
	for _, event := range events {
		if *level != "" && event.Level != *level {
			continue
		}
		if *component != "" && event.Component != *component {
			continue
		}
		filtered = append(filtered, event)
	}
	if len(filtered) > *tail {
		filtered = filtered[len(filtered)-*tail:]
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	for _, event := range filtered {
		if *jsonOut {
			if err := encoder.Encode(event); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(stdout, formatOperationalEvent(event)); err != nil {
			return err
		}
	}
	return nil
}

func formatOperationalEvent(event diagnostics.Event) string {
	parts := []string{event.At, strings.ToUpper(event.Level), event.Component, event.Event, event.Result}
	if event.Command != "" {
		command := event.Command
		if event.Subcommand != "" {
			command += " " + event.Subcommand
		}
		parts = append(parts, "command="+command)
	}
	if event.Operation != "" {
		parts = append(parts, "operation="+event.Operation)
	}
	if event.Source != "" {
		parts = append(parts, "source="+event.Source)
	}
	for _, count := range []struct {
		name  string
		value *int
	}{
		{"window_count", event.WindowCount},
		{"pane_count", event.PaneCount},
		{"shell_recipe_count", event.ShellRecipeCount},
		{"agent_recipe_count", event.AgentRecipeCount},
		{"startup_recipe_count", event.StartupRecipeCount},
		{"item_count", event.ItemCount},
	} {
		if count.value != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", count.name, *count.value))
		}
	}
	parts = append(parts, fmt.Sprintf("duration_ms=%d", event.DurationMS), "run_id="+event.RunID, "version="+event.Version, "mux_backend="+event.MuxBackend)
	if event.Kind != "" {
		parts = append(parts, "kind="+event.Kind)
	}
	if event.Code != "" {
		parts = append(parts, "code="+event.Code)
	}
	if event.Message != "" {
		parts = append(parts, "message="+event.Message)
	}
	return strings.Join(parts, " ")
}

func printDiagnosticsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux diagnostics log [--tail N] [--json] [--level LEVEL] [--component NAME] [--path]")
	fmt.Fprintln(w, "  projmux diagnostics report [--output <path>]")
}
