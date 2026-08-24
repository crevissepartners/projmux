package metadata

import (
	"strings"
	"time"
)

// Binding the app-owned control session -- the Home session `projmux shell`
// opens -- to Registry resources.
//
// Everything here is the Project import path's structure with exactly one thing
// removed: the root. ImportLegacySession resolves its Project through
// `@projmux_project_path`, validates that path is an existing directory, and
// uses it as the default Pane cwd. A control session has no path to resolve,
// validate, or default from, so this file resolves its root resource through the
// exact tmux session name instead and lets a Pane carry whatever cwd the live
// pane reported.
//
// Two other differences are deliberate refusals rather than omissions:
//
//   - No Agent is ever minted here. A live pane inside Home may well carry
//     `@projmux_ai_agent` -- an operator can start `claude` in a plain shell --
//     but an Agent resource is a *resumable* identity, and nothing in this phase
//     can materialize a resume below a control session. Recording one would
//     advertise a capability that does not exist. Such a pane is bound as an
//     ordinary shell Pane, which is what it structurally is.
//   - No Project is ever created, reused, or consulted. $HOME never becomes a
//     Project root, and the type this file writes has no field that could hold
//     one.

// ControlSessionObservation is one live observation of an app-owned control
// session: the exact session name plus its windows and panes in tmux order.
//
// It is a value, not a reader, for the same reason resourcegraph.Inventory is:
// the binding decision stays pure and a test can state a machine state directly
// instead of scripting tmux output.
//
// The caller is responsible for having proved the session is a control session
// before it builds one of these. Two facts decide that and neither is
// observable from this struct: the server carries `@projmux_app=1`, and the
// exact session's `@projmux_session_role` is exactly `control`. See
// internal/app/control_session.go for the writer-side guard and
// internal/core/resourcegraph for the reader-side one.
type ControlSessionObservation struct {
	// Session is the exact tmux session name. It is the control session's
	// identity, matched verbatim, never by basename or cwd.
	Session string
	// Windows are the session's live windows in window_index ascending order,
	// which is the ordinal adoption aligns against.
	Windows []ControlSessionWindow
}

// ControlSessionWindow is one observed window of a control session.
type ControlSessionWindow struct {
	// DisplayName is the tmux window_name. It is projected onto the
	// duplicate-allowed metadata.displayName and is never a name seed.
	DisplayName      string
	RuntimeSessionID string
	RuntimeID        string
	// UID is the `@projmux_window_uid` the live window already carries, empty
	// when it carries none.
	UID string
	// Panes are the window's live panes in pane order.
	Panes []ControlSessionPane
}

// ControlSessionPane is one observed pane of a control session window.
type ControlSessionPane struct {
	// UID is the `@projmux_pane_uid` the live pane already carries, empty when
	// it carries none.
	UID string
	// Name is the `@projmux_pane_label` mirror, the highest-priority name seed
	// for a minted Pane.
	Name string
	// Command is `pane_current_command`, the one-time name-derivation source.
	Command string
	// Title is `pane_title`, a derived display source only.
	Title string
	// CWD is `pane_current_path`. It is recorded verbatim; a control session has
	// no root to fall back to, so an unreadable cwd stays empty rather than
	// borrowing $HOME.
	CWD string
}

// ControlSessionBinding is the outcome of one control-session bind.
//
// Windows and Panes report every object that was bound to a live tmux object,
// created or not, because the adapter owes all of them a mirror write. Created
// reports only what the transaction minted, so a rollback removes exactly what
// this operation brought into existence and never an adopted object that
// predates it. Both properties are the ImportResult contract, restated here so
// the two paths can never drift into different adapter expectations.
type ControlSessionBinding struct {
	ControlSession ControlSession
	// Reused is true when the exact session name already had a ControlSession.
	Reused      bool
	Windows     []ImportedWindow
	Panes       []ImportedPane
	OperationID string
	Created     []string
}

