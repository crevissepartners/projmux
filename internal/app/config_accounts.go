package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/crevissepartners/projmux/internal/config"
	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"github.com/crevissepartners/projmux/internal/services/provideraccounts"
)

func (c *configCommand) runAccounts(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageRefusal(stderr, "config accounts", "config accounts requires add, list, or remove")
	}
	switch args[0] {
	case "add":
		return c.runAccountAdd(args[1:], stdout, stderr)
	case "list":
		return c.runAccountList(args[1:], stdout, stderr)
	case "remove":
		return c.runAccountRemove(args[1:], stdout, stderr)
	default:
		return usageRefusal(stderr, "config accounts", "config accounts requires add, list, or remove")
	}
}

func (c *configCommand) accountService() provideraccounts.Service {
	return provideraccounts.Service{Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}
}

func (c *configCommand) runAccountAdd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return usageRefusal(stderr, "config accounts add", "config accounts add requires a name")
	}
	name := args[0]
	fs := flag.NewFlagSet("config accounts add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "output: json")
	provider := fs.String("provider", "", "codex or claude")
	root := fs.String("root", "", "absolute existing directory")
	if err := fs.Parse(args[1:]); err != nil {
		return usageRefusal(stderr, "config accounts add", "config accounts add: "+err.Error())
	}
	if fs.NArg() != 0 || (*output != "" && *output != "json") {
		return usageRefusal(stderr, "config accounts add", "config accounts add accepts documented arguments and -o json")
	}
	value, err := c.accountService().Register(context.Background(), account.RegisterAccountRequest{Name: name, Provider: *provider, Root: *root})
	if err != nil {
		return err
	}
	return writeAccountOutput("add", *output, value, []account.ProviderAccount{value}, stdout)
}

func (c *configCommand) runAccountList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config accounts list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "output: json")
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "config accounts list", "config accounts list: "+err.Error())
	}
	if fs.NArg() != 0 || (*output != "" && *output != "json") {
		return usageRefusal(stderr, "config accounts list", "config accounts list accepts -o json and no operands")
	}
	rows, err := c.accountService().List(context.Background())
	if err != nil {
		return err
	}
	return writeAccountOutput("list", *output, rows, rows, stdout)
}

func (c *configCommand) runAccountRemove(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return usageRefusal(stderr, "config accounts remove", "config accounts remove requires a name or ref")
	}
	ref := args[0]
	fs := flag.NewFlagSet("config accounts remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "output: json")
	if err := fs.Parse(args[1:]); err != nil {
		return usageRefusal(stderr, "config accounts remove", "config accounts remove: "+err.Error())
	}
	if fs.NArg() != 0 || (*output != "" && *output != "json") {
		return usageRefusal(stderr, "config accounts remove", "config accounts remove accepts documented arguments and -o json")
	}
	value, err := c.accountService().Remove(context.Background(), account.RemoveAccountRequest{Ref: ref})
	if err != nil {
		return err
	}
	return writeAccountOutput("remove", *output, value, []account.ProviderAccount{value}, stdout)
}

func writeAccountOutput(verb, output string, value any, rows []account.ProviderAccount, stdout io.Writer) error {
	if output == "json" {
		return accountOutputError(verb, json.NewEncoder(stdout).Encode(value))
	}
	for _, value := range rows {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", value.Name, value.UID, value.Provider, value.Root, value.Version, value.State, value.AuthenticationStatus); err != nil {
			return accountOutputError(verb, err)
		}
	}
	return nil
}

// An output failure after a mutation cannot be reported as a pre-effect refusal.
func accountOutputError(verb string, err error) error {
	if err == nil {
		return nil
	}
	if verb == "list" {
		return account.Failure(account.IOFailure, "output", err)
	}
	return &account.Error{Reason: account.Unknown, Field: "output", Cause: err}
}
