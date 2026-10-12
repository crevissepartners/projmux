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
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// The two canonical create spellings this Phase makes executable.
const (
	canonicalCreateWindow = "create window"
	canonicalCreatePane   = "create pane"
)

// placementDown is the vertical member of the closed placement enum.
const placementDown = mutationruntime.PlacementDown

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
	// action is what actually happened to this resource. It is `created` for
	// every create except the same-root `create project` repeat, which reuses an
	// existing graph and has always said "created" anyway -- the exact mismatch
	// the operation receipt exists to close.
	action cli.ReceiptAction
}

// receiptAction answers `created` for the zero value so every existing create
// path keeps its meaning without restating it.
func (r createResult) receiptAction() cli.ReceiptAction {
	if r.action == "" {
		return cli.ActionCreated
	}
	return r.action
}

// resourceCreateFlags is the parsed argv of a resource-backed create route.
type resourceCreateFlags struct {
	// profileProjectUID is the target resolved from Registry ownership, never cwd.
	profileProjectUID string
	projects          repeatedFlag
	windows           repeatedFlag
	panes             repeatedFlag
	selectors         repeatedFlag
	labels            repeatedFlag
	name              string
	host              string
	detach            bool
	processWindow     bool
	placementSet      bool
	provider          string
	providerSet       bool
	// creator is the bare Agent UID --creator declared, or empty.
	// Both tmux and process requests consume this already validated UID.
	creator string
	cwd     string
	addDirs repeatedFlag
	// cwdSet records that --cwd was spelled at all. An explicit Agent working
	// directory never reads the split start config.
	cwdSet bool
	// cwdFrom is the spelled --cwd-from split start source; empty defers to
	// config. It is launch data only and never selects a scope.
	cwdFrom      string
	placement    string
	createWindow bool
	// allWindows and primaryWindow are the two explicit target-cardinality
	// spellings of a child create. --primary-window names the exact-one
	// `spec.primaryWindowRef` selection a `--project`-only scope now resolves on
	// its own, and --all-windows names the whole-Project fan-out that scope used
	// to resolve and that this flag keeps available. Neither is a filter, so
	// both are mutually exclusive with every selector that names a Window and
	// with each other.
	allWindows    bool
	primaryWindow bool
	output        string
	payload       []string
	// interactiveOnly is the public opt-out from Codex native turn control. It
	// is the only spelling that produces a plain-CLI Codex Agent with no native
	// thread binding, which is what keeps "no native authority" a thing the
	// operator asked for rather than a silent consequence of an unreachable
	// endpoint.
	interactiveOnly   bool
	dialogueReplyOnly bool
	// model and effort choose what a new Claude Agent runs with. They are
	// passed to the provider as its own --model and --effort, and both are
	// recorded as Agent annotations. Every resume passes the effort again; the
	// model is not re-passed, because the provider restores it on resume.
	model  string
	effort string
	// persona names a stored persona the new Agent starts with, and
	// personaLaunch is what preparePersonaLaunch resolved it to. Claude is
	// given the snapshot path and Codex the snapshot content, as the thread's
	// developer instructions; either way the name and digest are recorded as
	// Agent annotations and the content never reaches argv.
	persona       string
	personaOption string
	personaLaunch personaLaunch
	// profile is the spelled --profile: a profile name, "none" for no profile
	// and no role mapping, or "" when it was not spelled (a `role` label may
	// then select one). profileLaunch is what resolveCreateProfile resolved,
	// the zero value meaning no profile.
	profile       string
	profileLaunch profileLaunch
	// resumeConversation is set by the Projmux split UI's resume selection and by
	// nothing else. It is deliberately not a parsed flag: no public spelling of
	// `create` accepts it, so an operator cannot reach a resume through the create
	// verb, which is `agent resume`'s job for a Registry Agent and this field's
	// job for a conversation the picker found on disk.
	resumeConversation    string
	resumeSource          string
	resumeEndpoint        coremetadata.CodexEndpointRef
	resumeGenerationState coremetadata.CodexGenerationState
	// resumeLaunchValues is the launch-value bundle (persona, persona digest,
	// system prompt snapshot mode, effort) a resume-picker create inherited
	// from the Agents that already recorded resumeConversation. It is set only
	// inside that create's transaction, and nil everywhere else.
	resumeLaunchValues map[string]string
	// projectLinks is the create's Project label link rules, resolved by
	// prepareProjectLinks inside the create transaction. The zero value
	// changes no argv and no annotation.
	projectLinks projectLinksLaunch
	// agentGuidance is the create's agent guidance, resolved by
	// prepareAgentGuidance right after prepareProjectLinks. The zero value
	// changes no argv and no annotation.
	agentGuidance agentGuidanceLaunch
}

