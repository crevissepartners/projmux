package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// renameKinds lists the kind spellings `rename` implements, in help order, each
// canonical token followed by its accepted aliases. See getKinds.
var renameKinds = cli.ChildSpellings("rename")

// renameCommand implements the canonical `rename` verb.
//
// It changes the Projmux `metadata.name` of exactly one resource and nothing
// else. It never writes raw tmux presentation and never invents a suffix: an
// explicit name already reserved in the target root/kind scope is a usage error
// with zero mutations.
type renameCommand struct {
	store  *resourceStore
	mirror resourceMutationMirror
	// runtime is the live-tmux observation the rendered result's Window and
	// Pane status is derived from; see runtime_observation.go.
	runtime runtimeLookup
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
	tmuxRunner   tmuxCommandRunner
	lookupEnv    func(string) string
}

func newRenameCommand() *renameCommand {
	return &renameCommand{
		store:        newResourceStore(),
		mirror:       defaultResourceMutationMirror(),
		runtime:      defaultRuntimeLookup(),
		activeTarget: defaultActiveTargetLookup(),
		tmuxRunner:   inttmux.ExecRunner{},
		lookupEnv:    os.Getenv,
	}
}

// withoutActiveTarget disables contextual fallback and managed-root narrowing
// for callers whose ambient target does not identify the requester's scope.
func (c *renameCommand) withoutActiveTarget() {
	if c != nil {
		c.activeTarget = nil
	}
}

// Run dispatches one `rename <kind>` invocation.
func (c *renameCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("rename requires a resource kind: %s", strings.Join(renameKinds, ", ")))
	}
	token, ok := cli.CanonicalChildToken("rename", args[0])
	if !ok {
		return usageError(fmt.Sprintf("rename %s is not available; this release implements: %s",
			args[0], strings.Join(renameKinds, ", ")))
	}
	kind, ok := resourceKindTokens[token]
	if !ok {
		return usageError(fmt.Sprintf("rename %s is not available; this release implements: %s",
			args[0], strings.Join(renameKinds, ", ")))
	}
	return c.runKind(token, kind, args[1:], stdout, stderr)
}

func (c *renameCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "rename " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	flags := resourceQueryFlags{kind: kind, active: c.activeTarget, runtime: c.runtime}
	// Generic descendant renames resolve an explicit name inside the exact root
	// that owns the active Window. That root may be a Project or a
	// ControlSession; the latter remains invocation-derived and is not added to
	// the public selector grammar. A Project rename has no enclosing root.
	flags.managedRootNamespaceScope = kind == coremetadata.KindWindow || kind == coremetadata.KindPane || kind == coremetadata.KindAgent
	flags.register(fs)
	name := fs.String("name", "", "the new Projmux metadata.name")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(refs) > 1 {
		return usageError(fmt.Sprintf("%s accepts at most one resource reference; got %q", spelling, refs[1]))
	}
	for _, ref := range refs {
		flags.addPositionalRef(ref)
	}
	if strings.TrimSpace(*name) == "" {
		return usageError(spelling + " requires --name <name>")
	}

	mode, field, err := resolveOutputMode(spelling, flags.output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbRename, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	uid := resolution.Matches[0].UID

	_, notice, err := c.commitRename(context.Background(), kind, uid, *name, nil)
	if err != nil {
		return err
	}
	if notice != "" {
		if _, err := fmt.Fprintln(stderr, notice); err != nil {
			return err
		}
	}

	renamed, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]
	match.Name = *name
	receipt := renameReceipt(kind, uid, *name)
	if err := receipt.Validate(); err != nil {
		return err
	}
	if mode == cli.OutputModeReceipt {
		return receipt.WriteJSON(stdout)
	}
	// A singular result renders no elapsed time, so it passes no clock.
	if err := writeResourceProjection(stdout, spelling, mode, kind, []selector.Match{match}, renamed, false, time.Time{}); err != nil {
		return err
	}
	if mode != cli.OutputModeDefault {
		return nil
	}
	return receipt.WriteHuman(stdout)
}

// renameOriginGuard re-proves, under the Registry lock of one rename, the
// runtime evidence a caller resolved its target from. It reads the working
// Registry and never writes; a refusal leaves the Registry untouched.
type renameOriginGuard = mutations.RenameOriginGuard

func (c *renameCommand) commitRename(ctx context.Context, kind coremetadata.Kind, uid, name string, guard renameOriginGuard) (bool, string, error) {
	committed, notice, err := c.renameService().CommitRename(ctx, kind, uid, name, guard)
	if !committed {
		err = MapMetadataError(err)
	}
	return committed, notice, err
}

