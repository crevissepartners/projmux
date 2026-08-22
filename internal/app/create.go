package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
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
	// AwaitAgentActivation observes bounded provider/hook metadata only. Startup
	// readiness and initial-task acknowledgement have independent bounds; it
	// never reads pane content and runs after the create transaction releases
	// the Registry lock.
	AwaitAgentActivation(context.Context, tmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error)
}

type codexCapabilityAgentLauncher interface {
	PlanAgentLaunchWithCapability(provider string, workspace coremetadata.AgentWorkspace, payload []string, selection corecap.Selection) (title string, argv []string, err error)
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
	// resumes builds the provider *resume* launch. It is the same object as
	// agents and a separate field on purpose: the split UI's resume selection may
	// only ever reach PlanAgentResume, and keeping the two launch seams apart is
	// what makes "a resume never falls through to a fresh conversation" a
	// property of the type system rather than of a code review.
	resumes agentResumeLauncher
	// codexNative prepares exact app-server thread identity. Nil is the bounded
	// compatibility state and leaves the current CLI/hook path unchanged.
	codexNative codexNativeThreadController
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
	// anchorTarget builds the activeTarget of one invocation whose anchor pane is
	// stated rather than inherited. Only the split UI supplies an anchor, and it
	// supplies it per intent, which is what keeps the popup's origin pane out of
	// every other verb's scope resolution. See createFromIntent.
	anchorTarget func(paneID string) activeTargetLookup
	// shell is the configured shell whose basename seeds default names and
	// whose process a payload-free Pane runs.
	shell          string
	sessionNameFor func(root string) string
	newOperationID func() (string, error)
	// newGeneration mints the opaque activation generation one materialized
	// Pane's supervisor quotes back when its child stops.
	newGeneration    func() (string, error)
	resolveWorkspace func(coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
	// bindRuntime resolves the invocation's app-owned logical route lazily at
	// the first runtime action. Construction and help remain read/write free.
	bindRuntime  func(context.Context) error
	runtimeBound bool
}

func newCreateCommand() *createCommand {
	runner := inttmux.ExecRunner{}
	target := explicitTmuxTarget{flag: "-L", value: defaultAppSocket}
	routed := explicitTmuxRunner{runner: runner, target: target}
	client := defaultTmuxClientWithRunner(routed)
	home, _ := os.UserHomeDir()
	namer := coresessions.NewNamer(home)
	command := &createCommand{
		store:      newResourceStore(),
		reconciler: newRegistryReconciler(routed, client),
		runtime: &materializer{
			runner:     routed,
			mirror:     intmetadata.NewMirror(routed),
			sessions:   client,
			target:     target,
			warn:       os.Stderr,
			executable: os.Executable,
			lookupEnv:  os.Getenv,
		},
		activeTarget:     defaultActiveTargetLookup(),
		anchorTarget:     defaultAnchoredActiveTargetLookup,
		shell:            configuredShell(os.Getenv),
		sessionNameFor:   namer.SessionName,
		newOperationID:   newCreateOperationID,
		newGeneration:    coremetadata.NewGeneration,
		resolveWorkspace: resolveAgentWorkspace,
	}
	command.bindRuntime = func(ctx context.Context) error {
		route, err := resolveInvocationRuntimeMutationRoute(ctx, runner, os.Getenv)
		if err != nil {
			return err
		}
		exact := explicitTmuxRunner{runner: runner, target: route.target}
		client := defaultTmuxClientWithSocketRunner(exact, route.socketName)
		command.reconciler = newRegistryReconciler(exact, client)
		command.runtime.runner = exact
		command.runtime.mirror = intmetadata.NewMirror(exact)
		command.runtime.sessions = client
		command.runtime.target = route.target
		command.runtime.expectedSocketPath = route.expectedSocketPath
		return nil
	}
	return command
}

func (c *createCommand) ensureRuntimeRoute(ctx context.Context) error {
	if c == nil || c.runtimeBound || c.bindRuntime == nil {
		return nil
	}
	if err := c.bindRuntime(ctx); err != nil {
		return err
	}
	c.runtimeBound = true
	return nil
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

// agentPaneIntent is the canonical create intent the Projmux split UI produces.
//
// The split UI -- the saved default binding, the Alt-7 picker, the resume picker,
// the provider and shell direct actions -- decides *what the operator asked for*
// and nothing else. It does not know where the pane comes from, which Window owns
// it, or what the Registry has to allocate first, because those are create's
// business and there is exactly one implementation of them. Before this type the
// UI reached tmux's `split-window` directly and produced panes the Registry had
// never heard of, which is why a pane opened from the picker existed in the Main
// UI only until something reconciled.
type agentPaneIntent struct {
	// producer is the closed UI boundary that stated this intent. It is not
	// presentation metadata: the producer x root-kind table is executable, so a
	// new UI path cannot silently inherit authority it never classified.
	producer canonicalCreateProducer
	// provider is empty for a plain shell Pane. A shell surface is a Pane and not
	// an Agent, so an empty provider selects a different kind rather than a
	// default one.
	provider string
	// placement is the closed right/down enum. The anchor is always the Pane the
	// operator pressed the key in -- inherited by a producer that runs in that
	// pane, stated by anchorPaneID for one that does not.
	placement string
	// conversationID joins a provider conversation the machine already has
	// instead of starting a new one. It requires a provider.
	conversationID string
	// resumeSource is private picker provenance. A native Codex row keeps the
	// app-server resume lane; a rollout row keeps the current CLI lane.
	resumeSource string
	// codexCapability is a connection/version-bound picker selection. It is a
	// private UI intent field, not a public create flag or persisted config.
	codexCapability *corecap.Selection
	// anchorPaneID states the origin pane this split hangs off, for the one
	// producer that cannot let create infer it: a tmux popup inherits $TMUX but
	// no $TMUX_PANE, so the picker running inside one has no target of its own
	// and carries the pane the keypress came from instead. It is empty for a
	// producer that runs in the anchor pane, which is the case create resolves
	// exactly as it does for a typed invocation.
	anchorPaneID string
	// targetClient is the exact tmux client that originated an asynchronous UI
	// action. It is diagnostic routing only and never identity evidence.
	targetClient string
}

// canonicalCreateProducer is the closed set of UI producers allowed to create
// a managed descendant from an exact Pane origin. Public `create` is not a
// member: its omitted scope remains Project-only and follows the typed command
// contract in resolveCreateScope.
type canonicalCreateProducer string

const (
	canonicalProducerPaneMenu       canonicalCreateProducer = "pane-menu"
	canonicalProducerSavedDefault   canonicalCreateProducer = "saved-default"
	canonicalProducerProviderPicker canonicalCreateProducer = "provider-picker"
	canonicalProducerResumePicker   canonicalCreateProducer = "resume-picker"
	canonicalProducerDirectProvider canonicalCreateProducer = "direct-provider"
	canonicalProducerDirectShell    canonicalCreateProducer = "direct-shell"
	// Window lifecycle producers are separate from the Pane menu producer even
	// though they share the exact-origin resolver. Keeping their intent labels
	// distinct makes the generated artifact -> handler inventory bijective.
	canonicalProducerWindowCreate canonicalCreateProducer = "window-create"
	canonicalProducerWindowRename canonicalCreateProducer = "window-rename"
)

var canonicalCreateProducers = []canonicalCreateProducer{
	canonicalProducerPaneMenu,
	canonicalProducerSavedDefault,
	canonicalProducerProviderPicker,
	canonicalProducerResumePicker,
	canonicalProducerDirectProvider,
	canonicalProducerDirectShell,
}

var canonicalWindowMutationProducers = []canonicalCreateProducer{
	canonicalProducerWindowCreate,
	canonicalProducerWindowRename,
}

func (p canonicalCreateProducer) valid() bool {
	return slices.Contains(canonicalCreateProducers, p) || slices.Contains(canonicalWindowMutationProducers, p)
}

// canonicalPaneCreator is the seam the split UI hands its intents to.
type canonicalPaneCreator interface {
	createFromIntent(intent agentPaneIntent, stdout, stderr io.Writer) error
}

var _ canonicalPaneCreator = (*createCommand)(nil)

// createFromIntent projects one split-UI intent onto the canonical create route.
//
// The provider and shell branches render the exact argv an operator would type,
// which is deliberate: it keeps one parser and one set of refusals in play, so a
// UI action and a typed command cannot disagree about what `--placement down`
// means. The resume branch cannot be spelled in argv -- there is no public
// `--resume` on create, and adding one would widen the public surface for an
// interactive selection -- so it reaches the shared body with the one extra field
// set.
func (c *createCommand) createFromIntent(intent agentPaneIntent, stdout, stderr io.Writer) error {
	argv, provider, conversation, err := intent.canonicalArgv()
	if err != nil {
		return err
	}
	if !intent.producer.valid() {
		return usageError("canonical create intent has no classified producer; nothing was created")
	}
	if provider != "" {
		if c.agents == nil {
			return errors.New("create agent: the provider launcher is not configured")
		}
		if err := c.agents.RequireAgentEnabled(provider); err != nil {
			return err
		}
	}
	scope, err := c.resolveCanonicalIntentScope(intent)
	if err != nil {
		return err
	}
	if provider == "" {
		return visibleCanonicalCreateError(c.createCanonicalIntentPane(scope, intent, stdout))
	}
	// A resume cannot be spelled: `create` has no public `--resume`. The intent
	// route still parses the exact public argv before attaching its private
	// conversation field, so placement and provider validation remain shared.
	shape := resourceCreateShape{split: true, provider: true}
	flags, err := parseResourceCreateFlags(canonicalCreateAgent, argv[1:], stderr, shape)
	if err != nil {
		return err
	}
	flags.resumeConversation = conversation
	flags.resumeSource = strings.TrimSpace(intent.resumeSource)
	flags.codexCapability = intent.codexCapability
	return visibleCanonicalCreateError(c.createCanonicalIntentAgent(scope, intent, provider, flags, stdout))
}

// visibleCanonicalCreateError prevents a subprocess ExitCode from escaping a
// UI create. cmd/projmux deliberately suppresses its default stderr print for
// such errors because subprocess-owning commands are expected to have printed;
// canonical create owns the diagnostic instead, so it preserves the exact text
// on a plain error that the originating popup/client can display.
func visibleCanonicalCreateError(err error) error {
	if err == nil {
		return nil
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return errors.New(err.Error())
	}
	return err
}

// canonicalArgv renders one intent as the argv an operator would type, and
// returns the resolved provider and conversation alongside it.
//
// It is a pure function so the projection is assertable on its own: "the UI
// action and the typed command mean the same thing" is a property of this
// mapping, and a test that had to run a create to see the argv would be
// measuring the create instead.
func (i agentPaneIntent) canonicalArgv() (argv []string, provider, conversation string, err error) {
	placement := strings.TrimSpace(i.placement)
	if !slices.Contains(placementDirections, placement) {
		return nil, "", "", usageError(fmt.Sprintf("agent pane intent placement must be one of: %s",
			strings.Join(placementDirections, ", ")))
	}
	raw := strings.TrimSpace(i.provider)
	conversation = strings.TrimSpace(i.conversationID)
	if raw == "" {
		if conversation != "" {
			return nil, "", "", usageError("agent pane intent cannot resume a conversation without a provider; a shell surface is a Pane")
		}
		return []string{"pane", "--placement", placement}, "", "", nil
	}
	canonical, err := requireCanonicalProvider(canonicalCreateAgent, raw)
	if err != nil {
		return nil, "", "", err
	}
	return []string{"agent", "--provider", canonical, "--placement", placement}, canonical, conversation, nil
}
