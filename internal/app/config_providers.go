package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/settings"
)

// runProviders answers `config providers [--enable <id> | --disable <id>]`.
//
// The enabled-providers set is a central policy: `create agent`, `create
// window`, and `agent resume` refuse a disabled provider, and their refusal
// names this route as the way back. So the route is the CLI door onto exactly
// the policy the Settings "Enabled providers" toggle edits -- the same file, the
// same provider list, and the same write helper -- rather than a second
// implementation of it.
//
// Output is en-US and never reads the locale: it is public route stdout, which
// the D5 boundary keeps locale-independent. One line per provider, `<id>
// enabled` or `<id> disabled`; a mutation prints the resulting line for the one
// provider it touched.
func (c *configCommand) runProviders(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("config providers", flag.ContinueOnError)
	// The refusal prints the prefixed reason and the catalog Usage. The flag
	// package's own output would put a second, differently worded copy of
	// the reason on stderr.
	fs.SetOutput(io.Discard)
	enable := fs.String("enable", "", "enable one AI provider")
	disable := fs.String("disable", "", "disable one AI provider")
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "config providers", "config providers: "+err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("config providers does not accept positional arguments: %s", strings.Join(fs.Args(), " ")))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["enable"] && set["disable"] {
		return usageError("config providers accepts only one of --enable or --disable")
	}

	switch {
	case set["enable"]:
		return c.setProvider(*enable, true, stdout)
	case set["disable"]:
		return c.setProvider(*disable, false, stdout)
	default:
		result, err := (settings.Service{Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}).Get(context.Background(), "providers")
		if err != nil {
			return err
		}
		for _, p := range result.Providers {
			if _, err := fmt.Fprintln(stdout, aiProviderStatusLine(config.AIAgentProvider(p.ID), p.Enabled)); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *configCommand) setProvider(value string, enabled bool, stdout io.Writer) error {
	result, err := (settings.Service{Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}).Set(context.Background(), settings.Update{Key: "providers", Provider: value, Enabled: enabled})
	if err != nil {
		return err
	}
	provider, _ := settings.KnownAIProvider(value)
	for _, p := range result.Providers {
		if p.ID == string(provider) {
			_, err := fmt.Fprintln(stdout, aiProviderStatusLine(provider, p.Enabled))
			return err
		}
	}
	return nil
}

func aiProviderStatusLine(provider config.AIAgentProvider, enabled bool) string {
	if enabled {
		return string(provider) + " enabled"
	}
	return string(provider) + " disabled"
}

// knownAIProvider resolves one operator-typed provider id against the set the
// enabled-providers policy governs.
func knownAIProvider(value string) (config.AIAgentProvider, error) {
	return settings.KnownAIProvider(value)
}

// readAIEnabledAgentsPolicy reads the enabled-providers file strictly. Unlike
// aiEnabledAgents, which a launch gate uses and which falls back to the shipped
// default on any failure, a writer must not rebuild the policy from a default
// it merely failed to read.
//
// A missing file means the shipped default (every known provider). A present
// file is authoritative even when it names nothing, so disabling every provider
// persists as "none enabled" rather than falling back to the default.
func readAIEnabledAgentsPolicy(homeDir func() (string, error), lookupEnv func(string) string) (string, []config.AIAgentProvider, error) {
	return settings.ReadAIEnabledAgentsPolicy(homeDir, lookupEnv)
}

// setAIEnabledAgent is the one write path of the enabled-providers policy. The
// Settings "Enabled providers" toggle calls it with the negation of the current
// state; `config providers --enable|--disable` calls it with a fixed value.
//
// The result is written in known-provider order. A call that would not change
// membership writes nothing, so an idempotent `--enable` on a fresh install
// does not freeze the shipped default into a file that a later release's new
// provider would then be missing from.
func setAIEnabledAgent(homeDir func() (string, error), lookupEnv func(string) string, provider config.AIAgentProvider, enabled bool) error {
	return settings.SetAIEnabledAgent(homeDir, lookupEnv, provider, enabled)
}
