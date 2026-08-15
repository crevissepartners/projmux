package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// createKinds lists the resource kinds `create` implements, in help order.
var createKinds = []string{"window", "pane", "agent"}

// placementDirections is the closed placement enum shared by the Pane and Agent
// create routes. `left`/`up` are outside current parity and stay out of v2's
// first scope.
var placementDirections = []string{"right", "down"}

// defaultPlacement matches the historical `ai split` default.
const defaultPlacement = "right"

// agentLauncher is the provider-launch seam the canonical Agent create
// consumes.
//
// It is deliberately narrow. The legacy split handler owns three things: how a
// provider is launched, where the pane comes from, and where the client ends up.
// Only the first belongs on a detached create, so this interface exposes exactly
// that and the managed-pane binding that follows it. The pane itself is created
// by the materializer, which owns `-d` and the rollback ledger.
type agentLauncher interface {
	// RequireAgentEnabled applies the Settings enabled-agents gate.
	RequireAgentEnabled(provider string) error
	// PlanAgentLaunch builds the launch argv and the pane title for one
	// provider. It creates nothing, so a failure here costs zero mutations.
	PlanAgentLaunch(provider, contextDir string, payload []string) (title string, argv []string, err error)
	// BindManagedAgentPane applies the managed-agent pane options and starts
	// the title watcher on an already-created pane.
	BindManagedAgentPane(paneID, provider, contextDir, title string)
}

// createCommand implements the canonical `create` verb.
//
// Each kind has two halves separated by one discriminator. With `--project` the
// route is the resource-backed detached create: it resolves the registry, splits
// the resolved Windows through the materializer, and never moves the client.
// Without it the route is the `ai split` bridge that shipped first, kept
// byte-identical -- the launched pane, the stdout bytes, the exit code, and the
// focus effect are the ones that shipped before.
//
// Three separations are load bearing on both halves:
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
	// agents builds the provider launch of the canonical `create agent` route.
	// The legacy `--project`-less bridge never touches it.
	agents agentLauncher
	// store is the locked registry file. Only the resource-backed routes touch
	// it; the legacy `create pane` path never opens it.
	store *resourceStore
	// reconciler brings the registry up to date with the machine before a
	// selector is resolved. Without it `--project <name>` has nothing to match:
	// there is no other production path that registers a Project.
	reconciler *registryReconciler
	// runtime performs the detached tmux mutations and owns the rollback.
	runtime *materializer
	// shell is the configured shell whose basename seeds default names and
	// whose process a payload-free Pane runs.
	shell          string
	sessionNameFor func(root string) string
	newOperationID func() (string, error)
}

func newCreateCommand() *createCommand {
	runner := inttmux.ExecRunner{}
	client := defaultTmuxClient()
	home, _ := os.UserHomeDir()
	namer := coresessions.NewNamer(home)
	return &createCommand{
		store:      newResourceStore(),
		reconciler: newRegistryReconciler(runner, client),
		runtime: &materializer{
			runner:   runner,
			mirror:   intmetadata.NewMirror(runner),
			sessions: client,
			warn:     os.Stderr,
		},
		shell:          configuredShell(os.Getenv),
		sessionNameFor: namer.SessionName,
		newOperationID: newCreateOperationID,
	}
}

// newCreateOperationID mints the id that labels one create transaction and its
// created-resource ledger.
func newCreateOperationID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create: read operation id entropy: %w", err)
	}
	return "op-" + hex.EncodeToString(buf), nil
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
		// Same dispatch discriminator as `create pane`. With `--project` this
		// is the canonical resource-backed detached Agent create; without it
		// the invocation is the `ai split` bridge this route already shipped,
		// kept byte-identical down to the argv it forwards.
		if hasProjectFlag(rest) {
			return c.runResourceAgent("", rest, stdout, stderr)
		}
		return c.runAgent(rest, stdout, stderr)
	case "window":
		return c.runResourceWindow(rest, stdout, stderr)
	case "pane":
		// Dispatch discriminator, not a mode flag. `--project` selects the
		// canonical resource-backed Pane create; without it the invocation is
		// the shell split this route already shipped, kept byte-identical.
		if hasProjectFlag(rest) {
			return c.runResourcePane(rest, stdout, stderr)
		}
		return c.runPane(rest, stdout, stderr)
	}
	// A provider shortcut normalizes to `create agent --provider <id>`; the
	// provider is already specified, so repeating it is an error.
	for _, provider := range cli.ProviderCreateShortcuts() {
		if token == provider {
			if hasProjectFlag(rest) {
				return c.runResourceAgent(provider, rest, stdout, stderr)
			}
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

// resolveCreateOutput maps the `-o` token of the legacy `ai split` bridge onto
// what that bridge can emit.
//
// The bridge launches into the current Window and produces no Projmux resource,
// so the identity projections have nothing to project. That is a fixable input
// error rather than a missing feature: the same token works on the canonical
// route, and the fix is a single flag. Naming that flag is the whole point of
// the message, and exit 2 is correct because the operator typed a combination
// that cannot mean anything.
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
		return false, usageError(fmt.Sprintf(
			"%s -o %s projects a Projmux resource, which the compatibility split does not create; add --project <ref> for the resource-backed create",
			spelling, mode))
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
