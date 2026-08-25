package app

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

// The two canonical create spellings this Phase makes executable.
const (
	canonicalCreateWindow = "create window"
	canonicalCreatePane   = "create pane"
)

// placementDown is the vertical member of the closed placement enum.
const placementDown = "down"

// createResult is one created resource, carrying both its Projmux identity and
// the raw tmux pane handle the `pane-id` projection exposes.
type createResult struct {
	kind   coremetadata.Kind
	uid    string
	name   string
	paneID string
	// The owner context is the fan-out sort key: the contract orders scalar
	// output by (project name, window name, uid).
	projectName string
	windowName  string
	windowUID   string
}

// resourceCreateFlags is the parsed argv of a resource-backed create route.
type resourceCreateFlags struct {
	projects     repeatedFlag
	windows      repeatedFlag
	panes        repeatedFlag
	selectors    repeatedFlag
	labels       repeatedFlag
	name         string
	provider     string
	providerSet  bool
	cwd          string
	addDirs      repeatedFlag
	placement    string
	createWindow bool
	output       string
	payload      []string
	// resumeConversation is set by the Projmux split UI's resume selection and by
	// nothing else. It is deliberately not a parsed flag: no public spelling of
	// `create` accepts it, so an operator cannot reach a resume through the create
	// verb, which is `agent resume`'s job for a Registry Agent and this field's
	// job for a conversation the picker found on disk.
	resumeConversation string
	resumeSource       string
	codexCapability    *corecap.Selection
}

// resourceCreateShape selects which optional flag groups a resource-backed
// create route registers.
//
// The groups are per-route rather than per-flag because they travel together:
// a route that splits an existing Window needs the whole anchor surface, and a
// route that creates an Agent needs the provider. Keeping them grouped is what
// stops `create window` from silently accepting `--placement`.
type resourceCreateShape struct {
	// split registers the Window fan-out and split-anchor surface:
	// --window, --pane, --selector, --create-window, --placement.
	split bool
	// provider registers --provider. The provider shortcuts register it too,
	// so that respelling the provider they already name is rejected with the
	// shortcut's own message instead of a bare "flag not defined".
	provider bool
}

// createScope is the resolved Project/Window/anchor scope of one create.
//
// `--project` is a scope flag, never a mode selector: the same argv means the
// same thing with and without it, and the only difference is where the scope
// comes from. See resolveCreateScope for the rule.
type createScope struct {
	// project is the exact-one Project every created resource lands under.
	project selector.Ref
	// window and pane are filled in only when the whole scope was omitted and
	// the active tmux target supplied it. They are never blended with an
	// explicit --window/--pane/--selector occurrence.
	window *selector.Ref
	pane   *selector.Ref
}

// projectQuery builds the exact-one Project scope query.
func (s createScope) projectQuery() selector.Query {
	ref := s.project
	return selector.Query{Project: &ref}
}

// uidRef spells a resolved uid as the selector ref form, so downstream error
// text reports the identity that was actually selected rather than a name that
// was never typed.
func uidRef(kind coremetadata.Kind, uid string) selector.Ref {
	return selector.Ref{Kind: kind, UID: uid, Raw: selector.UIDPrefix + uid}
}

// resolveCreateScope fixes the scope of one create before its transaction opens.
//
// The rule has exactly two branches and no third product model:
//
//   - An explicit `--project` wins everywhere, inside tmux and outside it. The
//     ref is parsed here and resolved inside the transaction, after
//     reconciliation, because a Project that exists only as a discovered workdir
//     is registered by that reconciliation and not before it.
//   - With no `--project`, the Project is derived from the active exact tmux
//     runtime through the same registry identity mirror every other
//     implicit-target route reads. Outside tmux, and on any runtime that carries
//     no managed identity -- Home, a control session, an unattributed or foreign
//     pane -- the derivation refuses. It never falls back to a runtime-only
//     split, never invents a Project from `$HOME`, a session name, or a cwd, and
//     never probes a server the invocation did not inherit.
//
// The Window and the anchor Pane follow the *whole* scope, not the Project flag:
// they are derived only when no --window, --pane, or --selector was given at
// all. That is the same "an omitted selector is the whole selector" rule the
// read and rename verbs use, and it is what keeps a bare `create pane
// --placement right` -- the generated keybinding body -- a split of the Window
// the operator is looking at instead of a fan-out across every Window of the
// Project. One explicit scope occurrence turns the whole scope explicit, so an
// operator who names a Window never gets a silent anchor from somewhere else.
func (c *createCommand) resolveCreateScope(spelling string, flags resourceCreateFlags, shape resourceCreateShape) (createScope, error) {
	if len(flags.projects) == 1 {
		ref, err := selector.ParseRef(coremetadata.KindProject, flags.projects[0])
		if err != nil {
			return createScope{}, MapMetadataError(err)
		}
		return createScope{project: ref}, nil
	}
	if c == nil || c.store == nil || c.store.load == nil {
		return createScope{}, errors.New("create: the resource-backed create routes are not configured")
	}
	observer, inside := c.observeActiveTarget()
	if !inside {
		return createScope{}, requireExplicitProject(spelling, "this invocation is not inside a tmux client")
	}
	// Read-only. The derivation happens before the transaction so a refusal
	// costs zero registry writes and zero tmux mutations.
	registry, err := c.store.load()
	if err != nil {
		return createScope{}, MapMetadataError(err)
	}
	projectUID, detail := observer.uidFor(coremetadata.KindProject, registry)
	if projectUID == "" {
		return createScope{}, requireExplicitProject(spelling, detail)
	}
	scope := createScope{project: uidRef(coremetadata.KindProject, projectUID)}
	if !shape.split || flags.explicitWindowScope() {
		return scope, nil
	}
	windowUID, detail := observer.uidFor(coremetadata.KindWindow, registry)
	if windowUID == "" {
		return createScope{}, requireExplicitProject(spelling, detail)
	}
	window := uidRef(coremetadata.KindWindow, windowUID)
	scope.window = &window
	// The active Pane is the anchor when it carries a managed identity. When it
	// does not -- an unmirrored pane inside a managed Window -- the Window's
	// stored role-agnostic anchor is the structural point an explicit --window
	// resolves.
	if paneUID, _ := observer.uidFor(coremetadata.KindPane, registry); paneUID != "" {
		pane := uidRef(coremetadata.KindPane, paneUID)
		scope.pane = &pane
	}
	return scope, nil
}

