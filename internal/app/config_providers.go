package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
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
	// The usage error carries the message. The flag package's own usage dump
	// would put a second, differently worded copy on stderr.
	fs.SetOutput(io.Discard)
	enable := fs.String("enable", "", "enable one AI provider")
	disable := fs.String("disable", "", "disable one AI provider")
	if err := fs.Parse(args); err != nil {
		return usageError("config providers: " + err.Error())
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
		return c.setProvider(*enable, "--enable", true, stdout)
	case set["disable"]:
		return c.setProvider(*disable, "--disable", false, stdout)
	default:
		enabled := aiEnabledAgents(c.homeDir, c.lookupEnv)
		for _, provider := range config.KnownAIAgentProviders() {
			if _, err := fmt.Fprintln(stdout, aiProviderStatusLine(provider, aiEnabledAgentsContains(enabled, provider))); err != nil {
				return err
			}
		}
		return nil
	}
}

func (c *configCommand) setProvider(value, flagName string, enabled bool, stdout io.Writer) error {
	if strings.TrimSpace(value) == "" {
		return usageError(fmt.Sprintf("config providers %s requires a provider id: %s", flagName, knownAIProviderList()))
	}
	provider, err := knownAIProvider(value)
	if err != nil {
		return usageError(fmt.Sprintf("config providers %s: unknown provider %q; known providers: %s", flagName, value, knownAIProviderList()))
	}
	if err := setAIEnabledAgent(c.homeDir, c.lookupEnv, provider, enabled); err != nil {
		return err
	}
	// Report what a later launch will read, not what was asked for.
	_, current, err := readAIEnabledAgentsPolicy(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, aiProviderStatusLine(provider, aiEnabledAgentsContains(current, provider)))
	return err
}

func aiProviderStatusLine(provider config.AIAgentProvider, enabled bool) string {
	if enabled {
		return string(provider) + " enabled"
	}
	return string(provider) + " disabled"
}

func knownAIProviderList() string {
	known := config.KnownAIAgentProviders()
	names := make([]string, 0, len(known))
	for _, provider := range known {
		names = append(names, string(provider))
	}
	return strings.Join(names, ", ")
}

var errUnknownAIProvider = errors.New("unknown AI agent provider")

// knownAIProvider resolves one operator-typed provider id against the set the
// enabled-providers policy governs.
func knownAIProvider(value string) (config.AIAgentProvider, error) {
	normalized := config.NormalizeAIEnabledAgents([]string{value})
	if len(normalized) != 1 {
		return "", fmt.Errorf("%w: %s", errUnknownAIProvider, value)
	}
	return normalized[0], nil
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
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", nil, err
	}
	path := paths.AIEnabledAgentsFile()
	current, err := config.LoadAIEnabledAgentsFile(path)
	if err != nil {
		return "", nil, err
	}
	return path, current, nil
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
	path, current, err := readAIEnabledAgentsPolicy(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	if aiEnabledAgentsContains(current, provider) == enabled {
		return nil
	}
	members := map[config.AIAgentProvider]bool{}
	for _, agent := range current {
		members[agent] = true
	}
	members[provider] = enabled

	next := make([]config.AIAgentProvider, 0, len(config.DefaultAIEnabledAgents))
	for _, agent := range config.KnownAIAgentProviders() {
		if members[agent] {
			next = append(next, agent)
		}
	}
	return config.SaveAIEnabledAgentsFile(path, next)
}
