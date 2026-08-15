package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
)

// createKinds lists the resource kinds `create` implements, in help order.
var createKinds = []string{"agent", "pane"}

// placementDirections is the closed placement enum shared by the Pane and Agent
// create routes. `left`/`up` are outside current parity and stay out of v2's
// first scope.
var placementDirections = []string{"right", "down"}

// defaultPlacement matches the historical `ai split` default.
const defaultPlacement = "right"

// createCommand implements the canonical `create` verb for the two kinds the
// Agent decomposition splits apart.
//
// This is a parity-first alias, not a second implementation. `create agent` and
// `create pane` normalize the spelling — an explicit `--provider`, a named
// `--placement` instead of a bare positional, and `-o pane-id` instead of the
// `--print-pane-id` boolean — and then hand the work to the existing `ai split`
// handler, so the launched pane, the stdout bytes, and the exit code are the
// ones that shipped before.
//
// Three separations are load bearing:
//
//   - A plain shell split is a Pane, not an Agent, so it reaches `create pane`
//     and `shell` is not a member of the provider enum.
//   - `--provider` is required on the canonical route. A saved default split
//     mode is legacy behavior that stays reachable through `ai split`; promoting
//     it here would make the canonical route's result depend on hidden state.
//   - The provider shortcuts carry the provider in the command name, so passing
//     `--provider` as well is a usage error rather than a silent winner.
type createCommand struct {
	ai rawArgvCommand
}

func newCreateCommand() *createCommand {
	return &createCommand{}
}

// Run dispatches one `create <kind|provider>` invocation.
func (c *createCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("create requires a resource kind (%s) or a provider shortcut (%s)",
			strings.Join(createKinds, ", "), strings.Join(cli.ProviderCreateShortcuts(), ", ")))
	}
	token := args[0]
	rest := args[1:]
	switch token {
	case "agent":
		return c.runAgent(rest, stdout, stderr)
	case "pane":
		return c.runPane(rest, stdout, stderr)
	}
	// A provider shortcut normalizes to `create agent --provider <id>`; the
	// provider is already specified, so repeating it is an error.
	for _, provider := range cli.ProviderCreateShortcuts() {
		if token == provider {
			return c.runProviderShortcut(provider, rest, stdout, stderr)
		}
	}
	return usageError(fmt.Sprintf("create %s is not available; this release implements kinds %s and provider shortcuts %s",
		token, strings.Join(createKinds, ", "), strings.Join(cli.ProviderCreateShortcuts(), ", ")))
}

// createFlags is the shared canonical create surface.
type createFlags struct {
	provider    string
	providerSet bool
	placement   string
	output      string
	payload     []string
}

// parseCreateFlags parses the canonical create argv.
//
// The payload after a bare `--` is split off before flag parsing so projmux
// never reinterprets it, which is the same guarantee the current `ai split`
// spelling gives.
func parseCreateFlags(spelling string, args []string, stderr io.Writer, withProvider bool) (createFlags, error) {
	out := createFlags{placement: defaultPlacement}
	head := args
	for i, arg := range args {
		if arg == "--" {
			head = args[:i]
			out.payload = append([]string(nil), args[i+1:]...)
			if len(out.payload) == 0 {
				return createFlags{}, usageError(spelling + " -- requires a payload")
			}
			break
		}
	}

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if withProvider {
		fs.StringVar(&out.provider, "provider", "", "Agent provider: "+strings.Join(cli.AgentProviders(), "|"))
	}
	fs.StringVar(&out.placement, "placement", defaultPlacement, "split placement: "+strings.Join(placementDirections, "|"))
	fs.StringVar(&out.output, "output", "", "result projection")
	fs.StringVar(&out.output, "o", "", "result projection (alias of --output)")
	if err := fs.Parse(head); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return createFlags{}, err
		}
		return createFlags{}, usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return createFlags{}, usageError(fmt.Sprintf(
			"%s does not accept positional arguments; got %q. Use --placement %s",
			spelling, fs.Arg(0), strings.Join(placementDirections, "|")))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "provider" {
			out.providerSet = true
		}
	})
	if !slices.Contains(placementDirections, out.placement) {
		return createFlags{}, usageError(fmt.Sprintf("%s --placement must be one of: %s",
			spelling, strings.Join(placementDirections, ", ")))
	}
	return out, nil
}