// generatedRenameName applies the input rules of the generated rename routes to
// one raw prompt response.
//
// A blank or whitespace-only response is no request at all: requested is false
// and the caller writes nothing, not even a cleared label. Any other response
// must already be a valid name byte for byte. It is never trimmed or repaired
// into a different name behind the operator's back; the refusal carries the
// validation reason and, when one exists, the usable spelling SanitizeNameBase
// derives from the response, so the operator can type that instead.
func generatedRenameName(response string) (name string, requested bool, err error) {
	if strings.TrimSpace(response) == "" {
		return "", false, nil
	}
	if err := coremetadata.ValidateName(response); err != nil {
		reason := strings.TrimSpace(err.Error())
		if usable := coremetadata.SanitizeNameBase(response); usable != "" {
			return "", true, usageError(fmt.Sprintf("usable name %q; %s; nothing was changed", usable, reason))
		}
		return "", true, usageError(reason + "; nothing was changed")
	}
	return response, true, nil
}

// renameReceipt projects one committed rename.
//
// Only the address axis moves, and that is the contract rather than an
// observation: a rename keeps the uid, keeps the owner edge, keeps the desired
// state, and keeps the runtime exactly where it was. The previous spelling is
// deliberately absent -- the receipt schema records what an operation did to
// each resource, and a second name field would be the beginning of the stored
// second address this track just finished removing.
func renameReceipt(kind coremetadata.Kind, uid, name string) cli.OperationReceipt {
	return cli.OperationReceipt(mutations.RenameReceipt(kind, uid, name))
}
func windowDisplayNotConvergedNotice(uid, name string) string {
	return mutations.WindowDisplayNotConvergedNotice(uid, name)
}
func (c *renameCommand) renameService() mutations.Renamer {
	return mutations.Renamer{Store: projectMutationStore(c.store), Load: func() (coremetadata.Registry, error) { r, e := c.store.load(); return r, MapMetadataError(e) }, Mirror: c.mirror,
		ResolveWindowRoute: func(ctx context.Context) (mutations.WindowRenameRoute, error) {
			if c.tmuxRunner == nil {
				return mutations.WindowRenameRoute{}, mutations.ErrWindowRunnerNotConfigured
			}
			route, err := resolveExactObjectRuntimeMutationRoute(ctx, c.tmuxRunner, c.lookupEnv)
			if err != nil {
				return mutations.WindowRenameRoute{}, err
			}
			routed := explicitTmuxRunner{Runner: c.tmuxRunner, Target: tmuxTransport{Kind: tmuxSocketPath, Value: route.ExpectedSocketPath, Source: tmuxSocketPathSource}}
			actionFor := func(s mutations.WindowRenameStep, order int) plannedRuntimeMutation {
				target := runtimeMutationTarget{Kind: string(runtimeWindow), ID: s.WindowID, UID: s.UID, Parent: s.Parent}
				bindRuntimeMutationRouteTarget(&target, route)
				verb := mutationWriteStableName
				if s.Verb == "rename-window" {
					verb = mutationRenameWindow
				}
				action := newRuntimeMutation(order, verb, target)
				bindRuntimeMutationGuard(&action, "exact Window="+s.WindowID+";root="+strings.Join(strings.Split(s.Parent, "/")[:2], "/"))
				action.Operands = s.Operands
				return action
			}
			return mutations.WindowRenameRoute{SocketPath: route.ExpectedSocketPath, Runner: routed,
				Check: func(ctx context.Context, s mutations.WindowRenameStep) error {
					return guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, route, actionFor(s, 1))
				},
				Execute: func(ctx context.Context, steps []mutations.WindowRenameStep) error {
					converted := make([]runtimeMutationStep, 0, len(steps))
					for i, s := range steps {
						action := actionFor(s, i+1)
						guard := s.Guard
						converted = append(converted, runtimeMutationStep{Action: action, TargetRouteGuard: s.TargetRouteGuard, Guard: func(ctx context.Context) error {
							e := guard(ctx)
							if errors.Is(e, mutations.ErrEffectConverged) {
								return errRuntimeMutationEffectConverged
							}
							return e
						}, Reobserve: s.Reobserve, Apply: func(ctx context.Context) error { _, e := runRuntimeMutationCommand(ctx, routed, action); return e }})
					}
					return executeRuntimeMutationPlan(ctx, converted)
				}}, nil
		}}
}
