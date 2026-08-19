package app

import (
	"context"
	"errors"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The control-session convergence pass: the writer half of the control marker
// and of Home's Window/Pane identity mirror.
//
// The reading half already existed. resourcegraph.ControlSessionRole,
// Session.isControlSession, and the ClassControl branch of the resolver all read
// `@projmux_session_role`, and tmuxopts.SessionRole says in as many words that
// the marker's writer and lifecycle belong to the control-session surface. This
// is that surface. Until it existed the option had no producer at all: pane %0 of
// Home carried no `@projmux_window_uid`, so no owner chain could be derived from
// it and every route that resolves "the active target" refused inside Home.
//
// Four properties are contractual, and each is a decision rather than an
// implementation detail:
//
//  1. It runs from the canonical `projmux shell` lifecycle only, and only for the
//     app-session target. A session whose ownership goes to a Project target is
//     not a control session and never gets the marker; see resolveShellTarget's
//     ProjectDefault flag. No read verb reaches this code, so no read verb can
//     write the marker.
//  2. Both facts the reader requires are verified before anything is written:
//     the server carries `@projmux_app=1`, and the exact session does not carry
//     `@projmux_ephemeral=1`. A server projmux did not start gets no marker,
//     because a marker no reader will trust is just litter left on someone
//     else's tmux; an ephemeral session gets none either, because ephemeral
//     grants nothing and the resolver fails closed on the pair -- so the writer
//     must never be what produces it.
//  3. Every tmux call is routed through the one explicit `-L <socket>` target the
//     invocation was given. There is no unprefixed tmux call here, so this pass
//     can never mark a session on the default server or on a sibling socket, and
//     every write names one `-t <session>` target rather than a pattern or `-g`.
//  4. It converges. A brand-new Home and an already-live Home take the same path,
//     the registry commit goes through the store's convergent writer, and a
//     second pass over an already-converged Home performs zero registry byte
//     writes and re-writes only tmux options whose value it would set to what
//     they already hold.

// controlSessionSkip states why a convergence pass declined to do anything.
//
// A skip is not an error and must never fail the lifecycle entry it runs inside:
// `projmux shell` exists to give the operator a shell, and a server projmux does
// not own is a perfectly ordinary thing to refuse to mark. The reason is carried
// so a diagnostic can say which refusal fired instead of reporting silence.
type controlSessionSkip string

const (
	// controlSessionSkipNotAppOwned is a server carrying no @projmux_app=1.
	controlSessionSkipNotAppOwned controlSessionSkip = "the tmux server is not app-owned (@projmux_app is not 1)"
	// controlSessionSkipEphemeral is a session carrying @projmux_ephemeral=1.
	// Ephemeral grants nothing, and control plus ephemeral is the pair the
	// resolved graph fails closed on.
	controlSessionSkipEphemeral controlSessionSkip = "the session is marked ephemeral (@projmux_ephemeral=1), which grants nothing"
)

// controlSessionConvergence reports what one pass did.
type controlSessionConvergence struct {
	// skipped states why the pass declined, empty when it ran.
	skipped controlSessionSkip
	// controlUID is the ControlSession the pass settled on, empty when skipped.
	controlUID string
	// changed reports whether the registry commit wrote bytes. A converged
	// repeat pass reports false, which is the idempotence property.
	changed bool
	// windows and panes count the objects the pass bound, created or not.
	windows int
	panes   int
}

// controlSessionConverger converges one app-owned control session.
type controlSessionConverger struct {
	// runner is the raw tmux subprocess seam. Every call it makes is wrapped in
	// the explicit-socket router below.
	runner tmuxCommandRunner
	// resources is the registry seam. Writes go through its convergent writer so
	// an already-converged pass leaves registry.json byte-identical.
	resources *resourceStore
	// shell is the configured shell path; its basename seeds a minted Pane name
	// exactly as it does for every other registry-backed creation path.
	shell string
	// newOperationID labels the transaction ledger. Injectable so a test can
	// pin the label.
	newOperationID func() (string, error)
}

func newControlSessionConverger(runner tmuxCommandRunner, shell string) *controlSessionConverger {
	return &controlSessionConverger{
		runner:         runner,
		resources:      newResourceStore(),
		shell:          shell,
		newOperationID: newCreateOperationID,
	}
}

// converge brings one control session's Registry identity and tmux markers into
// agreement.
//
// The order is load bearing. The registry transaction commits first, and only
// then are the tmux options written: tmux options are not transactional, so a
// pass that wrote them first and then failed validation would leave uids on the
// machine that no resource backs. Committing first can leave the opposite
// mismatch -- a resource whose tmux object carries no uid yet -- and that one
// repairs itself, because the next pass sees an unmirrored live window and adopts
// the existing unbound Window instead of minting a second one.
func (c *controlSessionConverger) converge(ctx context.Context, socketName, sessionName string) (controlSessionConvergence, error) {
	if c == nil || c.runner == nil {
		return controlSessionConvergence{}, errors.New("control session convergence requires a tmux runner")
	}
	target, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	mirror := intmetadata.NewMirror(explicitTmuxRunner{runner: c.runner, target: target})

	markers, err := mirror.ObserveControlSessionMarkers(ctx, sessionName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	switch {
	case !markers.AppOwned:
		return controlSessionConvergence{skipped: controlSessionSkipNotAppOwned}, nil
	case markers.Ephemeral:
		return controlSessionConvergence{skipped: controlSessionSkipEphemeral}, nil
	}

	observed, targets, err := mirror.ObserveControlSession(ctx, sessionName)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	runtime, err := observeMirroredUIDs(ctx, mirror)
	if err != nil {
		return controlSessionConvergence{}, err
	}
	operationID, err := c.operationID()
	if err != nil {
		return controlSessionConvergence{}, err
	}

	var binding coremetadata.ControlSessionBinding
	var registry coremetadata.Registry
	changed, err := c.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		// The matcher is built inside the callback because the convergent writer
		// may run it more than once, and a matcher carries the claims of exactly
		// one pass. Reusing one across attempts would leave every candidate
		// claimed and turn the second attempt into a mint-everything pass.
		binder := coremetadata.NewBindingMatcher(runtime)
		result, err := mutator.BindControlSession(working, observed, c.shell, operationID, binder)
		if err != nil {
			return err
		}
		binding = result
		registry = working.Clone()
		return nil
	})
	if err != nil {
		return controlSessionConvergence{}, err
	}

	if err := mirror.MirrorControlSessionRole(ctx, sessionName); err != nil {
		return controlSessionConvergence{}, err
	}
	if err := mirrorControlSessionIdentity(ctx, mirror, registry, binding, targets); err != nil {
		return controlSessionConvergence{}, err
	}
	return controlSessionConvergence{
		controlUID: binding.ControlSession.Metadata.UID,
		changed:    changed,
		windows:    len(binding.Windows),
		panes:      len(binding.Panes),
	}, nil
}