// resourceCreateShape selects which optional flag groups a resource-backed
// create route registers.
//
// The groups are per-route rather than per-flag because they travel together:
// a route that splits an existing Window needs the whole anchor surface, and a
// route that creates an Agent needs the provider. Keeping them grouped is what
// stops `create window` from silently accepting `--placement`.
type resourceCreateShape struct {
	// host is registered by canonical Agent and Window creation.
	host bool
	// split registers the Window fan-out and split-anchor surface:
	// --window, --pane, --selector, --create-window, --placement.
	split bool
	// provider registers --provider. The provider shortcuts register it too,
	// so that respelling the provider they already name is rejected with the
	// shortcut's own message instead of a bare "flag not defined".
	provider bool
	// initialProvider registers --provider on its own, with none of the Agent
	// tuning flags `provider` brings. It is what `create window` needs: that
	// route names the surface its first Pane opens with and nothing else, so
	// --model, --effort, --add-dir, --cwd and --interactive-only stay where the
	// complete Agent route already spells them. The two fields are exclusive --
	// one flag registered twice is a flag package panic, not a refusal.
	initialProvider bool
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
// they are derived only when no --window, --pane, --selector, --all-windows, or
// --primary-window was given at all. That is the same "an omitted selector is
// the whole selector" rule the read and rename verbs use, and it is what keeps
// a bare `create pane --placement right` -- the generated keybinding body -- a
// split of the Window the operator is looking at rather than of the Project's
// primary Window, which is what the same route resolves once a --project scope
// is typed. One explicit scope occurrence turns the whole scope explicit, so an
// operator who names a Window never gets a silent anchor from somewhere else.
func (c *createCommand) resolveCreateScope(spelling string, flags resourceCreateFlags, shape resourceCreateShape) (createScope, error) {
	if len(flags.projects) == 1 {
		ref, err := selector.ParseRef(coremetadata.KindProject, flags.projects[0])
		if err != nil {
			return createScope{}, MapMetadataError(err)
		}
		return createScope{project: ref}, nil
	}
	if flags.explicitWindowScope() {
		return c.resolveExplicitCreateScope(spelling, flags)
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
	if !shape.split || flags.windowScopeSpelled() {
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

// resolveExplicitCreateScope derives the one Project owner from selectors the
// operator actually supplied. It deliberately does not consult the invoking
// Pane: an explicit Window/Pane/label target must have the same meaning under
// an unrelated focused client, and a missing Window ensure has no owner unless
// --project names one.
func (c *createCommand) resolveExplicitCreateScope(spelling string, flags resourceCreateFlags) (createScope, error) {
	if c == nil || c.store == nil || c.store.load == nil {
		return createScope{}, errors.New("create: the resource-backed create routes are not configured")
	}
	registry, err := c.store.load()
	if err != nil {
		return createScope{}, MapMetadataError(err)
	}
	query := selector.Query{}
	for _, raw := range flags.windows {
		ref, parseErr := selector.ParseRef(coremetadata.KindWindow, raw)
		if parseErr != nil {
			return createScope{}, MapMetadataError(parseErr)
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range flags.selectors {
		label, parseErr := selector.ParseLabel(raw)
		if parseErr != nil {
			return createScope{}, MapMetadataError(parseErr)
		}
		query.Labels = append(query.Labels, label)
	}

	projectUIDs := map[string]bool{}
	addWindow := func(windowUID string) error {
		window, ok := registry.Window(windowUID)
		if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
			return usageError(fmt.Sprintf("%s: explicit target window uid:%s is not owned by a Project; pass --project <ref>", spelling, windowUID))
		}
		projectUIDs[window.Metadata.OwnerUID()] = true
		return nil
	}

	if len(query.Windows) > 0 || len(query.Labels) > 0 {
		windows, err := selector.New(registry).ResolveWindows(query)
		if err != nil {
			return createScope{}, MapMetadataError(err)
		}
		for _, match := range windows.Matches {
			if err := addWindow(match.UID); err != nil {
				return createScope{}, err
			}
		}
	}

	if len(flags.panes) > 0 {
		paneQuery := selector.Query{Windows: query.Windows}
		for _, raw := range flags.panes {
			ref, parseErr := selector.ParseRef(coremetadata.KindPane, raw)
			if parseErr != nil {
				return createScope{}, MapMetadataError(parseErr)
			}
			paneQuery.Panes = append(paneQuery.Panes, ref)
		}
		panes, resolveErr := selector.New(registry).ResolvePanes(paneQuery)
		if resolveErr != nil {
			return createScope{}, MapMetadataError(resolveErr)
		}
		for _, match := range panes.Matches {
			pane, ok := registry.Pane(match.UID)
			if !ok || pane.Metadata.OwnerRef == nil {
				continue
			}
			windowUID := pane.Metadata.OwnerUID()
			if pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
				agent, ok := registry.Agent(windowUID)
				if !ok {
					continue
				}
				windowUID = agent.Metadata.OwnerUID()
			}
			if err := addWindow(windowUID); err != nil {
				return createScope{}, err
			}
		}
	}

	if len(projectUIDs) != 1 {
		detail := "matched no Project owner"
		if len(projectUIDs) > 1 {
			detail = "matched more than one Project owner"
		}
		return createScope{}, usageError(fmt.Sprintf(
			"%s: explicit Window/Pane selector %s; ambient TMUX/TMUX_PANE is not target authority, so pass --project <ref>",
			spelling, detail))
	}
	for uid := range projectUIDs {
		return createScope{project: uidRef(coremetadata.KindProject, uid)}, nil
	}
	panic("unreachable explicit create Project scope")
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

// deprecatedPersonaFlagGiven reports whether args spell the deprecated
// `--persona` flag on a route whose parser registers it. It reads args the way
// the flag package does -- one or two dashes, `=value` or the next token as
// the value of a non-boolean flag -- so the value of another flag is never
// taken for the spelling, and it keeps reading past a token the parse will
// reject.
func deprecatedPersonaFlagGiven(fs *flag.FlagSet, args []string) bool {
	if fs.Lookup("persona") == nil {
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' {
			continue
		}
		name, _, inline := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if name == "persona" {
			return true
		}
		registered := fs.Lookup(name)
		if registered == nil || inline {
			continue
		}
		if boolean, ok := registered.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		i++
	}
	return false
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
	setRouteUsage(fs)
	var legacyPersona string
	fs.Var(&out.projects, "project", "at-most-one Project scope: <name> or uid:<uid>; defaults to the active tmux runtime's managed Project")
	fs.Var(&out.projects, "p", "at-most-one Project scope: <name> or uid:<uid> (alias of --project)")
	if shape.initialProvider {
		fs.StringVar(&out.provider, "provider", "",
			"surface the Window's first Pane opens with: shell|"+strings.Join(cli.AgentProviders(), "|")+"; omitted means shell")
	}
	if shape.host {
		fs.StringVar(&out.host, "host", "tmux", "tmux (unchanged default) or process (owned foreground; hooks require runtime opt-in)")
		if spelling == canonicalCreateAgent {
			fs.BoolVar(&out.detach, "detach", false, "process only: return after startup; keep the Agent alive until agent stop or provider exit")
		}
	}
	if shape.provider {
		fs.BoolVar(&out.dialogueReplyOnly, claudeDialogueReplyOnlyFlag, false, "claude only: a headless activation with an isolated explicit reply tool, recorded so every resume of the Agent stays reply-only; qualification required")
		fs.StringVar(&out.provider, "provider", "", "Agent provider: "+strings.Join(cli.AgentProviders(), "|"))
		fs.StringVar(&out.cwd, "cwd", "", "effective Agent working directory (defaults to Project root)")
		fs.Var(&out.addDirs, "add-dir", "repeatable additional writable root")
		fs.BoolVar(&out.interactiveOnly, "interactive-only", false,
			"codex only: launch a plain interactive CLI Agent with no native thread binding")
		fs.StringVar(&out.model, "model", "", "claude or codex: model name the new session runs")
		fs.StringVar(&out.effort, "effort", "", "claude or codex: effort level: "+strings.Join(claudeEffortLevels, "|"))
		fs.StringVar(&out.persona, "instructions", "", "stored instructions the new session starts with; claude always, codex only with a prompt; manage with projmux instructions")
		fs.StringVar(&legacyPersona, "persona", "", "deprecated alias of --instructions")
		fs.StringVar(&out.profile, "profile", "", "named Agent profile the new session starts with; none turns off role mapping; manage with projmux profile")
	}
	if pane {
		fs.Var(&out.windows, "window", "repeatable Window selector: <name> or uid:<uid>")
		fs.Var(&out.windows, "w", "repeatable Window selector: <name> or uid:<uid> (alias of --window)")
		fs.Var(&out.panes, "pane", "repeatable anchor Pane selector: <name> or uid:<uid>")
		fs.Var(&out.selectors, "selector", "repeatable Window label filter: key=value (AND)")
		fs.BoolVar(&out.createWindow, "create-window", false, "create the exact-name --window Windows that do not exist yet")
		fs.BoolVar(&out.allWindows, "all-windows", false,
			"target every Window of the Project scope; the opt-in whole-Project fan-out")
		fs.BoolVar(&out.primaryWindow, "primary-window", false,
			"target exactly the Project scope's spec.primaryWindowRef Window; what a --project scope with no Window selector already means")
		fs.StringVar(&out.placement, "placement", defaultPlacement, "split placement: "+strings.Join(placementDirections, "|"))
		fs.StringVar(&out.cwdFrom, splitCWDFromFlag, "",
			"split start directory: project|pane; defaults to project. The CLI does not read [ai] split_cwd_from")
	}
	if shape.provider || shape.initialProvider {
		fs.StringVar(&out.creator, creatorFlagName, "", creatorFlagUsage)
	}
	fs.StringVar(&out.name, "name", "", "explicit Projmux metadata.name for the created resource")
	fs.Var(&out.labels, "label", "repeatable creation label: key=value")
	fs.StringVar(&out.output, "output", "", "result projection")
	fs.StringVar(&out.output, "o", "", "result projection (alias of --output)")

	// The deprecated spelling prints its notice once, before the parse, so a
	// create that is then refused -- even by another flag -- names the
	// replacement too. stderr rather than stdout, like
	// warnDeprecatedProjectDeleteAlias: a create given `--persona` keeps the
	// stdout, the projection, and the exit code it always had.
	personaFlagGiven := deprecatedPersonaFlagGiven(fs, head)
	if personaFlagGiven && stderr != nil {
		fmt.Fprintln(stderr, cli.DeprecatedPersonaFlagNotice)
	}
	if err := fs.Parse(head); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return resourceCreateFlags{}, err
		}
		return resourceCreateFlags{}, flagParseError(err)
	}
	if fs.NArg() != 0 {
		return resourceCreateFlags{}, usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}
	if out.persona != "" && legacyPersona != "" {
		return resourceCreateFlags{}, usageError(spelling + " accepts only one of --instructions and --persona")
	}
	if legacyPersona != "" {
		out.persona, out.personaOption = legacyPersona, "persona"
	} else if out.persona != "" {
		out.personaOption = "instructions"
	}
	cwdFromSet := false
	profileSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "profile":
			profileSet = true
		case "placement":
			out.placementSet = true
		case "provider":
			out.providerSet = true
		case "cwd":
			out.cwdSet = true
		case splitCWDFromFlag:
			cwdFromSet = true
		}
	})
	if profileSet && strings.TrimSpace(out.profile) == "" {
		return resourceCreateFlags{}, usageError(spelling + " --profile requires a profile name, or none; nothing was created")
	}
	if len(out.projects) > 1 {
		return resourceCreateFlags{}, usageError(spelling +
			" accepts at most one --project <ref>; an omitted --project takes the active tmux runtime's managed Project")
	}
	if pane && !slices.Contains(placementDirections, out.placement) {
		return resourceCreateFlags{}, usageError(fmt.Sprintf("%s --placement must be one of: %s",
			spelling, strings.Join(placementDirections, ", ")))
	}
	if cwdFromSet {
		if _, ok := parseSplitCWDSource(out.cwdFrom); !ok {
			return resourceCreateFlags{}, usageError(fmt.Sprintf("%s --%s must be one of: %s, %s",
				spelling, splitCWDFromFlag, splitCWDFromProject, splitCWDFromPane))
		}
		if out.cwdSet {
			return resourceCreateFlags{}, usageError(fmt.Sprintf(
				"%s --%s cannot be combined with --cwd: --cwd already names the Agent working directory; nothing was created",
				spelling, splitCWDFromFlag))
		}
	}
	creatorSet := false
	fs.Visit(func(f *flag.Flag) { creatorSet = creatorSet || f.Name == creatorFlagName })
	if creatorSet {
		if out.creator, err = parseCreatorFlag(spelling, out.creator); err != nil {
			return resourceCreateFlags{}, err
		}
	}
	if err := out.refuseConflictingWindowScope(spelling); err != nil {
		return resourceCreateFlags{}, err
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
	if out.detach && out.host != "process" {
		return resourceCreateFlags{}, usageError("create agent: --detach requires --host process; nothing was created")
	}
	return out, nil
}