// resolveCreateOutput maps the canonical `-o` token onto what this release can
// actually emit.
//
// The projections that need a registry-backed Agent resource are valid tokens
// whose data does not exist yet, so they fail as runtime errors rather than
// usage errors: exit 2 must keep meaning "the operator typed something wrong".
func resolveCreateOutput(spelling, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	mode, field, err := cli.ResolveOutputToken(spelling, token)
	if err != nil {
		return false, usageError(err.Error())
	}
	if field != "" {
		return false, usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}
	switch mode {
	case cli.OutputModePaneID:
		return true, nil
	case cli.OutputModeNone:
		return false, nil
	default:
		return false, fmt.Errorf("%s -o %s needs the resource-backed Agent create composition, which is not wired yet", spelling, mode)
	}
}

// runAgent answers the canonical `create agent`.
func (c *createCommand) runAgent(args []string, stdout, stderr io.Writer) error {
	const spelling = "create agent"

	flags, err := parseCreateFlags(spelling, args, stderr, true)
	if err != nil {
		return err
	}
	provider, err := requireCanonicalProvider(spelling, flags.provider)
	if err != nil {
		return err
	}
	return c.dispatchSplit(spelling, provider, flags, stdout, stderr)
}

// runProviderShortcut answers `create codex|claude|antigravity`.
func (c *createCommand) runProviderShortcut(provider string, args []string, stdout, stderr io.Writer) error {
	spelling := "create " + provider

	flags, err := parseCreateFlags(spelling, args, stderr, true)
	if err != nil {
		return err
	}
	if flags.providerSet {
		return usageError(fmt.Sprintf(
			"%s already names the provider; drop --provider or use `projmux create agent --provider %s`",
			spelling, strings.TrimSpace(flags.provider)))
	}
	return c.dispatchSplit(spelling, provider, flags, stdout, stderr)
}

// runPane answers the canonical `create pane`: the legacy shell split, on the
// Pane route where a shell surface belongs.
func (c *createCommand) runPane(args []string, stdout, stderr io.Writer) error {
	const spelling = "create pane"

	flags, err := parseCreateFlags(spelling, args, stderr, false)
	if err != nil {
		return err
	}
	if len(flags.payload) > 0 {
		return usageError(spelling + " does not take a payload yet; the shell Pane command arrives with the Pane create composition")
	}
	printPaneID, err := resolveCreateOutput(spelling, flags.output)
	if err != nil {
		return err
	}
	return forwardRawArgv(c.ai, spelling, "ai", splitArgv(aiModeShell, flags.placement, printPaneID, nil), nil, stdout, stderr)
}

// requireCanonicalProvider enforces the canonical Provider enum.
//
// The picker adapters get their own message because refusing them as "unknown"
// would hide the actual model: they are selection surfaces that end in one of
// these providers, not providers.
func requireCanonicalProvider(spelling, raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" {
		return "", usageError(fmt.Sprintf("%s requires --provider (%s); the saved split mode is not a canonical default",
			spelling, strings.Join(cli.AgentProviders(), ", ")))
	}
	if cli.IsPickerAdapter(provider) {
		return "", usageError(fmt.Sprintf("%s: %q is an interactive picker, not a provider; pick one of %s or run `projmux ai picker`",
			spelling, provider, strings.Join(cli.AgentProviders(), ", ")))
	}
	if provider == aiModeShell {
		return "", usageError(fmt.Sprintf("%s: a shell surface is a Pane, not an Agent; use `projmux create pane`", spelling))
	}
	if !cli.IsAgentProvider(provider) {
		return "", usageError(fmt.Sprintf("%s: unknown provider %q; accepted providers: %s",
			spelling, provider, strings.Join(cli.AgentProviders(), ", ")))
	}
	return provider, nil
}

// dispatchSplit forwards a normalized create onto the existing split handler.
func (c *createCommand) dispatchSplit(spelling, provider string, flags createFlags, stdout, stderr io.Writer) error {
	printPaneID, err := resolveCreateOutput(spelling, flags.output)
	if err != nil {
		return err
	}
	return forwardRawArgv(c.ai, spelling, "ai", splitArgv(provider, flags.placement, printPaneID, flags.payload), nil, stdout, stderr)
}

// splitArgv renders the `ai split` argv a normalized create maps onto. The
// placement positional and the payload terminator keep their historical order,
// so the handler parses exactly what the current spelling hands it.
func splitArgv(agent, placement string, printPaneID bool, payload []string) []string {
	argv := []string{"split", "--agent", agent}
	if printPaneID {
		argv = append(argv, "--print-pane-id")
	}
	argv = append(argv, placement)
	if len(payload) > 0 {
		argv = append(argv, "--")
		argv = append(argv, payload...)
	}
	return argv
}