// observeActiveTarget is the nil-safe read of the invocation's tmux target.
func (c *createCommand) observeActiveTarget() (activeTargetObserver, bool) {
	if c == nil || c.activeTarget == nil {
		return activeTargetObserver{}, false
	}
	return c.activeTarget()
}

// requireExplicitProject is the refusal of an omitted scope that resolved no
// managed Project.
//
// It names `--project` as the fix rather than reporting a cardinality failure:
// nothing was ambiguous, the runtime the operator is sitting in simply is not a
// managed Projmux resource. Being a usage error, it exits 2 with zero bytes on
// stdout, and it is raised before the registry transaction opens, so the refusal
// is measurably zero Registry writes and zero tmux mutations.
func requireExplicitProject(spelling, detail string) error {
	return usageError(fmt.Sprintf(
		"%s requires a Project: no --project <ref> was given and %s; nothing was created, so pass --project <ref>",
		spelling, detail))
}

// argumentTerminator ends option scanning; everything after it is payload that
// projmux forwards untouched.
const argumentTerminator = "--"

// splitPayload separates the argv head from the `--` payload.
func splitPayload(spelling string, args []string) ([]string, []string, error) {
	for i, arg := range args {
		if arg != argumentTerminator {
			continue
		}
		payload := append([]string(nil), args[i+1:]...)
		if len(payload) == 0 {
			return nil, nil, usageError(spelling + " -- requires a payload")
		}
		return args[:i], payload, nil
	}
	return args, nil, nil
}

// parseResourceCreateFlags parses one resource-backed create argv.
func parseResourceCreateFlags(spelling string, args []string, stderr io.Writer, shape resourceCreateShape) (resourceCreateFlags, error) {
	head, payload, err := splitPayload(spelling, args)
	if err != nil {
		return resourceCreateFlags{}, err
	}
	out := resourceCreateFlags{payload: payload, placement: defaultPlacement}
	pane := shape.split

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&out.projects, "project", "at-most-one Project scope: <name> or uid:<uid>; defaults to the active tmux runtime's managed Project")
	fs.Var(&out.projects, "p", "at-most-one Project scope: <name> or uid:<uid> (alias of --project)")
	if shape.provider {
		fs.StringVar(&out.provider, "provider", "", "Agent provider: "+strings.Join(cli.AgentProviders(), "|"))
		fs.StringVar(&out.cwd, "cwd", "", "effective Agent working directory (defaults to Project root)")
		fs.Var(&out.addDirs, "add-dir", "repeatable additional writable root")
	}
	if pane {
		fs.Var(&out.windows, "window", "repeatable Window selector: <name> or uid:<uid>")
		fs.Var(&out.windows, "w", "repeatable Window selector: <name> or uid:<uid> (alias of --window)")
		fs.Var(&out.panes, "pane", "repeatable anchor Pane selector: <name> or uid:<uid>")
		fs.Var(&out.selectors, "selector", "repeatable Window label filter: key=value (AND)")
		fs.BoolVar(&out.createWindow, "create-window", false, "create the exact-name --window Windows that do not exist yet")
		fs.StringVar(&out.placement, "placement", defaultPlacement, "split placement: "+strings.Join(placementDirections, "|"))
	}
	fs.StringVar(&out.name, "name", "", "explicit Projmux metadata.name for the created resource")
	fs.Var(&out.labels, "label", "repeatable creation label: key=value")
	fs.StringVar(&out.output, "output", "", "result projection")
	fs.StringVar(&out.output, "o", "", "result projection (alias of --output)")

	if err := fs.Parse(head); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return resourceCreateFlags{}, err
		}
		return resourceCreateFlags{}, usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return resourceCreateFlags{}, usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "provider" {
			out.providerSet = true
		}
	})
	if len(out.projects) > 1 {
		return resourceCreateFlags{}, usageError(spelling +
			" accepts at most one --project <ref>; an omitted --project takes the active tmux runtime's managed Project")
	}
	if pane && !slices.Contains(placementDirections, out.placement) {
		return resourceCreateFlags{}, usageError(fmt.Sprintf("%s --placement must be one of: %s",
			spelling, strings.Join(placementDirections, ", ")))
	}
	if out.createWindow {
		if len(out.selectors) > 0 {
			return resourceCreateFlags{}, usageError(spelling +
				" --create-window needs an exact --window <name> to create and cannot be combined with --selector")
		}
		if !slices.ContainsFunc(out.windows, isExactNameRef) {
			return resourceCreateFlags{}, usageError(spelling +
				" --create-window requires at least one exact-name --window <name>; a uid: reference never names a Window to create")
		}
	}
	return out, nil
}

// isExactNameRef reports whether a --window occurrence names a Window that
// --create-window may create. A `uid:` occurrence never does: an unresolvable
// uid names nothing, so there is no name to allocate.
func isExactNameRef(raw string) bool {
	return strings.TrimSpace(raw) != "" && !strings.HasPrefix(raw, selector.UIDPrefix)
}

// labelMap parses the repeatable `--label key=value` creation option.
func labelMap(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, value := range raw {
		label, err := selector.ParseLabel(value)
		if err != nil {
			return nil, err
		}
		out[label.Key] = label.Value
	}
	return out, nil
}

// payloadCommand renders the `--` payload as the one-time name-derivation
// source stored on the created Pane.
func payloadCommand(payload []string) string {
	return strings.TrimSpace(strings.Join(payload, " "))
}

// explicitWindowScope reports whether the argv named the Window scope itself.
//
// Any one of the three occurrences makes the whole scope explicit, which is what
// stops an implicit anchor from being blended into a fan-out the operator
// addressed on purpose.
func (f resourceCreateFlags) explicitWindowScope() bool {
	return len(f.windows) > 0 || len(f.panes) > 0 || len(f.selectors) > 0
}

