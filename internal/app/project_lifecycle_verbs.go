package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The canonical spellings this file makes executable.
const (
	canonicalStartProject = "start project"
	canonicalOpenProject  = "open project"
	canonicalStopProject  = "stop project"
)

// projectLifecycleVerb is the closed set of runtime lifecycle verbs that take a
// Project and change nothing but the runtime and focus axes.
type projectLifecycleVerb string

const (
	projectLifecycleStart projectLifecycleVerb = "start"
	projectLifecycleOpen  projectLifecycleVerb = "open"
	projectLifecycleStop  projectLifecycleVerb = "stop"
)

// projectLifecycleCommand implements `start|open|stop project`.
//
// The three verbs are one command because they are one decision with three
// answers. Every one of them resolves exactly one Project, reads whether its
// persistent session is live, and then differs only in what it does about that:
// start materializes and stays put, open materializes and moves the client,
// stop ends the session. Splitting them into three handlers would have produced
// three copies of the resolution and liveness half, which is precisely the half
// that has to agree for the receipts to mean anything.
//
// None of them touches identity, address, topology, or desired state. That is
// not an implementation detail -- it is the contract the receipt asserts, and
// the reason `open project` is a different command from the picker that can
// also create and replace Projects.
type projectLifecycleCommand struct {
	verb     projectLifecycleVerb
	store    *resourceStore
	switcher *switchCommand
	// lookupEnv answers the inside-tmux question `open project` needs and the
	// attached-client question `stop project` reports.
	lookupEnv func(string) string
}

func newProjectLifecycleCommand(verb projectLifecycleVerb, switcher *switchCommand) *projectLifecycleCommand {
	return &projectLifecycleCommand{
		verb:      verb,
		store:     newResourceStore(),
		switcher:  switcher,
		lookupEnv: os.Getenv,
	}
}

// projectLifecycleSpellings binds each verb to its canonical route spelling, so
// the flag set, the `-o` catalog lookup, and the receipt all name the same
// route rather than three independently assembled strings.
var projectLifecycleSpellings = map[projectLifecycleVerb]string{
	projectLifecycleStart: canonicalStartProject,
	projectLifecycleOpen:  canonicalOpenProject,
	projectLifecycleStop:  canonicalStopProject,
}

// spelling is the canonical route this invocation belongs to.
func (c *projectLifecycleCommand) spelling() string {
	return projectLifecycleSpellings[c.verb]
}

func (c *projectLifecycleCommand) Run(args []string, stdout, stderr io.Writer) error {
	verb := string(c.verb)
	if len(args) == 0 {
		return usageError(fmt.Sprintf("%s requires a resource kind: project", verb))
	}
	token, ok := cli.CanonicalChildToken(verb, args[0])
	if !ok || token != "project" {
		return usageError(fmt.Sprintf("%s %s is not available; this release implements: project", verb, args[0]))
	}
	return c.runProject(args[1:], stdout, stderr)
}