// isExactNameRef reports whether a --window occurrence names a Window that
// --create-window may create. A `uid:` occurrence never does: an unresolvable
// uid names nothing, so there is no name to allocate.
func isExactNameRef(raw string) bool {
	return mutationruntime.IsExactNameRef(raw)
}

// windowScopeFlagConflict is one flag an explicit cardinality flag cannot be
// combined with, plus the predicate that reads it off the parsed argv.
type windowScopeFlagConflict struct {
	flag    string
	spelled func(resourceCreateFlags) bool
}

// windowScopeFlagConflicts is the closed conflict table of --all-windows and
// --primary-window.
//
// Both name a target set outright rather than filtering one, so pairing either
// with a selector, with a Window ensure, or with the other one asks for two
// different target sets in one invocation. There is no precedence rule that
// would make one of them the answer, so the pair is refused as operator input.
var windowScopeFlagConflicts = []windowScopeFlagConflict{
	{flag: "--window", spelled: func(f resourceCreateFlags) bool { return len(f.windows) > 0 }},
	{flag: "--pane", spelled: func(f resourceCreateFlags) bool { return len(f.panes) > 0 }},
	{flag: "--selector", spelled: func(f resourceCreateFlags) bool { return len(f.selectors) > 0 }},
	{flag: "--create-window", spelled: func(f resourceCreateFlags) bool { return f.createWindow }},
}

