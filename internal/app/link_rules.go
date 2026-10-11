package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/crevissepartners/projmux/internal/app/personacmd"
	"github.com/crevissepartners/projmux/internal/config"
	links "github.com/crevissepartners/projmux/internal/core/projectlinks"
	service "github.com/crevissepartners/projmux/internal/services/projectlinks"
)

// linkRulesCommand parses and renders; the service owns all management decisions.
type linkRulesCommand struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	stdin     io.Reader
}

func newLinkRulesCommand() *linkRulesCommand {
	return &linkRulesCommand{os.UserHomeDir, os.Getenv, os.Stdin}
}
func (c *linkRulesCommand) service() service.Service {
	return service.Service{Paths: func() (config.Paths, error) { return savedSettingsPaths(c.homeDir, c.lookupEnv) }, RegistryPaths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}
}
func (c *linkRulesCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printRouteUsage(stderr, "link-rules")
		return usageError("link-rules requires show, set or delete")
	}
	verb := args[0]
	if verb != "show" && verb != "set" && verb != "delete" {
		printRouteUsage(stderr, "link-rules")
		return usageError("unknown link-rules subcommand: " + verb)
	}
	fs := flag.NewFlagSet("link-rules "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	output := fs.String("o", "text", "output format: text or json")
	var file string
	var dryRun bool
	if verb == "set" {
		fs.StringVar(&file, "file", "", "read stored-format JSON from this file; - reads stdin")
		fs.BoolVar(&dryRun, "dry-run", false, "validate and preview without writing")
	}
	operands, err := parseLinkRulesArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if *output != "text" && *output != "json" {
		return usageError("link-rules: -o must be text or json")
	}
	if verb == "set" && len(operands) == 2 && file == "" {
		file = operands[1]
		operands = operands[:1]
	}
	if len(operands) != 1 {
		switch verb {
		case "show":
			printRouteUsage(stderr, "link-rules show")
		case "set":
			printRouteUsage(stderr, "link-rules set")
		case "delete":
			printRouteUsage(stderr, "link-rules delete")
		}
		return usageError("link-rules " + verb + " requires exactly one <project-ref>")
	}
	var out service.Result
	switch verb {
	case "show":
		out, err = c.service().Read(context.Background(), operands[0])
	case "delete":
		out, err = c.service().Delete(context.Background(), operands[0])
	case "set":
		var source io.Reader = c.stdin
		if source == nil {
			source = os.Stdin
		}
		if file != "" && file != "-" {
			opened, openErr := personacmd.OpenFileUnderParent(file)
			if openErr != nil {
				return openErr
			}
			defer opened.Close()
			source = opened
		}
		content, readErr := io.ReadAll(io.LimitReader(source, links.MaxFileSize+1))
		if readErr != nil {
			return readErr
		}
		rules, decodeErr := service.Decode(content)
		if decodeErr != nil {
			return linkRulesError(decodeErr)
		}
		out, err = c.service().Write(context.Background(), operands[0], rules, dryRun)
	}
	if err != nil {
		return linkRulesError(err)
	}
	if *output == "json" {
		return json.NewEncoder(stdout).Encode(out)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Project: %s\n", out.Project)
	if out.DryRun {
		fmt.Fprintln(tw, "Dry run: no file written")
	}
	for i, url := range out.Jira {
		fmt.Fprintf(tw, "jira[%d]: %s\n", i, url)
	}
	for i, url := range out.Repo {
		fmt.Fprintf(tw, "repo[%d]: %s\n", i, url)
	}
	for _, url := range out.URLs {
		fmt.Fprintf(tw, "%s: %s\n", url.Name, url.URL)
	}
	fmt.Fprintln(tw, "LABEL KEY\tTEMPLATE\tEXAMPLE ("+out.ExampleValue+")")
	for _, link := range out.Links {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", link.LabelKey, link.Template, link.Example)
	}
	fmt.Fprintln(tw, "Placeholders:")
	for _, placeholder := range out.Placeholders {
		fmt.Fprintln(tw, placeholder)
	}
	fmt.Fprintln(tw, "PROJECT VARIABLE\tVALUE")
	for _, variable := range out.ProjectVariables {
		fmt.Fprintf(tw, "%s\t%s\n", variable.Placeholder, variable.Value)
	}
	return tw.Flush()
}
func linkRulesError(err error) error {
	var project *service.ProjectError
	if errors.As(err, &project) {
		return MapMetadataError(project.Err)
	}
	var refusal *service.Refusal
	if errors.As(err, &refusal) && refusal.Code == service.CodeInvalid {
		return usageError(refusal.Error())
	}
	return err
}

// parseLinkRulesArgs permits interspersed flags and a lone stdin dash while
// preserving the standard unknown-flag diagnostic and catalog Usage block.
func parseLinkRulesArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var operands []string
	for len(args) > 0 {
		if args[0] == "--" {
			return append(operands, args[1:]...), nil
		}
		if err := fs.Parse(args); err != nil {
			return nil, flagParseError(err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		operands = append(operands, rest[0])
		args = rest[1:]
	}
	return operands, nil
}