// runResourceWindow answers the canonical `create window`.
func (c *createCommand) runResourceWindow(args []string, stdout, stderr io.Writer) error {
	const spelling = canonicalCreateWindow

	shape := resourceCreateShape{}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	mode, err := c.resolveProjection(spelling, flags.output)
	if err != nil {
		return err
	}
	labels, err := labelMap(flags.labels)
	if err != nil {
		return MapMetadataError(err)
	}
	// Scope resolution runs last of the preflight, so every argv-only refusal --
	// an unknown projection, a malformed label, a closed-enum violation -- is
	// reported before an environment-dependent one. What the operator typed is
	// wrong independently of where they typed it.
	scope, err := c.resolveCreateScope(spelling, flags, shape)
	if err != nil {
		return err
	}

	var results []createResult
	if err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		project, err := c.resolveProject(*working, scope)
		if err != nil {
			return err
		}
		if err := c.refuseMissingRoot(project); err != nil {
			return err
		}

		// Metadata first. An explicit --name collision, or any other allocation
		// failure, therefore lands before a single tmux object exists.
		work, err := c.allocateWindow(working, mutator, project, windowRequest{
			name:        flags.name,
			labels:      labels,
			payload:     flags.payload,
			operationID: operationID,
		})
		if err != nil {
			return err
		}

		sessionName, err := c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
		if err != nil {
			return err
		}
		if err := c.materializeWindow(ctx, working, mutator, ledger, project, sessionName, &work); err != nil {
			return err
		}
		results = append(results, createResult{
			kind:        coremetadata.KindWindow,
			uid:         work.window.Metadata.UID,
			name:        work.window.Metadata.Name,
			paneID:      work.initialPaneID,
			projectName: project.Metadata.Name,
			windowName:  work.window.Metadata.Name,
			windowUID:   work.window.Metadata.UID,
		})
		return nil
	}, c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	return c.writeResults(stdout, spelling, mode, coremetadata.KindWindow, results)
}

// runResourcePane answers the resource-backed `create pane`.
func (c *createCommand) runResourcePane(args []string, stdout, stderr io.Writer) error {
	const spelling = canonicalCreatePane

	shape := resourceCreateShape{split: true}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	mode, err := c.resolveProjection(spelling, flags.output)
	if err != nil {
		return err
	}
	labels, err := labelMap(flags.labels)
	if err != nil {
		return MapMetadataError(err)
	}
	// Scope resolution runs last of the preflight, so every argv-only refusal --
	// an unknown projection, a malformed label, a closed-enum violation -- is
	// reported before an environment-dependent one. What the operator typed is
	// wrong independently of where they typed it.
	scope, err := c.resolveCreateScope(spelling, flags, shape)
	if err != nil {
		return err
	}

	var results []createResult
	if err := c.transact(func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
		project, err := c.resolveProject(*working, scope)
		if err != nil {
			return err
		}
		if err := c.refuseMissingRoot(project); err != nil {
			return err
		}

		// Full preflight plus the metadata half of the Window ensure. Every
		// target Window and every anchor Pane is fixed, and every Window this
		// operation must create is allocated, before the first tmux call.
		plan, windows, err := c.resolveSplitTargets(working, mutator, project, scope, flags,
			selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindWindow}, spelling, operationID)
		if err != nil {
			return err
		}
		if len(plan.targets) == 0 {
			return usageError(fmt.Sprintf("%s resolved no target Window; %s matched at least one Window is required",
				spelling, selector.DescribeSelector(plan.query)))
		}

		panes := make([]paneWork, 0, len(plan.targets))
		for _, target := range plan.targets {
			window, ok := working.Window(target.windowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, target.windowUID)
			}
			pane, err := mutator.AddPane(working, target.windowUID, coremetadata.BootstrapPane{
				Name:    flags.name,
				Command: payloadCommand(flags.payload),
				CWD:     project.Spec.Root,
				Labels:  labels,
			}, c.shell, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, "", operationID)
			if err != nil {
				return err
			}
			panes = append(panes, paneWork{
				target:     target,
				windowName: window.Metadata.Name,
				pane:       pane,
				activation: activation,
			})
		}

		// Runtime phase.
		sessionName, err := c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
		if err != nil {
			return err
		}
		for i := range windows {
			if err := c.materializeWindow(ctx, working, mutator, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range panes {
			anchorPaneID, err := c.ensureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, work.target)
			if err != nil {
				return err
			}
			launch := c.runtime.supervisedLaunch(ctx, work.activation, flags.payload)
			paneID, err := c.runtime.splitPane(ctx, anchorPaneID, flags.placement, project.Spec.Root, launch)
			if paneID != "" {
				if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, work.pane.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(err, claimErr)
				}
				if mirrorErr := c.runtime.mirrorPane(ctx, paneID, work.pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
				observeActivationRuntime(working, mutator, work.activation, paneID, c.runtime.warn)
			}
			if err != nil {
				return err
			}
			// A shell-required offline bootstrap must first materialize the exact
			// stored anchor. Only after this new shell Pane exists do we adopt it
			// as an empty optional default.
			if _, _, err := mutator.AdoptWindowDefaultShell(working, work.target.windowUID, work.pane.Metadata.UID); err != nil {
				return MapMetadataError(err)
			}
			// Equalize before releasing the create transaction: the next serialized
			// create may split the same exact anchor Pane and needs the capacity restored
			// first. equalizeSplitLayout keeps this lock-held work bounded by sharing
			// one route/inventory guard and one effect receipt across the complete
			// printable resize batch.
			c.runtime.equalizeSplitLayout(ctx, anchorPaneID, flags.placement)
			results = append(results, createResult{
				kind:        coremetadata.KindPane,
				uid:         work.pane.Metadata.UID,
				name:        work.pane.Metadata.Name,
				paneID:      paneID,
				projectName: project.Metadata.Name,
				windowName:  work.windowName,
				windowUID:   work.target.windowUID,
			})
		}
		return nil
	}, c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	return c.writeResults(stdout, spelling, mode, coremetadata.KindPane, results)
}