func (c *projectLifecycleCommand) runProject(args []string, stdout, stderr io.Writer) error {
	spelling := c.spelling()

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "Output projection")
	fs.StringVar(output, "output", "", "Output projection")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if len(refs) != 1 {
		return usageError(spelling + " requires exactly one Project reference")
	}
	mode, err := resolveLifecycleProjection(spelling, *output)
	if err != nil {
		return err
	}

	project, err := c.resolveProject(spelling, refs[0])
	if err != nil {
		return err
	}
	root := cleanOptionalPath(project.Spec.Root)
	if root == "" {
		return usageError(fmt.Sprintf("%s: project/%s has no spec.root to route a runtime through", spelling, project.Metadata.Name))
	}
	if c.switcher == nil {
		return fmt.Errorf("%s: the Project runtime executor is not configured", spelling)
	}
	sessionName, err := c.switcher.resolveTargetSession(root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(sessionName) == "" {
		return fmt.Errorf("%s: project/%s resolves to no persistent session name", spelling, project.Metadata.Name)
	}

	ctx := context.Background()
	live, route, err := c.sessionLive(ctx, sessionName)
	if err != nil {
		return err
	}

	receipt, err := c.execute(ctx, spelling, project, root, sessionName, live, route)
	if err != nil {
		return err
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	return writeOperationReceipt(stdout, mode, receipt)
}

// execute performs the verb and returns the receipt of what it actually did.
//
// Every refusal in here happens before the first runtime call, so a refused
// invocation is a zero-write outcome rather than a partial one.
func (c *projectLifecycleCommand) execute(
	ctx context.Context,
	spelling string,
	project coremetadata.Project,
	root, sessionName string,
	live bool,
	route *runtimeMutationRoute,
) (cli.OperationReceipt, error) {
	target := cli.ReceiptTarget{Kind: "Project", UID: project.Metadata.UID, Name: project.Metadata.Name}

	switch c.verb {
	case projectLifecycleStart:
		runtime := cli.RuntimeAlreadyLive
		if !live {
			if err := c.materialize(ctx, root, sessionName, true); err != nil {
				return cli.OperationReceipt{}, err
			}
			runtime = cli.RuntimeMaterialized
		}
		return c.receipt(cli.OperationStartProject, target, runtime, cli.FocusUnchanged), nil

	case projectLifecycleOpen:
		if !c.insideTmuxClient() {
			return cli.OperationReceipt{}, usageError(fmt.Sprintf(
				"%s moves the current tmux client; from outside tmux run `projmux attach project %s` instead",
				spelling, project.Metadata.Name))
		}
		runtime := cli.RuntimeAlreadyLive
		if live {
			if err := c.switcher.openProjectSession(ctx, sessionName); err != nil {
				return cli.OperationReceipt{}, err
			}
		} else {
			if err := c.materialize(ctx, root, sessionName, false); err != nil {
				return cli.OperationReceipt{}, err
			}
			runtime = cli.RuntimeMaterialized
		}
		return c.receipt(cli.OperationOpenProject, target, runtime, cli.FocusMovedCurrentClient), nil

	case projectLifecycleStop:
		if !live {
			return cli.OperationReceipt{}, usageError(fmt.Sprintf(
				"%s: project/%s has no live persistent session; nothing was changed",
				spelling, project.Metadata.Name))
		}
		focus := cli.FocusUnchanged
		if c.attachedToSession(sessionName) {
			focus = cli.FocusMovedCurrentClient
		}
		if err := c.switcher.stopManagedProjectSessionOnRoute(ctx, switchRegistryUIDPrefix+project.Metadata.UID, sessionName, "", "", route); err != nil {
			return cli.OperationReceipt{}, err
		}
		return c.receipt(cli.OperationStopProject, target, cli.RuntimeStopped, focus), nil
	}
	return cli.OperationReceipt{}, fmt.Errorf("%s: unknown Project lifecycle verb", spelling)
}

// sessionLive answers whether the Project's persistent session is live on the
// server this invocation would act on.
//
// Inside tmux that is the inherited server, read the way it always was. Outside
// tmux there is no inherited server, and asking tmux without a socket would ask
// the operator's default server -- not the app server a web request, a script,
// or an IDE terminal is driving. So `start` and `stop` resolve the same app
// route their writes use (`-L projmux`, proven app-owned) and read the exact
// session through its physical socket. That route is returned so `stop` can
// confirm and kill on it without resolving it a second time.
//
// An app server that is absent, or one the route cannot prove app-owned, is
// "not live": stop keeps its zero-write refusal and start keeps its
// materializing path, which owns its own route and its own refusals.
func (c *projectLifecycleCommand) sessionLive(ctx context.Context, sessionName string) (bool, *runtimeMutationRoute, error) {
	if c.verb == projectLifecycleOpen || c.switcher.inheritsTmuxServer() {
		live, err := c.switcher.switchSessionExists(ctx, sessionName)
		return live, nil, err
	}
	if c.switcher.tmuxRunner == nil {
		return false, nil, nil
	}
	route, err := resolveInvocationRuntimeMutationRoute(ctx, c.switcher.tmuxRunner, c.switcher.lookupEnv)
	if err != nil || route.authority == nil || strings.TrimSpace(route.expectedSocketPath) == "" {
		return false, nil, nil
	}
	exact := explicitTmuxRunner{runner: c.switcher.tmuxRunner, target: tmuxTransport{
		Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource,
	}}
	live, err := inttmux.NewClient(exact).SessionExists(ctx, sessionName)
	if err != nil {
		return false, nil, fmt.Errorf("check existing switch session %q: %w", sessionName, err)
	}
	if !live {
		return false, nil, nil
	}
	return true, &route, nil
}

// receipt renders the fixed lifecycle shape: the four desired-state axes are
// unchanged by construction, and the Project is the only counted resource.
func (c *projectLifecycleCommand) receipt(
	operation cli.Operation,
	target cli.ReceiptTarget,
	runtime cli.RuntimeEffect,
	focus cli.FocusEffect,
) cli.OperationReceipt {
	receipt := cli.NewReceipt(operation, target, cli.ReceiptEffects{
		Identity:     cli.IdentityUnchanged,
		Address:      cli.AddressUnchanged,
		Topology:     cli.TopologyUnchanged,
		DesiredState: cli.DesiredStateUnchanged,
		Runtime:      runtime,
		Focus:        focus,
	})
	action := cli.ReceiptAction(runtime)
	receipt.Add("Project", target.UID, target.Name, action)
	return receipt
}

// materialize brings the Project's current desired topology up through the one
// shared startup transaction. `continue` is the only mode used: these verbs
// never replace identity, which is what `Clear layout and open` owns.
func (c *projectLifecycleCommand) materialize(ctx context.Context, root, sessionName string, detached bool) error {
	return c.switcher.authorizeAndContinueProjectOpenRequest(ctx, projectOpenRequest{
		Target:      root,
		SessionName: sessionName,
		Mode:        projectStartupCandidate{Kind: projectStartupKindTopology},
		Detached:    detached,
	})
}

// resolveProject resolves the one Project this invocation addresses.
//
// An absolute path is accepted beside the `uid:`/name grammar because a Project
// root is the identity an operator types outside projmux -- it is what `cd`
// takes and what the shell prompt shows -- and refusing it here would make the
// lifecycle verbs the only Project routes that cannot be reached the way the
// sidebar reaches them.
func (c *projectLifecycleCommand) resolveProject(spelling, ref string) (coremetadata.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return coremetadata.Project{}, usageError(spelling + " requires a non-empty Project reference")
	}
	if c.store == nil || c.store.load == nil {
		return coremetadata.Project{}, fmt.Errorf("%s: the resource registry is not configured", spelling)
	}
	registry, err := c.store.load()
	if err != nil {
		return coremetadata.Project{}, MapMetadataError(err)
	}
	if filepath.IsAbs(ref) {
		project, ok := registry.ProjectByRoot(cleanOptionalPath(ref))
		if !ok {
			return coremetadata.Project{}, usageError(fmt.Sprintf(
				"%s: no Project claims root %q; register it with `projmux create project --root %s`", spelling, ref, ref))
		}
		return *project, nil
	}
	parsed, err := selector.ParseRef(coremetadata.KindProject, ref)
	if err != nil {
		return coremetadata.Project{}, err
	}
	query := selector.Query{Project: &parsed}
	resolution, err := selector.New(registry).ResolveProjects(query)
	if err != nil {
		return coremetadata.Project{}, err
	}
	target := selector.Target{Verb: selector.VerbFocus, Kind: coremetadata.KindProject}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return coremetadata.Project{}, err
	}
	project, ok := registry.Project(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Project{}, fmt.Errorf("%s: project %q disappeared during preflight", spelling, resolution.Matches[0].UID)
	}
	return *project, nil
}

