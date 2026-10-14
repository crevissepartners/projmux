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
		return usageError("config accounts requires add, list, or remove")
	}
	verb := args[0]
	rest := args[1:]
	if verb != "add" && verb != "list" && verb != "remove" {
		return usageError("config accounts requires add, list, or remove")
	}
	var ref string
	// The documented positional name/ref precedes leaf flags.
	if verb != "list" {
		if len(rest) == 0 || rest[0] == "" || rest[0][0] == '-' {
			return usageError("config accounts " + verb + " requires a name or ref")
		}
		ref = rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("config accounts "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "output: json")
	var provider, root string
	if verb == "add" {
		fs.StringVar(&provider, "provider", "", "codex or claude")
		fs.StringVar(&root, "root", "", "absolute existing directory")
	}
	if err := fs.Parse(rest); err != nil {
		return usageRefusal(stderr, "config accounts "+verb, "config accounts: "+err.Error())
	}
	if fs.NArg() != 0 || (*output != "" && *output != "json") {
		return usageError("config accounts accepts documented arguments and -o json")
	}
	s := provideraccounts.Service{Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}
	ctx := context.Background()
	var value any
	var rows []account.ProviderAccount
	switch verb {
	case "add":
		v, err := s.Register(ctx, account.RegisterAccountRequest{Name: ref, Provider: provider, Root: root})
		if err != nil {
			return err
		}
		value = v
		rows = []account.ProviderAccount{v}
	case "list":
		v, err := s.List(ctx)
		if err != nil {
			return err
		}
		value = v
		rows = v
	case "remove":
		v, err := s.Remove(ctx, account.RemoveAccountRequest{Ref: ref})
		if err != nil {
			return err
		}
		value = v
		rows = []account.ProviderAccount{v}
	}
	if *output == "json" {
		return accountOutputError(verb, json.NewEncoder(stdout).Encode(value))
	}
	for _, v := range rows {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", v.Name, v.UID, v.Provider, v.Root, v.Version, v.State, v.AuthenticationStatus); err != nil {
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