// paneWork is one allocated Pane waiting for its runtime split.
type paneWork struct {
	target     paneTarget
	windowName string
	pane       coremetadata.Pane
	// activation is the generation this Pane's supervised launch was issued.
	activation superviseSpec
}

// paneTarget is one preflighted (Window, anchor Pane) pair.
type paneTarget struct {
	windowUID    string
	anchorUID    string
	storedAnchor bool
}

// panePlan is the preflight result of a `create pane` fan-out.
type panePlan struct {
	query              selector.Query
	targets            []paneTarget
	missingWindowNames []string
}

// resolveSplitTargets is the shared preflight of the two routes that split an
// existing Window: `create pane --project` and `create agent --project`.
//
// It fixes every target Window and every anchor Pane, then promotes the missing
// exact-name --window occurrences into real Window allocations when
// --create-window asked for it. Both halves finish before the caller performs a
// single tmux call, so a stale stored role-agnostic anchor, an unsatisfiable cardinality, or
// an explicit --name collision leaves zero metadata writes and zero tmux objects
// behind.
//
// The returned targets are ordered by Window uid so a fan-out is deterministic
// regardless of selector spelling; the human-facing output order is applied
// separately by writeResults, which sorts by (project name, window name, uid).
func (c *createCommand) resolveSplitTargets(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	scope createScope,
	flags resourceCreateFlags,
	fanOut selector.Target,
	spelling, operationID string,
) (panePlan, []windowWork, error) {
	plan, err := c.planPaneTargets(*working, project, scope, flags, fanOut, spelling)
	if err != nil {
		return panePlan{}, nil, err
	}
	var windows []windowWork
	for _, missing := range plan.missingWindowNames {
		work, err := c.allocateWindow(working, mutator, project, windowRequest{
			name:        missing,
			operationID: operationID,
		})
		if err != nil {
			return panePlan{}, nil, err
		}
		windows = append(windows, work)
		// The Window this operation created owns a brand new initial Pane,
		// which is both its default shell ref and this operation's anchor.
		plan.targets = append(plan.targets, paneTarget{
			windowUID:    work.window.Metadata.UID,
			anchorUID:    work.initial.Metadata.UID,
			storedAnchor: true,
		})
	}
	slices.SortStableFunc(plan.targets, func(a, b paneTarget) int {
		return cmp.Compare(a.windowUID, b.windowUID)
	})
	return plan, windows, nil
}

// planPaneTargets resolves every target Window and its anchor Pane without
// mutating anything.
func (c *createCommand) planPaneTargets(
	registry coremetadata.Registry,
	project coremetadata.Project,
	scope createScope,
	flags resourceCreateFlags,
	fanOut selector.Target,
	spelling string,
) (panePlan, error) {
	query := scope.projectQuery()
	for _, raw := range flags.windows {
		ref, err := selector.ParseRef(coremetadata.KindWindow, raw)
		if err != nil {
			return panePlan{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.selectors {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return panePlan{}, err
		}
		query.Labels = append(query.Labels, label)
	}
	if scope.window != nil && len(query.Windows) == 0 && len(query.Labels) == 0 {
		query.Windows = append(query.Windows, *scope.window)
	}

	plan := panePlan{query: query}
	resolver := selector.New(registry)
	resolution, err := resolver.ResolveWindows(query)
	if err != nil {
		return panePlan{}, err
	}

	if flags.createWindow {
		// A missing exact name is promoted to a Window ensure. Everything else
		// keeps the default no-match behavior.
		plan.missingWindowNames = unresolvedWindowNames(registry, project, flags.windows)
	}
	if len(resolution.Matches) == 0 && len(plan.missingWindowNames) == 0 {
		// The fan-out cell belongs to the caller, not to this helper: `create
		// pane` fans out over the Windows it selected, and `create agent` over
		// the Agents it produces, one per selected Window.
		return panePlan{}, selector.Enforce(fanOut, selector.DescribeSelector(query), resolution)
	}

	for _, match := range resolution.Matches {
		anchorUID, storedAnchor, err := c.resolveAnchor(registry, project, scope, match.UID, flags, spelling)
		if err != nil {
			return panePlan{}, err
		}
		plan.targets = append(plan.targets, paneTarget{windowUID: match.UID, anchorUID: anchorUID, storedAnchor: storedAnchor})
	}
	return plan, nil
}

// unresolvedWindowNames returns the exact-name --window occurrences that match
// no Window in the Project, deduplicated and in argv order.
func unresolvedWindowNames(registry coremetadata.Registry, project coremetadata.Project, refs []string) []string {
	existing := map[string]bool{}
	for _, window := range registry.WindowsOf(project.Metadata.UID) {
		existing[window.Metadata.Name] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, raw := range refs {
		if !isExactNameRef(raw) || existing[raw] || seen[raw] {
			continue
		}
		seen[raw] = true
		missing = append(missing, raw)
	}
	return missing
}

// resolveAnchor fixes the split anchor of one target Window.
//
// An explicit --pane is resolved inside that Window's own owner scope and must
// be exactly one. With no --pane the persisted role-agnostic anchorPaneRef is
// the anchor: an explicitly scoped create has deliberately no fallback to the
// focused, last-used, or another live Pane, and a missing or stale ref is a
// usage error rather than a silent repair.
//
// The one implicit anchor is the whole-scope one, and it is not a fallback. When
// the argv named no scope at all, resolveCreateScope resolved the active Window
// *and* the active Pane together, so the anchor is a resolved target of the same
// invocation rather than a substitution for one. As soon as any --project,
// --window, --pane, or --selector is typed, that pair is gone: --project and
// --window fixed the target set, and substituting the focused pane would
// silently split somewhere the invocation never addressed, possibly in a
// different Window entirely.
func (c *createCommand) resolveAnchor(registry coremetadata.Registry, project coremetadata.Project, scope createScope, windowUID string, flags resourceCreateFlags, spelling string) (string, bool, error) {
	window, ok := registry.Window(windowUID)
	if !ok {
		return "", false, fmt.Errorf("%s: window %q disappeared during preflight", spelling, windowUID)
	}
	panes := flags.panes
	if len(panes) == 0 && scope.pane != nil {
		panes = repeatedFlag{scope.pane.Raw}
	}
	if len(panes) > 0 {
		query := scope.projectQuery()
		query.Windows = []selector.Ref{{Kind: coremetadata.KindWindow, UID: windowUID, Raw: window.Metadata.Name}}
		for _, raw := range panes {
			ref, err := selector.ParseRef(coremetadata.KindPane, raw)
			if err != nil {
				return "", false, err
			}
			query.Panes = append(query.Panes, ref)
		}
		resolution, err := selector.New(registry).ResolvePanes(query)
		if err != nil {
			return "", false, err
		}
		target := selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindPane}
		if err := selector.Enforce(target, selector.DescribeSelector(query)+" in window/"+window.Metadata.Name, resolution); err != nil {
			return "", false, err
		}
		paneUID := resolution.Matches[0].UID
		if _, ok := registry.PaneInWindow(window.Metadata.UID, paneUID); !ok {
			return "", false, usageError(fmt.Sprintf(
				"%s: explicit pane %q is not owned by window/%s; refusing cross-Window anchor",
				spelling, paneUID, window.Metadata.Name))
		}
		return paneUID, false, nil
	}

	anchor := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if anchor == "" {
		return "", true, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef; pass an explicit --pane <ref>",
			spelling, window.Metadata.Name, project.Metadata.Name))
	}
	if resolved, ok := registry.WindowAnchor(window.Metadata.UID); !ok || resolved.Metadata.UID != anchor {
		return "", true, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) anchorPaneRef %q is dangling or cross-Window; pass an explicit --pane <ref>",
			spelling, window.Metadata.Name, project.Metadata.Name, anchor))
	}
	return anchor, true, nil
}