// insideTmuxClient reports whether this invocation already has a tmux client.
func (c *projectLifecycleCommand) insideTmuxClient() bool {
	if c.lookupEnv == nil {
		return false
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

// attachedToSession reports whether this invocation's own client is inside the
// session the verb is about to end, which is the one case where a runtime stop
// also moves the operator.
func (c *projectLifecycleCommand) attachedToSession(sessionName string) bool {
	if !c.insideTmuxClient() || c.switcher == nil {
		return false
	}
	return strings.TrimSpace(c.switcher.originSession()) == strings.TrimSpace(sessionName)
}

// resolveLifecycleProjection maps the `-o` token onto the lifecycle catalog.
func resolveLifecycleProjection(spelling, token string) (cli.OutputMode, error) {
	if token == "" {
		return cli.OutputModeDefault, nil
	}
	mode, field, err := cli.ResolveOutputToken(spelling, token)
	if err != nil {
		return "", usageError(err.Error())
	}
	if field != "" {
		return "", usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}
	return mode, nil
}

// writeOperationReceipt renders one receipt through the requested projection.
func writeOperationReceipt(stdout io.Writer, mode cli.OutputMode, receipt cli.OperationReceipt) error {
	switch mode {
	case cli.OutputModeNone:
		return nil
	case cli.OutputModeReceipt:
		return receipt.WriteJSON(stdout)
	default:
		return receipt.WriteHuman(stdout)
	}
}
