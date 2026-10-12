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

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/projectcandidates"
)

func (c *getCommand) runProjectCandidates(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("get project-candidates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	output := ""
	fs.StringVar(&output, "o", "", "output mode: json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		return usageError("get project-candidates does not accept positional arguments")
	}
	if output != "" && output != "json" {
		return usageError("get project-candidates -o must be json")
	}
	service := c.projectCandidates
	if service == nil {
		service = &projectcandidates.Service{Paths: config.DefaultPathsFromEnv, HomeDir: os.UserHomeDir, LookupEnv: os.Getenv, TmuxProjdir: tmuxProjdirOption, LoadRegistry: c.loadRegistry}
	}
	out, err := service.List(context.Background())
	if err != nil {
		return err
	}
	if output == "json" {
		return json.NewEncoder(stdout).Encode(out)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "PATH\tPINNED\tPROJECT UID"); err != nil {
		return err
	}
	for _, candidate := range out.Candidates {
		if _, err := fmt.Fprintf(w, "%s\t%t\t%s\n", candidate.Path, candidate.Pinned, candidate.ProjectUID); err != nil {
			return err
		}
	}
	return w.Flush()
}