// resolveProject resolves the exact-one Project scope through the declared
// <create, Project> cardinality cell.
//
// The cell is the same whether the ref was typed or derived: a create always
// lands under exactly one Project.
func (c *createCommand) resolveProject(registry coremetadata.Registry, scope createScope) (coremetadata.Project, error) {
	query := scope.projectQuery()
	resolution, err := selector.New(registry).ResolveProjects(query)
	if err != nil {
		return coremetadata.Project{}, err
	}
	target := selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindProject}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return coremetadata.Project{}, err
	}
	project, ok := registry.Project(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Project{}, fmt.Errorf("create: project %q disappeared during preflight", resolution.Matches[0].UID)
	}
	return *project, nil
}

// refuseMissingRoot rejects a Project whose spec.root has disappeared.
//
// A MissingRoot Project is preserved rather than deleted, so it still resolves
// as a selector target; creating resources below a directory that is not there
// would materialize a runtime anchored at a path tmux cannot enter.
func (c *createCommand) refuseMissingRoot(project coremetadata.Project) error {
	condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot)
	if !ok || condition.Status != coremetadata.ConditionTrue {
		return nil
	}
	return usageError(fmt.Sprintf(
		"create: project/%s carries a MissingRoot condition for %q; rebind it before creating resources",
		project.Metadata.Name, project.Spec.Root))
}

// windowRequest describes one Window creation.
type windowRequest struct {
	name        string
	labels      map[string]string
	payload     []string
	operationID string
}

// windowWork is one allocated Window plus the runtime handles it acquires.
type windowWork struct {
	window  coremetadata.Window
	initial coremetadata.Pane
	payload []string
	// activation is the generation the initial Pane's supervised launch was
	// issued.
	activation superviseSpec
	// initialPaneID is the tmux pane id of the Window's initial Pane, filled in
	// by the runtime phase.
	initialPaneID string
}

// allocateWindow creates one Window and its initial Pane in metadata only.
func (c *createCommand) allocateWindow(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	req windowRequest,
) (windowWork, error) {
	command := payloadCommand(req.payload)
	window, panes, err := mutator.AddWindow(working, project.Metadata.UID, coremetadata.BootstrapWindow{
		Name:    req.name,
		Command: command,
		Labels:  req.labels,
		Panes: []coremetadata.BootstrapPane{{
			Command: command,
			CWD:     project.Spec.Root,
			Labels:  req.labels,
		}},
	}, c.shell, req.operationID)
	if err != nil {
		return windowWork{}, MapMetadataError(err)
	}
	activation, err := c.issuePaneActivation(working, mutator, panes[0].Metadata.UID, "", req.operationID)
	if err != nil {
		return windowWork{}, err
	}
	return windowWork{window: window, initial: panes[0], payload: req.payload, activation: activation}, nil
}

// materializeWindow creates the detached tmux window for an allocated Window and
// mirrors both identities onto it.
func (c *createCommand) materializeWindow(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *runtimeLedger,
	project coremetadata.Project,
	sessionName string,
	work *windowWork,
) error {
	launch := c.runtime.supervisedLaunch(ctx, work.activation, work.payload)
	created, err := c.runtime.newWindow(ctx, sessionName, work.window.Metadata.Name, project.Spec.Root, launch)
	if created.WindowID == "" {
		return err
	}
	if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, work.window.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	// The exact attributed Window is immediately renamed by MirrorWindow to
	// the stable allocated name. Persist that same duplicate-allowed display
	// value before the mirror so a later lifecycle reconcile has no Registry
	// drift to discover. This is intentionally after the exact @N UID claim:
	// an attribution or claim failure leaves the private transaction unchanged.
	projected, projectErr := mutator.ObserveWindowDisplayName(working, work.window.Metadata.UID, work.window.Metadata.Name)
	if projectErr != nil {
		return errors.Join(err, projectErr)
	}
	work.window = projected
	if mirrorErr := c.runtime.mirrorWindow(ctx, created.WindowID, work.window); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	if strings.TrimSpace(sessionName) != "" || strings.TrimSpace(created.WindowID) != "" {
		projected, bindingErr := mutator.ObserveWindowRuntimeBinding(
			working, work.window.Metadata.UID, sessionName, created.WindowID,
		)
		if bindingErr != nil {
			return errors.Join(err, bindingErr)
		}
		work.window = projected
	}
	if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, work.initial.Metadata.UID, ledger); claimErr != nil {
		return errors.Join(err, claimErr)
	}
	work.initialPaneID = created.PaneID
	if mirrorErr := c.runtime.mirrorPane(ctx, work.initialPaneID, work.initial); mirrorErr != nil {
		return errors.Join(err, mirrorErr)
	}
	observeActivationRuntime(working, mutator, work.activation, work.initialPaneID, c.runtime.warn)
	return err
}