// BindControlSession converts one observed app-owned control session into
// Registry resources and reattaches the ones that already exist.
//
// It is convergent by construction, which is what makes it safe to run on every
// `projmux shell` entry:
//
//   - The exact session name reuses the same ControlSession uid. Nothing else
//     merges uids, and no observation ever renames or re-roots an existing one.
//   - Windows and Panes resolve through the shared adoption matcher, so a live
//     object that still carries a known uid is rebound, an unmirrored live object
//     adopts the next unbound Registry object of the same owner in creation
//     order, and only a genuinely surplus live object mints a new resource. A
//     mirror write that failed halfway through a previous pass therefore repairs
//     itself instead of duplicating a Window on the next pass.
//   - Nothing is deleted, pruned, renamed, or re-identified. A Window whose tmux
//     window is gone keeps its uid and its name reservation exactly as a
//     MissingRoot Project does.
//
// binder carries the adoption decision across a whole reconciliation pass so one
// Registry Window is never handed to two live tmux windows. A nil binder gets a
// private one over an empty observation, which is the correct reading for a
// caller binding a single session in isolation.
func (m Mutator) BindControlSession(reg *Registry, observed ControlSessionObservation, defaultShell, operationID string, binder *BindingMatcher) (ControlSessionBinding, error) {
	const op = "bind control session"

	session := strings.TrimSpace(observed.Session)
	if session == "" {
		return ControlSessionBinding{}, inputErr(op, ErrInvalidName, "control session must name a tmux session")
	}
	if binder == nil {
		binder = NewBindingMatcher(RuntimeObservation{})
	}

	now := m.clock()().UTC()
	txn := m.Begin(reg, operationID)
	result, err := m.bindControlSessionTx(txn, reg, op, session, observed, defaultShell, now, binder)
	if err != nil {
		txn.Rollback()
		return ControlSessionBinding{}, err
	}
	result.Created = txn.Created()
	result.OperationID = txn.ID()
	txn.Commit()
	reg.UpdatedAt = now
	return result, nil
}

func (m Mutator) bindControlSessionTx(txn *Transaction, reg *Registry, op, session string, observed ControlSessionObservation, defaultShell string, now time.Time, binder *BindingMatcher) (ControlSessionBinding, error) {
	result := ControlSessionBinding{}

	var controlUID string
	if existing, ok := reg.ControlSessionBySession(session); ok {
		controlUID = existing.Metadata.UID
		result.Reused = true
	} else {
		uid, err := m.mintUID(KindControlSession)
		if err != nil {
			return ControlSessionBinding{}, err
		}
		// Automatic, not explicit: a session name that collides with an existing
		// ControlSession name takes the lowest free suffix rather than failing.
		// `projmux shell` is the app's own entrypoint and must not become
		// unusable because of a name the Registry already holds.
		name, err := reg.allocateName(op, "", KindControlSession, ControlSessionNameBase(session), uid)
		if err != nil {
			return ControlSessionBinding{}, err
		}
		reg.ControlSessions = append(reg.ControlSessions, ControlSession{
			APIVersion: APIVersion,
			Kind:       KindControlSession,
			Metadata: ObjectMeta{
				UID:       uid,
				Name:      name,
				CreatedAt: now,
			},
			Spec: ControlSessionSpec{Session: session},
		})
		txn.record(KindControlSession, uid)
		controlUID = uid
	}

	for index, window := range observed.Windows {
		if err := m.bindControlWindowTx(txn, reg, op, controlUID, defaultShell, index, window, now, &result, binder); err != nil {
			return ControlSessionBinding{}, err
		}
	}

	control, _ := reg.ControlSession(controlUID)
	result.ControlSession = control.Clone()
	return result, nil
}

