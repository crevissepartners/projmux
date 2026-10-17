package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
)

// Guidance is host-global configuration, independent of an Agent selector,
// provider availability or tmux. Parse every argument before opening the store.
func (c *agentCommand) runGuidance(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent guidance requires show, enable, or disable")
	}
	action := args[0]
	if action != "show" && action != "enable" && action != "disable" {
		return usageError("agent guidance requires show, enable, or disable")
	}
	fs := flag.NewFlagSet("agent guidance "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	var output string
	var process bool
	if action == "show" {
		fs.StringVar(&output, "o", "", "output mode: json")
		fs.BoolVar(&process, "process", false, "include the process host context")
	}
	rest, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if (action == "show" && len(rest) != 0) || len(rest) > 1 {
		return usageError("agent guidance " + action + ": unexpected arguments")
	}
	if output != "" && output != "json" {
		return usageError("agent guidance show: unsupported output mode " + output)
	}
	fragment := ""
	if len(rest) == 1 {
		fragment = rest[0]
		if err := agentguidance.ValidateFragmentName(fragment); err != nil {
			return usageError(err.Error())
		}
	}
	paths, err := configPaths(nil, c.lookupEnv)
	if err != nil {
		return err
	}
	store := agentguidance.NewDefaultStore(paths)
	if action != "show" {
		err := store.SetEnabled(fragment, action == "enable")
		if err != nil {
			return err
		}
		target := "all guidance"
		if fragment != "" {
			target = "fragment " + fragment
		}
		_, err = fmt.Fprintf(stdout, "%s: %sd\n", target, action)
		return err
	}
	view, err := store.Inspect(process)
	if view.MigrationNotice != "" {
		fmt.Fprintln(stderr, "projmux: "+view.MigrationNotice)
	}
	if err != nil {
		return err
	}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	for _, part := range view.Parts {
		status := "enabled"
		if !part.Enabled {
			status = "disabled"
		}
		present := "present"
		if !part.Present {
			present = "missing"
		}
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", part.Source, part.Name, status, present, part.Path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "\nFinal guidance:"); err != nil {
		return err
	}
	_, err = io.WriteString(stdout, view.Text)
	return err
}