// ensureAnchorPane returns the live tmux pane id of the preflighted anchor,
// materializing the Window it belongs to when that Window is still offline.
func (c *createCommand) ensureAnchorPane(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *runtimeLedger,
	project coremetadata.Project,
	sessionName, operationID string,
	target paneTarget,
) (string, error) {
	registry := *working
	window, ok := registry.Window(target.windowUID)
	if !ok {
		return "", fmt.Errorf("create pane: window %q disappeared before the mutation ran", target.windowUID)
	}
	anchor, ok := registry.Pane(target.anchorUID)
	if !ok {
		return "", fmt.Errorf("create pane: anchor pane %q disappeared before the mutation ran", target.anchorUID)
	}
	if _, ok := registry.PaneInWindow(window.Metadata.UID, anchor.Metadata.UID); !ok {
		return "", fmt.Errorf("create pane: anchor pane/%s is not owned by window/%s", anchor.Metadata.Name, window.Metadata.Name)
	}
	if target.storedAnchor {
		stored, ok := registry.WindowAnchor(window.Metadata.UID)
		if !ok || stored.Metadata.UID != anchor.Metadata.UID {
			return "", fmt.Errorf("create pane: window/%s stored anchor %q is dangling or cross-Window", window.Metadata.Name, target.anchorUID)
		}
	}

	windowID, err := c.runtime.windowIDForUID(ctx, sessionName, target.windowUID)
	if err != nil {
		return "", err
	}
	if windowID == "" {
		// A whole offline Window has no Pane against which tmux can split. A stored
		// direct shell anchor can be its initial Pane. A stored Agent anchor instead
		// gets an explicit lazy default-shell bootstrap; the anchor identity itself
		// remains unchanged and is materialized only by its Agent launch path.
		bootstrap := *anchor
		if anchor.Spec.Role != coremetadata.PaneRoleShell || anchor.Metadata.OwnerRef == nil ||
			anchor.Metadata.OwnerRef.Kind != coremetadata.KindWindow || anchor.Metadata.OwnerRef.UID != window.Metadata.UID {
			if !target.storedAnchor {
				return "", fmt.Errorf("create pane: explicit anchor pane/%s is offline; refusing to infer a different Pane", anchor.Metadata.Name)
			}
			bootstrap, _, err = mutator.EnsureWindowDefaultShell(working, window.Metadata.UID, c.shell, operationID)
			if err != nil {
				return "", MapMetadataError(err)
			}
			window, _ = working.Window(window.Metadata.UID)
		}
		bootstrapActivation, activationErr := c.issuePaneActivation(working, mutator, bootstrap.Metadata.UID, "", operationID)
		if activationErr != nil {
			return "", activationErr
		}
		created, createErr := c.runtime.newWindow(ctx, sessionName, window.Metadata.Name, project.Spec.Root,
			c.runtime.supervisedLaunch(ctx, bootstrapActivation, nil))
		windowID, err = created.WindowID, createErr
		if windowID != "" {
			if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, windowID, window.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.mirrorWindow(ctx, windowID, *window); mirrorErr != nil {
				return "", errors.Join(err, mirrorErr)
			}
			if strings.TrimSpace(sessionName) != "" || strings.TrimSpace(windowID) != "" {
				if _, bindingErr := mutator.ObserveWindowRuntimeBinding(
					working, window.Metadata.UID, sessionName, windowID,
				); bindingErr != nil {
					return "", errors.Join(err, bindingErr)
				}
			}
			if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, bootstrap.Metadata.UID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
			if mirrorErr := c.runtime.mirrorPane(ctx, created.PaneID, bootstrap); mirrorErr != nil {
				return "", errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, bootstrapActivation, created.PaneID, c.runtime.warn)
			return created.PaneID, err
		}
		if err != nil {
			return "", err
		}
	}

	panes, err := c.runtime.panesOf(ctx, windowID)
	if err != nil {
		return "", err
	}
	for _, row := range panes {
		if row[0] == target.anchorUID {
			return row[1], nil
		}
	}
	return "", fmt.Errorf(
		"create pane: anchor pane/%s (window/%s) has no live tmux binding in window %s",
		anchor.Metadata.Name, window.Metadata.Name, windowID)
}

// ensureProjectRuntime makes the Project's persistent tmux session live and
// records the projection.
//
// This is the acceptance-criterion-1 path: a Project whose status.session is
// absent or not live is materialized in the background, detached, with no
// client movement at all.
func (c *createCommand) ensureProjectRuntime(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	operationID string,
	ledger *runtimeLedger,
) (string, error) {
	if err := c.ensureRuntimeRoute(ctx); err != nil {
		return "", err
	}
	sessionName := c.reconciler.projectPhysicalSessionName(*working, project)
	if sessionName == "" {
		return "", fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
	}
	exists, err := c.runtime.sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return "", fmt.Errorf("create: inspect Project runtime %q: %w", sessionName, err)
	}
	if !exists {
		windows := working.WindowsOf(project.Metadata.UID)
		if len(windows) > 0 {
			if _, ok := working.WindowDefaultShell(windows[0].Metadata.UID); !ok {
				if _, _, err := mutator.EnsureWindowDefaultShell(working, windows[0].Metadata.UID, c.shell, operationID); err != nil {
					return "", MapMetadataError(err)
				}
			}
		}
	}
	created, err := c.runtime.ensureSession(ctx, project, sessionName, ledger)
	if err != nil {
		return "", err
	}
	if created.Created {
		// `new-session` always brings one window and one pane with it. Binding
		// them to the Project's first bootstrap Window is what keeps the runtime
		// a projection of the stored topology instead of an orphan window
		// sitting next to it.
		if err := c.adoptInitialWindow(ctx, working, mutator, project, created, ledger); err != nil {
			return "", err
		}
	}
	if _, err := mutator.BindProjectSession(working, project.Metadata.UID, sessionName, true); err != nil {
		return "", MapMetadataError(err)
	}
	if err := c.runtime.finalizeSessionStartup(ctx, created, sessionName, project.Spec.Root, ledger); err != nil {
		return "", err
	}
	return created.SessionID, nil
}

