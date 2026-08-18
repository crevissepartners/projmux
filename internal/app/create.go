package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// createKinds lists the resource kinds `create` implements, in help order.
var createKinds = []string{"project", "window", "pane", "agent", "notification", "snapshot"}

// placementDirections is the closed placement enum shared by the Pane and Agent
// create routes. `left`/`up` are outside current parity and stay out of v2's
// first scope.
var placementDirections = []string{"right", "down"}

// defaultPlacement matches the historical `ai split` default.
const defaultPlacement = "right"

// agentLauncher is the provider-launch seam the canonical Agent create
// consumes.
//
// It is deliberately narrow. The retired split handler owned three things: how a
// provider is launched, where the pane comes from, and where the client ends up.
// Only the first belongs on a detached create, so this interface exposes exactly
// that and the managed-pane binding that follows it. The pane itself is created
// by the materializer, which owns `-d` and the rollback ledger.
type agentLauncher interface {
	// RequireAgentEnabled applies the Settings enabled-agents gate.
	RequireAgentEnabled(provider string) error
	// PlanAgentLaunch builds the launch argv and the pane title for one
	// provider. It creates nothing, so a failure here costs zero mutations.
	PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (title string, argv []string, err error)
	// BindManagedAgentPane applies the managed-agent pane options without the
	// legacy title/content watcher.
	BindManagedAgentPane(paneID, provider, contextDir, title string)
	// AwaitAgentActivation observes bounded provider/hook metadata only. It
	// never reads pane content and runs after the create transaction releases
	// the Registry lock.
	AwaitAgentActivation(context.Context, tmuxCommandRunner, string, time.Duration) (bool, string, error)
}

// createCommand implements the canonical `create` verb.
//
// Every kind reaches one parser and one product model. There is no dispatch
// discriminator: `--project` is a scope flag, not a mode selector, so the same
// argv means the same thing whether or not it is present. When it is omitted the
// scope comes from the active exact tmux runtime, which is resolved through the
// same registry mirror every other implicit-target route reads. See
// resolveCreateScope for the exact rule and its refusals.
//
// Three separations are load bearing:
//
//   - A plain shell split is a Pane, not an Agent, so it reaches `create pane`
//     and `shell` is not a member of the provider enum.
//   - `--provider` is required on `create agent`. A saved default split mode is
//     legacy behavior that stays reachable through the generated-config
//     `internal agent-pane launch-default` bridge; promoting it here would make
//     the canonical route's result depend on hidden state.
//   - The provider shortcuts carry the provider in the command name, so passing
//     `--provider` as well is a usage error rather than a silent winner.
type createCommand struct {
	// notify and snapshots are parity-forwarder seams. They keep the canonical
	// resource spellings on the exact handlers and leaf parsers that own the
	// legacy routes.
	notify    rawArgvCommand
	snapshots rawArgvCommand
	// agents builds the provider launch of the `create agent` route.
	agents agentLauncher
	// store is the locked registry file. Every resource route touches it, and
	// the implicit-scope resolution reads it before the transaction opens.
	store *resourceStore
	// reconciler brings the registry up to date with the machine before a
	// selector is resolved: it imports the live sessions it can attribute and
	// refreshes status. It no longer registers the configured discovery roots, so
	// `--project <name>` matches a Project that something explicitly registered --
	// `create project`, or opening a candidate from the sidebar -- and a name that
	// matches only a discovered directory is a refusal that names those routes.
	reconciler *registryReconciler
	// runtime performs the detached tmux mutations and owns the rollback.
	runtime *materializer
	// activeTarget observes the tmux target this invocation runs in. It is the
	// only source of an omitted create scope, and it is nil-safe: a create with
	// no --project outside tmux refuses rather than guessing a server.
	activeTarget activeTargetLookup
	// shell is the configured shell whose basename seeds default names and
	// whose process a payload-free Pane runs.
	shell          string
	sessionNameFor func(root string) string
	newOperationID func() (string, error)
	// newGeneration mints the opaque activation generation one materialized
	// Pane's supervisor quotes back when its child stops.
	newGeneration    func() (string, error)
	resolveWorkspace func(coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
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
			runner:     runner,
			mirror:     intmetadata.NewMirror(runner),
			sessions:   client,
			warn:       os.Stderr,
			executable: os.Executable,
			lookupEnv:  os.Getenv,
		},
		activeTarget:     defaultActiveTargetLookup(),
		shell:            configuredShell(os.Getenv),
		sessionNameFor:   namer.SessionName,
		newOperationID:   newCreateOperationID,
		newGeneration:    coremetadata.NewGeneration,
		resolveWorkspace: resolveAgentWorkspace,
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
//
// Every branch below reaches exactly one handler. A kind is never routed twice
// and never chooses between two product models, which is what makes the argv
// surface of `create` a single contract.
func (c *createCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("create requires a resource kind (%s) or a provider shortcut (%s)",
			strings.Join(createKinds, ", "), strings.Join(cli.ProviderCreateShortcuts(), ", ")))
	}
	token := args[0]
	rest := args[1:]
	switch token {
	case "project":
		return c.runResourceProject(rest, stdout, stderr)
	case "agent":
		return c.runResourceAgent("", rest, stdout, stderr)
	case "window":
		return c.runResourceWindow(rest, stdout, stderr)
	case "pane":
		return c.runResourcePane(rest, stdout, stderr)
	case "notification":
		return forwardRawArgv(c.notify, "create notification", "notify", []string{"push"}, rest, stdout, stderr)
	case "snapshot":
		return forwardRawArgv(c.snapshots, "create snapshot", "session-state", []string{"save"}, rest, stdout, stderr)
	}
	// A provider shortcut normalizes to `create agent --provider <id>`; the
	// provider is already specified, so repeating it is an error.
	for _, provider := range cli.ProviderCreateShortcuts() {
		if token == provider {
			return c.runResourceAgent(provider, rest, stdout, stderr)
		}
	}
	return usageError(fmt.Sprintf("create %s is not available; this release implements kinds %s and provider shortcuts %s",
		token, strings.Join(createKinds, ", "), strings.Join(cli.ProviderCreateShortcuts(), ", ")))
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
		return "", usageError(fmt.Sprintf("%s: %q is an interactive picker, not a provider; choose one of %s for `projmux create agent --provider`",
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
