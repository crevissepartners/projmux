package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	guidanceService "github.com/crevissepartners/projmux/internal/services/agentguidance"
)

// Guidance is host-global configuration, independent of an Agent selector,
// provider availability or tmux. Parse every argument before opening the store.
func (c *agentCommand) runGuidance(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent guidance requires show, read, validate, set, reset, enable, or disable")
	}
	action := args[0]
	if action == "read" || action == "validate" || action == "set" || action == "reset" {
		return c.runGuidanceEdit(args, stdout, stderr)
	}
	if action != "show" && action != "enable" && action != "disable" {
		return usageError("agent guidance requires show, read, validate, set, reset, enable, or disable")
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
	service := guidanceService.Service{Paths: func() (config.Paths, error) { return paths, nil }}
	if action != "show" {
		err := service.SetEnabled(context.Background(), fragment, action == "enable")
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
	view, err := service.Inspect(context.Background(), process)
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

// Editing routes parse all flags before reading the candidate or current inputs.
func (c *agentCommand) runGuidanceEdit(args []string, stdout, stderr io.Writer) error {
	action := args[0]
	fs := flag.NewFlagSet("agent guidance "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	var output, file, revision string
	fs.StringVar(&output, "o", "", "output mode: json")
	if action == "validate" || action == "set" {
		fs.StringVar(&file, "file", "", "candidate path, or - for stdin")
	}
	if action == "set" || action == "reset" {
		fs.StringVar(&revision, "if-match", "", "revision from read or validate")
	}
	rest, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(rest) != 0 {
		return usageError("agent guidance " + action + ": unexpected arguments")
	}
	if output != "" && output != "json" {
		return usageError("agent guidance " + action + ": unsupported output mode " + output)
	}
	if (action == "validate" || action == "set") && file == "" {
		return usageError("agent guidance " + action + ": --file is required")
	}
	if (action == "set" || action == "reset") && revision == "" {
		return usageError("agent guidance " + action + ": --if-match is required")
	}
	var content []byte
	if file != "" {
		input := c.guidanceInput
		if input == nil {
			input = os.Stdin
		}
		if file != "-" {
			f, e := os.Open(file) // #nosec G304 -- explicit CLI candidate path selected by the caller; read-only and bounded to MaxSize+1.
			if e != nil {
				return &agentguidance.EditError{Code: "agent-guidance-unreadable", Err: e}
			}
			defer f.Close()
			input = f
		}
		content, err = io.ReadAll(io.LimitReader(input, agentguidance.MaxSize+1))
		if err != nil {
			return &agentguidance.EditError{Code: "agent-guidance-unreadable", Err: err}
		}
	}
	service := guidanceService.Service{Paths: func() (config.Paths, error) { return configPaths(nil, c.lookupEnv) }}
	ctx := context.Background()
	if action == "validate" {
		v, err := service.Validate(ctx, content)
		if err != nil {
			return err
		}
		if output == "json" {
			return encodeGuidance(stdout, v)
		}
		_, err = fmt.Fprintf(stdout, "%s\t%d/%d bytes\ttmux=%d\tprocess=%d\n", v.Revision, v.Bytes, v.MaxBytes, v.AssembledBytes, v.ProcessAssembledBytes)
		return err
	}
	var v agentguidance.File
	switch action {
	case "read":
		v, err = service.Read(ctx)
	case "set":
		v, err = service.Write(ctx, content, revision)
	case "reset":
		v, err = service.Reset(ctx, revision)
	}
	if err != nil {
		return err
	}
	if output == "json" {
		return encodeGuidance(stdout, v)
	}
	_, err = io.WriteString(stdout, v.Text)
	return err
}
func encodeGuidance(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