// adoptInitialWindow binds the window and pane a freshly created session came
// with to the Project's first stored Window and that Window's default shell.
//
// Adoption is deliberately narrow: it only ever runs for a session this
// operation just created, so it can never claim a window that belonged to
// someone else. The exact initial Window and Pane are claimed and recorded as
// separate ledger entries before their full mirrors, so rollback can verify and
// remove each owned handle even when a hook moved it out of the new Session.
func (c *createCommand) adoptInitialWindow(ctx context.Context, registry *coremetadata.Registry, mutator coremetadata.Mutator, project coremetadata.Project, created intmux.NewSessionResult, ledger *runtimeLedger) error {
	windows := registry.WindowsOf(project.Metadata.UID)
	if len(windows) == 0 {
		return nil
	}
	first := windows[0]
	if err := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, first.Metadata.UID, ledger); err != nil {
		return err
	}
	projected, err := mutator.ObserveWindowDisplayName(registry, first.Metadata.UID, first.Metadata.Name)
	if err != nil {
		return err
	}
	first = projected
	if err := c.runtime.mirrorWindow(ctx, created.WindowID, first); err != nil {
		return err
	}
	if strings.TrimSpace(created.SessionID) != "" || strings.TrimSpace(created.WindowID) != "" {
		projected, err := mutator.ObserveWindowRuntimeBinding(
			registry, first.Metadata.UID, created.SessionID, created.WindowID,
		)
		if err != nil {
			return err
		}
		first = projected
	}
	primary, ok := registry.WindowDefaultShell(first.Metadata.UID)
	if !ok {
		return nil
	}
	if err := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, primary.Metadata.UID, ledger); err != nil {
		return err
	}
	return c.runtime.mirrorPane(ctx, created.PaneID, *primary)
}

// createOperation is one create body, run inside the registry transaction.
type createOperation func(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
	ledger *runtimeLedger,
) error

type createPreReconcile func(
	ctx context.Context,
	working coremetadata.Registry,
	mutator coremetadata.Mutator,
	operationID string,
) (liveSessionIdentity, error)

// projectOwnershipGuard contains the selected session before lifecycle
// reconciliation can import, adopt, or re-mirror anything in it. Existing
// Projects require the complete UID/root/duplicate proof. A Project visible
// only through discovery has no durable UID proof yet; if its would-be session
// name is already live, create refuses rather than importing that blank or
// foreign runtime under a freshly allocated identity.
func (c *createCommand) projectOwnershipGuard(scope createScope) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (liveSessionIdentity, error) {
		project, err := c.resolveProject(working, scope)
		if err == nil {
			sessionName := c.reconciler.projectPhysicalSessionName(working, project)
			if sessionName == "" {
				return liveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
			}
			identity, _, err := c.runtime.preflightSessionOwnership(ctx, project, sessionName)
			if identity.Name == "" {
				identity.Name = sessionName
			}
			return identity, err
		}
		query := scope.projectQuery()
		var selectorErr *selector.SelectorError
		if query.Project == nil || query.Project.IsUID() || !errors.As(err, &selectorErr) || !selectorErr.IsNoMatch() {
			return liveSessionIdentity{}, err
		}
		// Even when the name is neither registered nor discovered, a live
		// same-name session must not become the selector's identity source via
		// lifecycle import.
		if err := c.runtime.refuseUnregisteredSessionClaims(ctx, query.Project.Name, ""); err != nil {
			return liveSessionIdentity{}, err
		}

		// A name that matches no Project used to be a name that was about to
		// become one: the reconcile prelude registered every discovered workdir,
		// so this guard only had to predict the name that registration would
		// allocate. Registration is explicit now, so the honest answer is a
		// refusal -- and the useful refusal names the exact path and the route
		// that would register it, which is why discovery is still read here.
		return liveSessionIdentity{}, c.unregisteredProjectRefusal(query.Project.Name, err)
	}
}

// unregisteredProjectRefusal turns a Project selector no-match into an actionable
// bootstrap instruction.
//
// Discovery is consulted for the message and for nothing else. Finding a candidate
// directory whose basename matches the requested name does not register it, does
// not reserve the name, and does not change the refusal -- it only lets the error
// name the exact `--root` an operator would pass next.
func (c *createCommand) unregisteredProjectRefusal(name string, cause error) error {
	name = strings.TrimSpace(name)
	if root := c.discoveredCandidateRootFor(name); root != "" {
		return usageError(fmt.Sprintf(
			"create: no Registry Project is named %q; %q is a discovered directory that no Project claims. Register it with `projmux create project --root %s`, or open it once from the Project sidebar",
			name, root, root))
	}
	return cause
}

// discoveredCandidateRootFor returns the discovered, unregistered directory whose
// automatic Project name would be name, or empty when there is none.
func (c *createCommand) discoveredCandidateRootFor(name string) string {
	if name == "" || c.reconciler == nil || c.reconciler.discoverRoots == nil {
		return ""
	}
	roots, err := c.reconciler.discoverRoots()
	if err != nil {
		return ""
	}
	roots = slices.Clone(roots)
	slices.SortStableFunc(roots, func(a, b string) int {
		return strings.Compare(candidates.CanonicalPath(a), candidates.CanonicalPath(b))
	})
	for _, root := range roots {
		if coremetadata.ProjectNameBase(root) == name {
			return root
		}
	}
	return ""
}