func (c *controlSessionConverger) operationID() (string, error) {
	if c.newOperationID == nil {
		return "op-control-session", nil
	}
	return c.newOperationID()
}

// observeMirroredUIDs reads the live Window and Pane uid inventory the adoption
// matcher needs.
//
// It is taken before anything is written, which is what lets adoption refuse to
// steal a uid that is already the binding of some other live tmux object. An
// empty observation is the fail-closed reading: it can never invent a binding,
// only decline to protect one that no longer exists.
func observeMirroredUIDs(ctx context.Context, mirror intmetadata.Mirror) (coremetadata.RuntimeObservation, error) {
	windows, err := mirror.LiveWindowUIDs(ctx)
	if err != nil {
		return coremetadata.RuntimeObservation{}, err
	}
	panes, err := mirror.LivePaneUIDs(ctx)
	if err != nil {
		return coremetadata.RuntimeObservation{}, err
	}
	return coremetadata.RuntimeObservation{Windows: windows, Panes: panes}, nil
}

// mirrorControlSessionIdentity writes the uids a control-session bind settled on
// back onto exactly the tmux objects it bound.
//
// It is the mirrorImported contract applied to the control session, including its
// skip: a rebound object already carries the exact registry uid, so re-writing it
// would spend tmux calls to change nothing. Created and adopted objects go
// through the same MirrorWindow / MirrorPane calls every other managed path uses,
// so a control-session Window gets the same `automatic-rename off` and the same
// name projections a Project's Window does -- there is deliberately no second
// writer with weaker guarantees.
func mirrorControlSessionIdentity(
	ctx context.Context,
	mirror intmetadata.Mirror,
	registry coremetadata.Registry,
	binding coremetadata.ControlSessionBinding,
	targets intmetadata.LegacyTargets,
) error {
	for _, bound := range binding.Windows {
		if bound.Origin == coremetadata.ImportRebound {
			continue
		}
		if bound.SourceIndex < 0 || bound.SourceIndex >= len(targets.Windows) {
			continue
		}
		window, ok := registry.Window(bound.UID)
		if !ok {
			continue
		}
		if err := mirror.MirrorWindow(ctx, targets.Windows[bound.SourceIndex], *window); err != nil {
			return err
		}
	}
	for _, bound := range binding.Panes {
		if bound.Origin == coremetadata.ImportRebound {
			continue
		}
		if bound.WindowIndex < 0 || bound.WindowIndex >= len(targets.Panes) {
			continue
		}
		row := targets.Panes[bound.WindowIndex]
		if bound.PaneIndex < 0 || bound.PaneIndex >= len(row) {
			continue
		}
		pane, ok := registry.Pane(bound.UID)
		if !ok {
			continue
		}
		if err := mirror.MirrorPane(ctx, row[bound.PaneIndex], *pane); err != nil {
			return err
		}
	}
	return nil
}

// controlSessionWarning renders the one-line diagnostic a failed convergence
// prints. The lifecycle entry continues either way: `projmux shell` owes the
// operator a shell, and an unmarked control session degrades to exactly the
// pre-Phase-0 behavior rather than to a broken terminal.
func controlSessionWarning(sessionName string, err error) string {
	return fmt.Sprintf("warning: converge control session %q: %v\n", sessionName, err)
}