// refuseConflictingWindowScope is the argv-only cardinality conflict check.
//
// It runs inside the parser, so it lands before the projection, the label
// parse, the scope derivation, and the transaction: a conflicting pair exits 2
// with zero Registry writes, zero tmux objects, and zero provider calls.
func (f resourceCreateFlags) refuseConflictingWindowScope(spelling string) error {
	if f.allWindows && f.primaryWindow {
		return usageError(spelling +
			" --all-windows and --primary-window select different target sets; pass exactly one")
	}
	for _, exclusive := range []struct {
		flag    string
		spelled bool
	}{
		{flag: "--all-windows", spelled: f.allWindows},
		{flag: "--primary-window", spelled: f.primaryWindow},
	} {
		if !exclusive.spelled {
			continue
		}
		for _, conflict := range windowScopeFlagConflicts {
			if conflict.spelled(f) {
				return usageError(fmt.Sprintf(
					"%s %s already fixes the target Window set and cannot be combined with %s",
					spelling, exclusive.flag, conflict.flag))
			}
		}
	}
	return nil
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

// explicitWindowScope reports whether the argv named the Window scope itself.
//
// Any one of the three occurrences makes the whole scope explicit, which is what
// stops an implicit anchor from being blended into a fan-out the operator
// addressed on purpose.
func (f resourceCreateFlags) explicitWindowScope() bool {
	return len(f.windows) > 0 || len(f.panes) > 0 || len(f.selectors) > 0
}

// windowScopeSpelled reports whether the argv named the target Window
// cardinality at all, by selector or by one of the two explicit cardinality
// flags.
//
// It is deliberately wider than explicitWindowScope. The two predicates answer
// different questions: explicitWindowScope asks "did argv name a resource whose
// stored ownerRef can supply the Project", which only a --window/--pane/
// --selector occurrence can; windowScopeSpelled asks "did argv already decide
// how many Windows this create targets", which --all-windows and
// --primary-window also do. The second is what suppresses the natural-omitted
// active Window and active Pane derivation, so `create pane --primary-window`
// inside a managed Pane targets the Project's primary Window rather than
// blending it with the Window the operator happens to be looking at.
func (f resourceCreateFlags) windowScopeSpelled() bool {
	return f.explicitWindowScope() || f.allWindows || f.primaryWindow
}

// defaultsToPrimaryWindow reports whether argv fixed a Project scope and then
// said nothing at all about how many of that Project's Windows it meant.
//
// That spelling resolves the Project's spec.primaryWindowRef Window and nothing
// else -- the same exact-one set `--primary-window` names, so a call site that
// wants to say it out loud can, and `--all-windows` is the whole-Project
// fan-out this spelling used to resolve.
//
// The predicate is unchanged from the release that warned on it, so the
// invocations whose meaning narrowed are exactly the invocations that were told
// they would. `--create-window` stays excluded for the reason it was excluded
// from that warning: it asks for a Window ensure rather than for a selection
// over the Windows that already exist.
func (f resourceCreateFlags) defaultsToPrimaryWindow() bool {
	return len(f.projects) > 0 && !f.windowScopeSpelled() && !f.createWindow
}

// explicitTargetAuthority reports whether argv selected any resource scope or
// named an exact missing Window to ensure. Once one occurrence is explicit the
// whole target chain is explicit: inherited TMUX/TMUX_PANE may still be useful
// diagnostics, but it cannot choose, narrow, or refuse the target.
func (f resourceCreateFlags) explicitTargetAuthority() bool {
	return len(f.projects) > 0 || f.explicitWindowScope() || f.createWindow
}

// runResourceWindow answers the canonical `create window`.
//
// `--provider` names the surface the new Window opens with, and omitting it is
// the shell Pane this route has always made -- the same allocation, the same
// materialization, the same result. Naming an Agent provider adds three steps
// to the *same* transaction: the Agent and its managed Pane are allocated
// alongside the Window, that Pane is split off the initial shell once the
// Window is live, and the initial shell is then retired. What commits is
// therefore a Window holding one Agent Pane and nothing else, and a provider
// that refuses anywhere in there rolls the whole operation back -- no Window, no
// Pane. That is the difference from `create agent --create-window`, which
// commits a Window with a shell beside the Agent and keeps the shell when the
// Agent fails.
//
// The saved launch default is deliberately not consulted. `create window` is a
// canonical route, and a canonical route whose result depends on a UI setting is
// not canonical; TestPublicCreateWindowNeverReadsTheSavedLaunchDefault is that
// boundary's authority.
func (c *createCommand) runResourceWindow(args []string, stdout, stderr io.Writer) error {
	const spelling = canonicalCreateWindow

	shape := resourceCreateShape{initialProvider: true, host: true}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	if flags.host != "tmux" && flags.host != "process" {
		return usageError(fmt.Sprintf("create window --host %q requires tmux or process", flags.host))
	}
	if flags.host == "process" {
		if flags.provider == "" {
			return usageError("process-window-provider-required: create window --host process requires --provider claude or codex; nothing was created")
		}
		if flags.provider != aiModeClaude && flags.provider != aiModeCodex {
			return usageError("process-window-provider-unsupported: create window --host process requires Claude or Codex; nothing was created")
		}
		flags.processWindow = true
		return c.runProcessAgentCLI(flags, stdout, stderr)
	}
	// The initial surface is an argv-only decision, so a misspelled provider and
	// an interactive picker adapter are both refused before the Settings gate,
	// the scope derivation, and the transaction -- zero mutations, zero bytes of
	// stdout.
	provider, err := resolveInitialWindowProvider(spelling, flags)
	if err != nil {
		return err
	}
	if flags.creator != "" && provider == "" {
		return usageError(fmt.Sprintf(creatorFlagRequiresAgentRefusalFmt, spelling))
	}
	if provider != "" {
		if c.agents == nil {
			return errors.New("create window: the provider launcher is not configured")
		}
		// The Settings gate applies to this spelling for the reason it applies
		// to the canonical Agent route: naming a provider through a different
		// command does not re-enable one the operator switched off.
		if err := c.agents.RequireAgentEnabled(provider); err != nil {
			return err
		}
	}
	mode, err := resolveLifecycleProjection(spelling, flags.output)
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
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	if provider == "" {
		var out mutationruntime.ShellCreateResult
		err := c.transact(diagnostics.CreateKindWindow, func(ctx context.Context, reg *coremetadata.Registry, mut coremetadata.Mutator, op string, ledger *RuntimeLedger) error {
			return c.createExecution().WindowOperation(scope.shared(), mutationruntime.WindowRequest{Name: flags.name, Labels: labels, Payload: flags.payload}, &out)(ctx, reg, mut, op, ledger)
		}, c.projectOwnershipGuard(scope))
		if err != nil {
			return err
		}
		return c.writeResults(stdout, spelling, mode, coremetadata.KindWindow, localShellResults(out.Results))
	}

	var results []createResult
	var openedAgent coremetadata.Agent
	var activationTargets []agentActivationTarget
	var creator creatorRecord
	var linksNotice, guidanceNotice string
	if err := c.transact(diagnostics.CreateKindWindow, func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
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

		var agent agentWork
		var agentLaunch agentPaneLaunch
		if provider != "" {
			// The initial shell Pane is only the anchor the Agent splits off, so
			// the operator's payload is the Agent's initial task rather than that
			// Pane's command. The Window and that Pane keep the stored command
			// allocateWindow already derived from the same payload, which is what
			// makes the Window's name identical with and without --provider.
			work.payload = nil
			if creator, err = c.decideCreator(ctx, spelling, working, flags.creator); err != nil {
				return err
			}
			// The Agent's Project label link rules are the created Window's
			// Project's.
			c.prepareProjectLinks(provider, project, &flags)
			c.prepareAgentGuidance(provider, &flags)
			if agent, agentLaunch, err = c.allocateWindowAgent(
				working, mutator, project, provider, work, flags, labels, creator, operationID); err != nil {
				return err
			}
			linksNotice = flags.projectLinks.notice(agent.agent.Metadata.Name)
			guidanceNotice = flags.agentGuidance.notice(agent.agent.Metadata.Name)
		}

		sessionName, err := c.ensureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
		if err != nil {
			return err
		}
		if err := c.materializeWindow(ctx, working, mutator, ledger, project, sessionName, &work); err != nil {
			return err
		}
		paneID := work.initialPaneID
		if provider != "" {
			if paneID, err = c.openWindowAgentPane(ctx, working, mutator, ledger,
				provider, work, agent, agentLaunch, flags); err != nil {
				return err
			}
			// Both Panes exist now, which is what lets the shell go: retiring it
			// leaves the Agent Pane as the Window's anchor with no default shell
			// at all -- the Agent-only Window shape spec.defaultShellPaneRef has
			// always admitted as optional.
			if err := c.retireWindowInitialShell(ctx, working, mutator, work); err != nil {
				return err
			}
			openedAgent = agent.agent
			if len(flags.payload) > 0 {
				activationTargets = append(activationTargets, agentActivationTarget{
					agentUID:   agent.agent.Metadata.UID,
					agentName:  agent.agent.Metadata.Name,
					paneUID:    agent.pane.Metadata.UID,
					paneID:     paneID,
					generation: agent.activation.Generation,
				})
			}
		}
		results = append(results, createResult{
			kind: coremetadata.KindWindow,
			uid:  work.window.Metadata.UID,
			name: work.window.Metadata.Name,
			// `-o pane-id` is the Window's remaining Pane, which is the Agent's
			// managed Pane on the provider branch and the initial shell without
			// it. Either way it is the Pane an operator can address.
			paneID:      paneID,
			projectName: project.Metadata.Name,
			windowName:  work.window.Metadata.Name,
			windowUID:   work.window.Metadata.UID,
		})
		return nil
	}, c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	for _, notice := range []string{linksNotice, guidanceNotice} {
		if notice != "" {
			// A lost disclosure must not turn a committed create into a failure.
			fmt.Fprintln(stderr, notice)
		}
	}
	if err := c.confirmAgentActivations(activationTargets); err != nil {
		return err
	}
	if err := c.warnUnregisteredClaudeActivations(activationTargets, stderr); err != nil {
		return err
	}
	creator.report(stderr)
	receipt := createResultsReceipt(coremetadata.KindWindow, results)
	if openedAgent.Metadata.UID != "" {
		// The receipt records what the operation did, and on this branch it
		// opened an Agent. The Window row alone would report a `create.window`
		// that produced no Agent at all.
		receipt.Add(strings.ToLower(string(coremetadata.KindAgent)),
			openedAgent.Metadata.UID, openedAgent.Metadata.Name, cli.ActionCreated)
	}
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindWindow, results, receipt)
}