func (c *createCommand) exactProjectOwnershipGuard(projectUID string) createPreReconcile {
	return func(ctx context.Context, working coremetadata.Registry, _ coremetadata.Mutator, _ string) (liveSessionIdentity, error) {
		project, ok := working.Project(projectUID)
		if !ok {
			return liveSessionIdentity{}, fmt.Errorf("create: project %q disappeared before ownership preflight", projectUID)
		}
		sessionName := c.reconciler.projectPhysicalSessionName(working, *project)
		if sessionName == "" {
			return liveSessionIdentity{}, fmt.Errorf("create: Project %q has no valid collision-safe physical session name", project.Metadata.UID)
		}
		identity, _, err := c.runtime.preflightSessionOwnership(ctx, *project, sessionName)
		if identity.Name == "" {
			identity.Name = sessionName
		}
		return identity, err
	}
}

// transact runs one create operation under the declared transaction order:
// full preflight -> operation id -> created-resource ledger -> metadata
// mutation -> runtime mutation -> commit.
//
// Everything happens inside a single registry transaction, including the
// mutation-route registry reconciliation. The store applies the operation to a
// private clone and only writes when the whole body succeeds, so any failure --
// a pre-create hook refusal, a stale anchor, a tmux error -- leaves the registry
// file byte-identical. The tmux objects the body created are undone from the
// runtime ledger, which is the half no transaction can roll back for us.
func (c *createCommand) transact(op createOperation, guards ...createPreReconcile) error {
	if c == nil || c.store == nil || c.store.update == nil || c.runtime == nil || c.reconciler == nil {
		return errors.New("create: the resource-backed create routes are not configured")
	}
	ctx := context.Background()
	// Parsing and scope resolution have already succeeded before a resource
	// handler reaches the transaction. Bind the exact app-owned route here so
	// malformed argv keeps its stable usage failure and no environment probe can
	// preempt it, while reconciliation and every later write share one route.
	if err := c.ensureRuntimeRoute(ctx); err != nil {
		return err
	}
	operationID, err := c.newOperationID()
	if err != nil {
		return err
	}
	ledger := newRuntimeLedger(operationID)
	guard := func(ctx context.Context, working coremetadata.Registry, mutator coremetadata.Mutator, operationID string) (liveSessionIdentity, error) {
		var selected liveSessionIdentity
		for _, candidate := range guards {
			if candidate != nil {
				identity, err := candidate(ctx, working.Clone(), mutator, operationID)
				if err != nil {
					return liveSessionIdentity{}, err
				}
				if selected.Name != "" && identity.Name != "" && selected.Name != identity.Name {
					return liveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux sessions %q and %q", selected.Name, identity.Name)
				}
				if selected.ID != "" && identity.ID != "" && selected.ID != identity.ID {
					return liveSessionIdentity{}, fmt.Errorf("create: ownership guards selected different tmux session ids %q and %q", selected.ID, identity.ID)
				}
				if identity.Name != "" {
					selected = identity
				}
			}
		}
		return selected, nil
	}

	_, err = c.store.update(func(working *coremetadata.Registry) error {
		if _, err := guard(ctx, working.Clone(), c.store.mutator(), operationID); err != nil {
			return err
		}
		if err := c.reconciler.reconcileGuarded(ctx, working, c.store.mutator(), operationID, guard); err != nil {
			return err
		}
		if err := op(ctx, working, c.store.mutator(), operationID, ledger); err != nil {
			return err
		}
		// The lifecycle hook caused by our own tmux mutation deliberately
		// defers while this transaction owns the registry lock. Re-run the same
		// reconciler after all explicit mirrors are in place so the committed
		// status and the live tmux projection agree before create returns.
		return c.reconciler.reconcileGuarded(ctx, working, c.store.mutator(), operationID, guard)
	})
	if err != nil {
		c.runtime.rollback(ctx, ledger)
		c.runtime.clearCreateOperations(ctx, ledger)
		return MapMetadataError(err)
	}
	c.runtime.clearCreateOperations(ctx, ledger)
	return nil
}

// resolveProjection maps the `-o` token onto the shared output catalog of the
// canonical route.
func (c *createCommand) resolveProjection(spelling, token string) (cli.OutputMode, error) {
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

// writeResults renders a committed create through the shared output catalog.
//
// Nothing reaches stdout before the transaction commits, so a failed create
// leaves zero bytes there.
func (c *createCommand) writeResults(stdout io.Writer, spelling string, mode cli.OutputMode, kind coremetadata.Kind, results []createResult) error {
	slices.SortStableFunc(results, func(a, b createResult) int {
		if got := cmp.Compare(a.projectName, b.projectName); got != 0 {
			return got
		}
		if got := cmp.Compare(a.windowName, b.windowName); got != 0 {
			return got
		}
		return cmp.Compare(a.windowUID, b.windowUID)
	})

	switch mode {
	case cli.OutputModeDefault:
		for _, result := range results {
			if _, err := fmt.Fprintf(stdout, "%s/%s created\n", strings.ToLower(string(result.kind)), result.name); err != nil {
				return err
			}
		}
		return nil
	case cli.OutputModePaneID:
		for _, result := range results {
			if _, err := fmt.Fprintln(stdout, result.paneID); err != nil {
				return err
			}
		}
		return nil
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	matches := make([]selector.Match, 0, len(results))
	for _, result := range results {
		matches = append(matches, selector.Match{
			Kind:  kind,
			UID:   result.uid,
			Name:  result.name,
			Owner: selector.OwnerContext{Project: result.projectName, Window: result.windowName},
		})
	}
	// A create result is a fan-out even when it produced exactly one resource,
	// so the structured modes keep the List envelope.
	// The default projection of a create is the `<kind>/<name> created` line
	// handled above, so the columnar table -- and with it the only consumer of a
	// clock -- is unreachable from here.
	return writeResourceProjection(stdout, spelling, mode, kind, matches, registry, true, time.Time{})
}