// bindControlWindowTx binds one observed window to a Registry Window owned by
// the control session, then binds that window's panes.
//
// The four outcomes are the legacy import's four, and for the same reasons:
// rebound, adopted, created, or refused. A refusal -- a live window carrying a
// uid that exists and belongs to somebody else -- contributes none of its panes
// either, because a pane can only be paired inside a Window that was itself
// paired.
func (m Mutator) bindControlWindowTx(txn *Transaction, reg *Registry, op, controlUID, defaultShell string, index int, observed ControlSessionWindow, now time.Time, result *ControlSessionBinding, binder *BindingMatcher) error {
	match := binder.MatchWindow(reg, controlUID, observed.UID)
	if match.Kind == AdoptionRefused {
		return nil
	}

	windowUID := match.UID
	origin := ImportAdopted
	if match.Kind == AdoptionRebind {
		origin = ImportRebound
	}
	panes := observed.Panes

	if match.Kind == AdoptionUnmatched || match.Kind == AdoptionForeign {
		origin = ImportCreated
		uid, err := m.mintUID(KindWindow)
		if err != nil {
			return err
		}
		// Nothing observed from tmux seeds the stable name, exactly as
		// LegacyWindowNameSeed refuses to: the observed window_name is projected
		// separately onto the duplicate-allowed displayName.
		name, err := reg.allocateName(op, controlUID, KindWindow, FallbackWindowNameBase, uid)
		if err != nil {
			return err
		}
		reg.Windows = append(reg.Windows, Window{
			APIVersion: APIVersion,
			Kind:       KindWindow,
			Metadata: ObjectMeta{
				UID:         uid,
				Name:        name,
				DisplayName: observed.DisplayName,
				OwnerRef:    &OwnerRef{Kind: KindControlSession, UID: controlUID},
				CreatedAt:   now,
			},
		})
		txn.record(KindWindow, uid)
		binder.Claim(uid)
		windowUID = uid
		// A minted Window must end up with a primary Pane, so an observation
		// with no panes at all still materializes one. An adopted Window keeps
		// the topology it already has.
		if len(panes) == 0 {
			panes = []ControlSessionPane{{}}
		}
	}

	window, ok := reg.Window(windowUID)
	if !ok {
		return nil
	}
	window.Status.RuntimeSessionID = strings.TrimSpace(observed.RuntimeSessionID)
	window.Status.RuntimeID = strings.TrimSpace(observed.RuntimeID)
	result.Windows = append(result.Windows, ImportedWindow{
		UID:                     windowUID,
		Name:                    window.Metadata.Name,
		SourceIndex:             index,
		NeedsAutomaticRenameOff: true,
		Origin:                  origin,
	})

	shellPaneRef := ""
	for paneIndex, observedPane := range panes {
		paneUID, err := m.bindControlPaneTx(txn, reg, op, windowUID, defaultShell, index, paneIndex, observedPane, now, result, binder)
		if err != nil {
			return err
		}
		if paneUID != "" && shellPaneRef == "" {
			shellPaneRef = paneUID
		}
	}

	// Only ever fills a gap. An adopted Window already names its shell anchor,
	// and overwriting that from a tmux pane order the operator may have
	// rearranged would be a rename by another route.
	stored, _ := reg.Window(windowUID)
	if strings.TrimSpace(stored.Spec.AnchorPaneRef) == "" {
		stored.Spec.AnchorPaneRef = shellPaneRef
		stored.Spec.DefaultShellPaneRef = shellPaneRef
	}
	if _, err := m.ObserveWindowDisplayName(reg, windowUID, observed.DisplayName); err != nil {
		return err
	}
	return nil
}

// bindControlPaneTx binds one observed pane inside an already-bound Window and
// returns the Registry Pane uid it settled on, empty when it refused.
//
// Every pane is a shell Pane. See the package note above for why no Agent is
// minted below a control session in this slice.
func (m Mutator) bindControlPaneTx(txn *Transaction, reg *Registry, op, windowUID, defaultShell string, windowIndex, paneIndex int, observed ControlSessionPane, now time.Time, result *ControlSessionBinding, binder *BindingMatcher) (string, error) {
	match := binder.MatchPane(reg, windowUID, observed.UID)
	if match.Kind == AdoptionRefused {
		return "", nil
	}
	// AdoptionForeign falls through to the create path below, for the same
	// reason the Window branch does: a uid nothing knows is never adopted, but
	// leaving the pane unmanaged forever is worse than minting a Pane for it.
	if match.Matched() {
		pane, ok := reg.Pane(match.UID)
		if !ok {
			return "", nil
		}
		origin := ImportAdopted
		if match.Kind == AdoptionRebind {
			origin = ImportRebound
		}
		result.Panes = append(result.Panes, ImportedPane{
			UID:         pane.Metadata.UID,
			Name:        pane.Metadata.Name,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
			Origin:      origin,
		})
		return pane.Metadata.UID, nil
	}

	nameBase := PaneNameBase(observed.Command, defaultShell)
	if base := SanitizeNameBase(observed.Name); base != "" {
		nameBase = base
	}
	pane, err := m.addPaneTx(txn, reg, op, windowUID, KindWindow, PaneRoleShell, "", nameBase, observed.Command, strings.TrimSpace(observed.CWD), nil, now)
	if err != nil {
		return "", err
	}
	pane.Status.DisplayTitle = DerivePaneDisplayTitle("", "", observed.Command, observed.Title)
	reg.storePaneStatus(pane.Metadata.UID, pane.Status)
	binder.Claim(pane.Metadata.UID)
	result.Panes = append(result.Panes, ImportedPane{
		UID:         pane.Metadata.UID,
		Name:        pane.Metadata.Name,
		WindowIndex: windowIndex,
		PaneIndex:   paneIndex,
		Origin:      ImportCreated,
	})
	return pane.Metadata.UID, nil
}