// agentPaneLaunch is the provider launch one `create window --provider`
// allocated, carried from the metadata phase into the runtime phase.
//
// It is built before the first tmux call for the same reason `create agent`
// builds it there: a missing provider binary is the likeliest failure on this
// route, and it has to land while the operation still owns nothing.
type agentPaneLaunch struct {
	workspace coremetadata.AgentWorkspace
	title     string
	argv      []string
}

// allocateWindowAgent allocates the Agent half of one `create window
// --provider` in metadata only.
//
// It splits no Pane and calls no tmux: every refusal reachable from the
// operator's argv -- a provider the Settings gate allows but whose binary is
// absent, an --add-dir outside the Project, an explicit --name already taken --
// lands here, with the Window still uncommitted and nothing materialized.
func (c *createCommand) allocateWindowAgent(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	project coremetadata.Project,
	provider string,
	work windowWork,
	flags resourceCreateFlags,
	labels map[string]string,
	creator creatorRecord,
	operationID string,
) (agentWork, agentPaneLaunch, error) {
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspace
	}
	// This route spells no --cwd and no --add-dir, so the Agent's workspace is
	// the Project root the Window was created under.
	workspace, err := resolver(*working, project, provider, "", nil)
	if err != nil {
		return agentWork{}, agentPaneLaunch{}, err
	}
	title, launchArgv, err := c.planAgentPaneLaunch(provider, workspace, flags)
	if err != nil {
		return agentWork{}, agentPaneLaunch{}, err
	}
	agent, err := mutator.CreateAgent(working, work.window.Metadata.UID, coremetadata.CreateAgentOptions{
		// An explicit --name names the Window on this route, so the Agent takes
		// the generated one and its managed Pane derives from that.
		Provider:    provider,
		Labels:      labels,
		Annotations: flags.agentGuidance.withCreateAnnotation(flags.projectLinks.withCreateAnnotation(creator.annotations())),
		Workspace:   workspace,
		Activation:  activationStateForPayload(flags.payload),
		OperationID: operationID,
	})
	if err != nil {
		return agentWork{}, agentPaneLaunch{}, MapMetadataError(err)
	}
	creator = creator.forAgent(working, agent.Metadata.UID)
	pane, err := mutator.AttachAgentPane(working, agent.Metadata.UID, coremetadata.BootstrapPane{
		Name:   derivedAgentPaneName(agent.Metadata.Name),
		CWD:    workspace.CWD,
		Labels: labels,
	}, operationID)
	if err != nil {
		return agentWork{}, agentPaneLaunch{}, MapMetadataError(err)
	}
	pane = creator.annotatePane(working, pane)
	activation, err := c.issuePaneActivation(working, mutator, pane.Metadata.UID, agent.Metadata.UID, operationID)
	if err != nil {
		return agentWork{}, agentPaneLaunch{}, err
	}
	return agentWork{
		target:     paneTarget{windowUID: work.window.Metadata.UID, anchorUID: work.initial.Metadata.UID, storedAnchor: true},
		windowName: work.window.Metadata.Name,
		agent:      agent,
		pane:       pane,
		activation: activation,
	}, agentPaneLaunch{workspace: workspace, title: title, argv: launchArgv}, nil
}

// openWindowAgentPane materializes the allocated Agent Pane beside the Window's
// initial shell and returns its exact tmux pane id.
//
// The anchor is the Pane materializeWindow just created, so this is the same
// split every Agent create performs -- claimed for rollback, mirrored, and given
// the managed-agent presentation options -- and not a second definition of what
// opening an Agent means. The layout equalization a split normally ends with is
// deliberately absent: the peer it would resize against is the shell this
// operation is about to retire, after which tmux gives the Agent the whole
// Window on its own.
func (c *createCommand) openWindowAgentPane(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	ledger *RuntimeLedger,
	provider string,
	work windowWork,
	agent agentWork,
	launch agentPaneLaunch,
	flags resourceCreateFlags,
) (string, error) {
	paneID, err := c.runtime.SplitPane(ctx, work.initialPaneID, defaultPlacement, launch.workspace.CWD,
		c.runtime.SupervisedLaunch(ctx, agent.activation, launch.argv), nil)
	if paneID != "" {
		if claimErr := c.runtime.ClaimRuntimeUIDForRollback(ctx, runtimePane, paneID, agent.pane.Metadata.UID, ledger); claimErr != nil {
			return "", errors.Join(err, claimErr)
		}
		if mirrorErr := c.runtime.MirrorPaneIdentity(ctx, paneID, agent.pane); mirrorErr != nil {
			return "", errors.Join(err, mirrorErr)
		}
		observeActivationRuntime(working, mutator, agent.activation, paneID, c.runtime.Warn)
	}
	if err != nil {
		return "", err
	}
	if err := c.bindAgentPane(ctx, paneID, provider, launch.workspace.CWD, launch.title, "", flags); err != nil {
		return "", fmt.Errorf("%s: bind Agent Pane %s presentation metadata: %w", canonicalCreateWindow, paneID, err)
	}
	return paneID, nil
}

// retireWindowInitialShell removes the Pane the Window was created with, in both
// authorities, once the Agent Pane has taken its place.
//
// The Registry row goes first: DeletePane reselects the Window's anchor onto the
// Agent Pane and empties the optional default shell, and a refusal there leaves
// the tmux pane alive rather than stranding a Pane the Registry no longer
// describes. The tmux half then names the exact handle this operation mirrored
// its own uid onto, which is what stops it from killing a Pane something else
// put in that Window between the split and this write.
func (c *createCommand) retireWindowInitialShell(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	work windowWork,
) error {
	if err := mutator.DeletePane(working, work.initial.Metadata.UID); err != nil {
		return MapMetadataError(err)
	}
	return c.runtime.RetireOwnedPane(ctx, work.initialPaneID, work.initial.Metadata.UID)
}

// runResourcePane answers the resource-backed `create pane`.
func (c *createCommand) runResourcePane(args []string, stdout, stderr io.Writer) error {
	const spelling = canonicalCreatePane

	shape := resourceCreateShape{split: true}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	mode, err := resolveLifecycleProjection(spelling, flags.output)
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
	c.selectRuntimeAuthority(flags.explicitTargetAuthority())

	var results []createResult
	var selectedWindowUIDs []string
	var notices []string
	if err := c.transactAdmitted(diagnostics.CreateKindPane, c.processAnchorAdmission(scope, flags, spelling), func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		var out mutationruntime.ShellCreateResult
		err := c.createExecution().PaneOperation(scope.shared(), mutationruntime.ShellPaneRequest{CreateSelection: flags.sharedSelection(), Payload: flags.payload, Placement: flags.placement, CwdFrom: flags.cwdFrom}, labels, &out)(ctx, working, mutator, operationID, ledger)
		results = localShellResults(out.Results)
		selectedWindowUIDs = out.SelectedWindowUIDs
		notices = out.Notices
		return err
	}, c.projectOwnershipGuard(scope)); err != nil {
		return err
	}
	if err := writeSplitCWDNotices(stderr, notices); err != nil {
		return err
	}
	return c.writeResultsWithReceipt(stdout, spelling, mode, coremetadata.KindPane, results,
		createPlannedReceipt(coremetadata.KindPane, results, selectedWindowUIDs))
}

// paneWork is one allocated Pane waiting for its runtime split.

// processAnchorAdmission checks a read-only snapshot before route binding and
// repeats against locked bytes before reconciliation or resource allocation.
// Nil retains the legacy injectable transaction order.
func (c *createCommand) processAnchorAdmission(scope createScope, flags resourceCreateFlags, spelling string) func(coremetadata.Registry) error {
	return c.createExecution().ProcessAnchorAdmission(scope.shared(), flags.sharedSelection(), spelling)
}

// primaryWindowSpelling names how argv asked for the exact-one primary Window,
// or "" when it did not ask for it at all.
//
// The two spellings resolve the same Window but are not interchangeable in a
// refusal: telling an operator that `--primary-window` is dangling when they
// never typed it names a flag they would then have to go find.
func (f resourceCreateFlags) primaryWindowSpelling() string {
	switch {
	case f.primaryWindow:
		return "--primary-window"
	case f.defaultsToPrimaryWindow():
		return "--project"
	default:
		return ""
	}
}

// writeResults renders a committed create through the shared output catalog.
//
// Nothing reaches stdout before the transaction commits, so a failed create
// leaves zero bytes there.
func (c *createCommand) writeResults(stdout io.Writer, spelling string, mode cli.OutputMode, kind coremetadata.Kind, results []createResult) error {
	return c.writeResultsWithReceipt(stdout, spelling, mode, kind, results, createResultsReceipt(kind, results))
}

// writeResultsWithReceipt renders a committed create through the shared output
// catalog and the operation receipt the route actually earned.
//
// The receipt is a separate argument rather than a projection of `results`
// because the two answer different questions. `results` is what the create
// *produced* and is what the scalar and structured modes print, byte for byte,
// as they always have. The receipt is what the operation *did*, which for
// `create project` includes the bootstrap Window and Pane that the Project row
// alone never mentioned.
func (c *createCommand) writeResultsWithReceipt(
	stdout io.Writer,
	spelling string,
	mode cli.OutputMode,
	kind coremetadata.Kind,
	results []createResult,
	receipt cli.OperationReceipt,
) error {
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
			if _, err := fmt.Fprintf(stdout, "%s/%s %s\n",
				strings.ToLower(string(result.kind)), result.name, result.receiptAction()); err != nil {
				return err
			}
		}
		if err := receipt.Validate(); err != nil {
			return err
		}
		return receipt.WriteHuman(stdout)
	case cli.OutputModeReceipt:
		if err := receipt.Validate(); err != nil {
			return err
		}
		return receipt.WriteJSON(stdout)
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

// createResultsReceipt is the default receipt of a create whose route has no
// separate target planner to quote.
//
// `create window` and the split-UI intents each produce exactly one resource in
// exactly one Window, so the created row *is* the selection. Every route that
// fans out goes through createPlannedReceipt instead.
func createResultsReceipt(kind coremetadata.Kind, results []createResult) cli.OperationReceipt {
	selected := make([]string, 0, len(results))
	for _, result := range results {
		selected = append(selected, result.windowUID)
	}
	return createPlannedReceipt(kind, results, selected)
}

// createPlannedReceipt is the receipt of a child create, with the selected
// Window set supplied by the target planner rather than derived from the
// results.
//
// A child create is unconditional on every axis: it mints an identity, it
// allocates that identity's address, it establishes the owner edge, and it
// materializes the runtime for the Windows the target planner selected. The
// conditional route is `create project`, which builds its own receipt.
//
// selectedWindowUIDs is the planner's decision, recorded through the receipt's
// own deduplicating accumulator. No child-create spelling carries a
// compatibility warning any more: the one that did is now the primary-window
// default it was warning about.
func createPlannedReceipt(
	kind coremetadata.Kind,
	results []createResult,
	selectedWindowUIDs []string,
) cli.OperationReceipt {
	var rows []mutationruntime.CreateReceiptResource
	for _, result := range results {
		rows = append(rows, mutationruntime.CreateReceiptResource{Kind: result.kind, UID: result.uid, Name: result.name, Action: result.receiptAction()})
	}
	return cli.OperationReceipt(mutationruntime.CreateReceipt(kind, rows, selectedWindowUIDs))
}
