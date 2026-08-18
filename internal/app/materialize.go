package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// tmuxError renders a tmux subprocess failure as a plain error.
//
// The cause is deliberately not wrapped with %w. A failed tmux command carries
// an *exec.ExitError, and *exec.ExitError satisfies the `error + ExitCode() int`
// interface cmd/projmux uses to let a command pick its own exit code -- which
// also suppresses main's default stderr print, because a command that chose its
// own code is expected to have printed its own diagnostic. Propagating that
// wrap would turn every tmux failure on this path into a silent exit 1.
func tmuxError(format string, args ...any) error {
	// fmt.Errorf without a %w verb returns a plain error, which is exactly the
	// point: the cause's text is preserved, its identity is not.
	return fmt.Errorf(format, args...)
}

// tmuxCommandRunner is the narrow subprocess seam the materializer shares with
// the resource metadata mirror. Production wires the same ExecRunner into both,
// so a test can replace one object and observe every tmux call the operation
// makes.
type tmuxCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// sessionMaterializer is the persistent-session half of the runtime. It is the
// existing tmux client, deliberately: `EnsureSession` already owns the public
// pre-create/post-create hook contract (`PROJMUX_SESSION`, `PROJMUX_CWD`,
// `PROJMUX_SESSION_KIND`, `PROJMUX_SOCKET`, `PROJMUX_PANE`, `PROJMUX_VERSION`),
// creates the session detached, and never moves a client. Reusing it is what
// keeps this Phase from inventing a second hook surface.
type sessionMaterializer interface {
	SessionExists(ctx context.Context, sessionName string) (bool, error)
	EnsureSession(ctx context.Context, sessionName, cwd string) error
}

type operationSessionMaterializer interface {
	EnsureSessionWithEnvironmentResult(ctx context.Context, sessionName, cwd string, env map[string]string) (intmux.NewSessionResult, error)
}

type sessionStartupFinalizer interface {
	FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd, operationMarker string) error
}

func (m *materializer) finalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd string, ledger *runtimeLedger) error {
	if !result.Created {
		return nil
	}
	finalizer, ok := m.sessions.(sessionStartupFinalizer)
	if !ok {
		return errors.New("materialize tmux session: startup finalization is unavailable")
	}
	if err := finalizer.FinalizeSessionStartup(ctx, result, sessionName, cwd, ledger.operationMarker); err != nil {
		return tmuxError("finalize tmux session %q startup: %v", sessionName, err)
	}
	return nil
}

type liveSessionIdentity struct {
	ID   string
	Name string
	UID  string
	Root string
}

// runtimeObjectKind names one class of tmux object an operation can create.
type runtimeObjectKind string

const (
	runtimeSession runtimeObjectKind = "session"
	runtimeWindow  runtimeObjectKind = "window"
	runtimePane    runtimeObjectKind = "pane"
)

// runtimeObject is one ledger entry: a tmux object this operation created,
// pinned by its stable tmux id and by the Projmux uid mirrored onto it.
type runtimeObject struct {
	Kind runtimeObjectKind
	// ID is the stable tmux handle ($N, @N, %N). Indexes are deliberately not
	// used: they shift when a sibling is created or destroyed.
	ID string
	// UID is the Projmux uid mirrored onto ID at creation time.
	UID string
}

// runtimeLedger records the tmux objects one operation created so a later
// failure can undo exactly them.
//
// Rollback is ownership checked, not best effort: an entry is only removed when
// the tmux object still carries the same Projmux uid this operation mirrored
// onto it. A pre-existing session, a window another operation created, and an
// id that tmux has since recycled are all left alone.
type runtimeLedger struct {
	created          []runtimeObject
	operationMarker  string
	markedSessionIDs []string
}

func newRuntimeLedger(operationID string) *runtimeLedger {
	return &runtimeLedger{operationMarker: newCreateOperationMarker(operationID)}
}

func (l *runtimeLedger) record(kind runtimeObjectKind, id, uid string) {
	if l == nil || strings.TrimSpace(id) == "" {
		return
	}
	l.created = append(l.created, runtimeObject{Kind: kind, ID: id, UID: uid})
}

// entries returns the ledger in creation order.
func (l *runtimeLedger) entries() []runtimeObject {
	if l == nil {
		return nil
	}
	return l.created
}

func (l *runtimeLedger) markSession(session string) {
	if l == nil || strings.TrimSpace(session) == "" {
		return
	}
	if slices.Contains(l.markedSessionIDs, session) {
		return
	}
	l.markedSessionIDs = append(l.markedSessionIDs, session)
}

// ownershipOption is the mirrored option that proves this operation owns a
// runtime object.
func (o runtimeObject) ownershipOption() string {
	switch o.Kind {
	case runtimeSession:
		return tmuxopts.ProjectUIDSession
	case runtimeWindow:
		return tmuxopts.WindowUID
	default:
		return tmuxopts.PaneUID
	}
}

func (o runtimeObject) killCommand() string {
	switch o.Kind {
	case runtimeSession:
		return "kill-session"
	case runtimeWindow:
		return "kill-window"
	default:
		return "kill-pane"
	}
}

// materializer turns offline Project/Window/Pane metadata into detached tmux
// objects.
//
// Every command it issues is detached. It never calls switch-client,
// attach-session, select-window, or select-pane, and it never reads which
// client or pane is focused, so the operator's view is byte-identical before
// and after a create.
type materializer struct {
	runner   tmuxCommandRunner
	mirror   intmetadata.Mirror
	sessions sessionMaterializer
	// warn receives non-fatal rollback diagnostics. Progress and warnings are
	// stderr-only; stdout stays empty until the operation succeeds.
	warn io.Writer
}

func (m *materializer) read(ctx context.Context, args ...string) (string, error) {
	out, err := m.runner.Run(ctx, "tmux", args...)
	return strings.TrimSpace(string(out)), err
}

// option reads one tmux format value for a target, treating an unreadable
// target as an empty value rather than an error. Ownership checks want "this is
// not ours" for a target that has already disappeared.
func (m *materializer) option(ctx context.Context, target, format string) string {
	out, err := m.read(ctx, "display-message", "-p", "-t", target, "-F", format)
	if err != nil {
		return ""
	}
	return out
}

// rollback removes, in reverse creation order, only the tmux objects this
// operation created that still carry the uid it mirrored onto them.
func (m *materializer) rollback(ctx context.Context, ledger *runtimeLedger) {
	entries := ledger.entries()
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		got, err := m.read(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.ownershipOption()+"}")
		if err != nil {
			// The object is already gone, so the desired rollback state holds.
			continue
		}
		if got != entry.UID {
			// The object still exists but no longer belongs to this operation.
			// Preserve it and make the residual drift explicit.
			if m.warn != nil {
				fmt.Fprintf(m.warn, "projmux: rollback preserved %s %s because its ownership uid is %q, want %q\n",
					entry.Kind, entry.ID, got, entry.UID)
			}
			continue
		}
		if _, err := m.runner.Run(ctx, "tmux", entry.killCommand(), "-t", entry.ID); err != nil && m.warn != nil {
			fmt.Fprintf(m.warn, "projmux: rollback could not remove %s %s: %v\n", entry.Kind, entry.ID, err)
		}
	}
}

// ensureSession makes the Project's persistent tmux session live.
//
// A session that already exists is reused untouched, which is what keeps the
// pre-create/post-create hooks on their documented trigger: they fire when a
// session is created, and only then.
func (m *materializer) ensureSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	ledger *runtimeLedger,
) (intmux.NewSessionResult, error) {
	exists, err := m.sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return intmux.NewSessionResult{}, tmuxError("check tmux session %q: %v", sessionName, err)
	}
	if exists {
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, err
		}
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, nil
	}
	if identity, found, err := m.preflightSessionOwnership(ctx, project, sessionName); err != nil {
		return intmux.NewSessionResult{}, err
	} else if found {
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, nil
	}
	sessions, ok := m.sessions.(operationSessionMaterializer)
	if !ok {
		return intmux.NewSessionResult{}, errors.New("materialize tmux session: atomic session result is unavailable")
	}
	result, ensureErr := sessions.EnsureSessionWithEnvironmentResult(ctx, sessionName, project.Spec.Root, map[string]string{
		createOperationEnvironment: ledger.operationMarker,
	})
	if ensureErr != nil {
		// A pre-create hook refusal lands here with nothing created. A later
		// synchronous tmux hook can instead fail after new-session created the
		// session. The -e lease is then exact ownership evidence, so establish
		// the Project uid and ledger entry before surfacing the original error.
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		return intmux.NewSessionResult{}, tmuxError("materialize tmux session %q: %v", sessionName, ensureErr)
	}
	if !result.Created {
		// The outer check missed and the client's inner check hit. Treat that
		// session exactly like any other pre-existing runtime; never infer that
		// this operation created its first Window or Pane.
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, err
		}
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		result.SessionID = identity.ID
		return result, nil
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || exactTmuxHandle(result.WindowID, "@") == "" || exactTmuxHandle(result.PaneID, "%") == "" {
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		return intmux.NewSessionResult{}, fmt.Errorf("materialize tmux session %q: atomic result is incomplete", sessionName)
	}
	ledger.markSession(result.SessionID)
	if claimErr := m.claimRuntimeUIDForRollback(ctx, runtimeSession, result.SessionID, project.Metadata.UID, ledger); claimErr != nil {
		return intmux.NewSessionResult{}, claimErr
	}
	if err := m.mirror.MirrorProject(ctx, result.SessionID, project); err != nil {
		return intmux.NewSessionResult{}, err
	}
	return result, nil
}

// requireOwnedSession validates the complete server-wide ownership proof for
// one existing session before an operation lease or identity mirror is written.
func (m *materializer) requireOwnedSession(ctx context.Context, project coremetadata.Project, sessionName string) (liveSessionIdentity, error) {
	identity, found, err := m.preflightSessionOwnership(ctx, project, sessionName)
	if err != nil {
		return liveSessionIdentity{}, err
	}
	if !found {
		return liveSessionIdentity{}, fmt.Errorf("create: tmux session %q disappeared during ownership preflight", sessionName)
	}
	return identity, nil
}

// preflightSessionOwnership rejects duplicate Project UID/root claims even
// when the selected session is absent. An absent selected name is available
// only when no other live session already owns either identity edge.
func (m *materializer) preflightSessionOwnership(ctx context.Context, project coremetadata.Project, sessionName string) (liveSessionIdentity, bool, error) {
	identities, err := m.sessionIdentities(ctx)
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return liveSessionIdentity{}, false, nil
		}
		return liveSessionIdentity{}, false, err
	}
	wantRoot := candidates.CanonicalPath(project.Spec.Root)
	var named []liveSessionIdentity
	uidClaims, rootClaims := 0, 0
	for _, identity := range identities {
		if identity.Name == sessionName {
			named = append(named, identity)
		}
		if strings.TrimSpace(identity.UID) == project.Metadata.UID {
			uidClaims++
		}
		if root := candidates.CanonicalPath(identity.Root); root != "" && root == wantRoot {
			rootClaims++
		}
	}
	if len(named) != 1 {
		if len(named) == 0 && uidClaims == 0 && rootClaims == 0 {
			return liveSessionIdentity{}, false, nil
		}
		return liveSessionIdentity{}, false, fmt.Errorf("create: tmux session %q ownership is unavailable or ambiguous: found %d same-name sessions, uid claims=%d, root claims=%d", sessionName, len(named), uidClaims, rootClaims)
	}
	identity := named[0]
	gotRoot := candidates.CanonicalPath(identity.Root)
	if strings.TrimSpace(identity.UID) == "" || identity.UID != project.Metadata.UID || gotRoot == "" || gotRoot != wantRoot || uidClaims != 1 || rootClaims != 1 {
		return liveSessionIdentity{}, false, fmt.Errorf(
			"create: refuse foreign tmux session %q: project uid=%q root=%q, want unique uid=%q root=%q (uid claims=%d, root claims=%d)",
			sessionName, identity.UID, identity.Root, project.Metadata.UID, project.Spec.Root, uidClaims, rootClaims)
	}
	return identity, true, nil
}

// refuseUnregisteredSessionClaims is the read-only first-use variant of the
// ownership preflight. There is no durable Project UID to prove yet, so any
// same-name session or any session already claiming the discovered canonical
// root is foreign to this create and must be handled explicitly first.
func (m *materializer) refuseUnregisteredSessionClaims(ctx context.Context, sessionName, root string) error {
	identities, err := m.sessionIdentities(ctx)
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil
		}
		return err
	}
	wantRoot := candidates.CanonicalPath(root)
	for _, identity := range identities {
		if identity.Name == sessionName {
			return fmt.Errorf("create: refuse live tmux session %q for an unregistered Project; import or reconcile it explicitly before create", sessionName)
		}
		if wantRoot != "" && candidates.CanonicalPath(identity.Root) == wantRoot {
			return fmt.Errorf("create: refuse unregistered Project root %q because tmux session %q already claims it", root, identity.Name)
		}
	}
	return nil
}

func (m *materializer) sessionIdentities(ctx context.Context) ([]liveSessionIdentity, error) {
	out, err := m.read(ctx, "list-sessions", "-F", tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectPathSession+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil, err
		}
		return nil, tmuxError("list tmux session identities: %v", err)
	}
	rows, parseErr := strictTmuxRows(out, 4)
	if parseErr != nil {
		return nil, fmt.Errorf("list tmux session identities: %w", parseErr)
	}
	identities := make([]liveSessionIdentity, 0, len(rows))
	for _, fields := range rows {
		if exactTmuxHandle(fields[0], "$") == "" || strings.TrimSpace(fields[1]) == "" {
			return nil, fmt.Errorf("list tmux session identities: malformed session row")
		}
		identities = append(identities, liveSessionIdentity{ID: fields[0], Name: fields[1], UID: strings.TrimSpace(fields[2]), Root: strings.TrimSpace(fields[3])})
	}
	return identities, nil
}

func (m *materializer) claimRuntimeUID(ctx context.Context, kind runtimeObjectKind, target, uid string) (bool, error) {
	ownershipOption := runtimeObject{Kind: kind}.ownershipOption()
	args := []string{"set-option"}
	switch kind {
	case runtimeWindow:
		args = append(args, "-w")
	case runtimePane:
		args = append(args, "-p")
	}
	args = append(args, "-t", target, "-q", ownershipOption, uid)
	if _, err := m.runner.Run(ctx, "tmux", args...); err != nil {
		claimErr := tmuxError("claim created tmux %s %s: %v", kind, target, err)
		if got := m.option(ctx, target, "#{"+ownershipOption+"}"); got == uid {
			return true, claimErr
		}
		return false, claimErr
	}
	return true, nil
}

// claimRuntimeUIDForRollback establishes the UID before recording the object.
// A tmux command can report failure after applying the option, so an error is
// followed by an exact readback: a stuck claim enters the ledger and can be
// rolled back safely; an unstuck claim is preserved as an unowned residual.
func (m *materializer) claimRuntimeUIDForRollback(ctx context.Context, kind runtimeObjectKind, target, uid string, ledger *runtimeLedger) error {
	claimed, err := m.claimRuntimeUID(ctx, kind, target, uid)
	if claimed {
		ledger.record(kind, target, uid)
	}
	if err != nil && !claimed {
		m.warnUnclaimedHandle(kind, target)
	}
	return err
}

func (m *materializer) recordErrorCreatedSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	exactSessionID string,
	ledger *runtimeLedger,
) {
	if ledger == nil || strings.TrimSpace(ledger.operationMarker) == "" {
		return
	}
	target := exactTmuxHandle(exactSessionID, "$")
	if target == "" {
		exists, err := m.sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			return
		}
		target = sessionName
	}
	out, err := m.runner.Run(ctx, "tmux", "show-environment", "-t", target)
	if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.operationMarker {
		if exactTmuxHandle(exactSessionID, "$") != "" {
			m.warnUnclaimedHandle(runtimeSession, exactSessionID)
		}
		return
	}
	ledger.markSession(target)
	sessionID := exactTmuxHandle(exactSessionID, "$")
	if sessionID == "" {
		sessionID = m.option(ctx, target, "#{session_id}")
	}
	if sessionID == "" {
		if m.warn != nil {
			fmt.Fprintf(m.warn, "projmux: create failed with an owned but unidentifiable tmux session %s; preserved it\n", sessionName)
		}
		return
	}
	if claimErr := m.claimRuntimeUIDForRollback(ctx, runtimeSession, sessionID, project.Metadata.UID, ledger); claimErr != nil {
		return
	}
	_ = m.mirror.MirrorProject(ctx, sessionID, project)
}

func (m *materializer) markCreateOperation(ctx context.Context, sessionName string, ledger *runtimeLedger) error {
	if ledger == nil || strings.TrimSpace(ledger.operationMarker) == "" {
		return errors.New("materialize tmux session: create-operation lease is missing")
	}
	if _, err := m.runner.Run(ctx, "tmux", "set-environment", "-t", sessionName, createOperationEnvironment, ledger.operationMarker); err != nil {
		return tmuxError("mark tmux session %q for create operation: %v", sessionName, err)
	}
	ledger.markSession(sessionName)
	return nil
}

func (m *materializer) clearCreateOperations(ctx context.Context, ledger *runtimeLedger) {
	if ledger == nil {
		return
	}
	for _, sessionName := range ledger.markedSessionIDs {
		exists, err := m.sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			continue
		}
		out, err := m.runner.Run(ctx, "tmux", "show-environment", "-t", sessionName)
		if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.operationMarker {
			continue
		}
		if _, err := m.runner.Run(ctx, "tmux", "set-environment", "-u", "-t", sessionName, createOperationEnvironment); err != nil && m.warn != nil {
			fmt.Fprintf(m.warn, "projmux: could not clear create-operation lease for session %s: %v\n", sessionName, err)
		}
	}
}

// windowIDForUID returns the tmux window id inside sessionName that mirrors uid.
func (m *materializer) windowIDForUID(ctx context.Context, sessionName, uid string) (string, error) {
	if strings.TrimSpace(uid) == "" {
		return "", nil
	}
	out, err := m.read(ctx, "list-windows", "-t", sessionName, "-F",
		tmuxRowFormat("#{"+tmuxopts.WindowUID+"}", "#{window_id}"))
	if err != nil {
		return "", tmuxError("list tmux windows of session %q: %v", sessionName, err)
	}
	for _, fields := range splitTmuxRows(out, 2) {
		if fields[0] == uid {
			return fields[1], nil
		}
	}
	return "", nil
}

// panesOf lists the panes of a tmux window as (mirrored uid, pane id) rows in
// tmux order.
func (m *materializer) panesOf(ctx context.Context, windowID string) ([][2]string, error) {
	out, err := m.read(ctx, "list-panes", "-t", windowID, "-F",
		tmuxRowFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, tmuxError("list tmux panes of window %q: %v", windowID, err)
	}
	rows := splitTmuxRows(out, 2)
	panes := make([][2]string, 0, len(rows))
	for _, fields := range rows {
		panes = append(panes, [2]string{fields[0], fields[1]})
	}
	return panes, nil
}

type windowCreateResult struct {
	WindowID string
	PaneID   string
}

type runtimeOwner struct {
	SessionID string
	WindowID  string
}

type runtimeOwnerSet map[runtimeOwner]struct{}

type runtimeOwners map[string]runtimeOwnerSet

// newWindow creates one detached tmux Window and accepts it only when the
// composite output, global before/after inventory, and exact owner relation all
// identify the same sole new Window and sole new primary Pane.
func (m *materializer) newWindow(ctx context.Context, sessionID, name, cwd string, command []string) (windowCreateResult, error) {
	beforeWindows, beforePanes, beforeErr := m.runtimeOwners(ctx)
	if beforeErr != nil {
		return windowCreateResult{}, tmuxError("inventory tmux runtime before window create: %v", beforeErr)
	}
	args := []string{"new-window", "-d", "-P", "-F", tmuxRowFormat("#{window_id}", "#{pane_id}"), "-t", sessionID + ":"}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, command...)
	output, createErr := m.read(ctx, args...)
	afterWindows, afterPanes, inventoryErr := m.runtimeOwners(ctx)
	if inventoryErr != nil {
		m.warnCompositeWindowResult(output)
		inventoryFailure := tmuxError("inventory tmux runtime after window create: %v", inventoryErr)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(tmuxError("create tmux window in session %q: %v", sessionID, createErr), inventoryFailure)
		}
		return windowCreateResult{}, inventoryFailure
	}
	result, attributionErr := attributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
	if attributionErr != nil {
		m.warnUnclaimedOwners("window", beforeWindows, afterWindows)
		m.warnUnclaimedOwners("pane", beforePanes, afterPanes)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(tmuxError("create tmux window in session %q: %v", sessionID, createErr), attributionErr)
		}
		return windowCreateResult{}, attributionErr
	}
	if createErr != nil {
		return result, tmuxError("create tmux window in session %q: %v", sessionID, createErr)
	}
	return result, nil
}

func (m *materializer) runtimeOwners(ctx context.Context) (runtimeOwners, runtimeOwners, error) {
	windowsOut, err := m.read(ctx, "list-windows", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, nil, err
	}
	panesOut, err := m.read(ctx, "list-panes", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return nil, nil, err
	}
	windows := runtimeOwners{}
	windowRows, err := strictTmuxRows(windowsOut, 2)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Window owner inventory: %w", err)
	}
	for _, row := range windowRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" {
			return nil, nil, fmt.Errorf("malformed Window owner inventory")
		}
		owner := runtimeOwner{SessionID: row[0], WindowID: row[1]}
		if windows[row[1]] == nil {
			windows[row[1]] = runtimeOwnerSet{}
		}
		windows[row[1]][owner] = struct{}{}
	}
	panes := runtimeOwners{}
	paneRows, err := strictTmuxRows(panesOut, 3)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Pane owner inventory: %w", err)
	}
	for _, row := range paneRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" || exactTmuxHandle(row[2], "%") == "" {
			return nil, nil, fmt.Errorf("malformed Pane owner inventory")
		}
		owner := runtimeOwner{SessionID: row[0], WindowID: row[1]}
		if panes[row[2]] == nil {
			panes[row[2]] = runtimeOwnerSet{}
		}
		panes[row[2]][owner] = struct{}{}
	}
	return windows, panes, nil
}

func attributeCreatedWindow(
	output, sessionID string,
	beforeWindows, beforePanes, afterWindows, afterPanes runtimeOwners,
) (windowCreateResult, error) {
	rows := splitTmuxRows(output, 2)
	if len(rows) != 1 || exactTmuxHandle(rows[0][0], "@") == "" || exactTmuxHandle(rows[0][1], "%") == "" {
		return windowCreateResult{}, fmt.Errorf("create tmux window in session %q: malformed composite result %q", sessionID, output)
	}
	result := windowCreateResult{WindowID: rows[0][0], PaneID: rows[0][1]}
	newWindows := newRuntimeIDs(beforeWindows, afterWindows)
	newPanes := newRuntimeIDs(beforePanes, afterPanes)
	windowOwners, windowPresent := afterWindows[result.WindowID]
	paneOwners, panePresent := afterPanes[result.PaneID]
	wantWindowOwner := runtimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	wantPaneOwner := runtimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	if len(newWindows) != 1 || newWindows[0] != result.WindowID || len(newPanes) != 1 || newPanes[0] != result.PaneID ||
		!windowPresent || !windowOwners.contains(wantWindowOwner) || !panePresent || !paneOwners.contains(wantPaneOwner) ||
		!paneOwnerSetMatchesWindow(paneOwners, windowOwners, result.WindowID) {
		return windowCreateResult{}, fmt.Errorf(
			"create tmux window in session %q: attribution mismatch output=%s/%s new-windows=%v new-panes=%v window-owners=%v pane-owners=%v",
			sessionID, result.WindowID, result.PaneID, newWindows, newPanes, sortedRuntimeOwners(windowOwners), sortedRuntimeOwners(paneOwners))
	}
	return result, nil
}

func paneOwnerSetMatchesWindow(paneOwners, windowOwners runtimeOwnerSet, windowID string) bool {
	if len(paneOwners) == 0 {
		return false
	}
	for owner := range paneOwners {
		if owner.WindowID != windowID || !windowOwners.contains(owner) {
			return false
		}
	}
	return true
}

func (owners runtimeOwnerSet) contains(owner runtimeOwner) bool {
	_, ok := owners[owner]
	return ok
}

func sortedRuntimeOwners(owners runtimeOwnerSet) []string {
	values := make([]string, 0, len(owners))
	for owner := range owners {
		values = append(values, owner.SessionID+"/"+owner.WindowID)
	}
	slices.Sort(values)
	return values
}

func newRuntimeIDs(before, after runtimeOwners) []string {
	var ids []string
	for id := range after {
		if _, existed := before[id]; !existed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (m *materializer) warnUnclaimedOwners(kind string, before, after runtimeOwners) {
	if m.warn == nil {
		return
	}
	if residual := newRuntimeIDs(before, after); len(residual) > 0 {
		fmt.Fprintf(m.warn, "projmux: create attribution failed with unclaimed tmux %s drift; preserved %s; inspect these exact handles before retry or cleanup\n",
			kind, strings.Join(residual, ", "))
	}
}

func (m *materializer) warnUnclaimedHandle(kind runtimeObjectKind, id string) {
	if m.warn == nil || strings.TrimSpace(id) == "" {
		return
	}
	fmt.Fprintf(m.warn, "projmux: create could not claim tmux %s %s; preserved this exact residual handle for inspection before retry or cleanup\n", kind, id)
}

func (m *materializer) warnCompositeWindowResult(output string) {
	rows := splitTmuxRows(output, 2)
	if len(rows) != 1 {
		return
	}
	if id := exactTmuxHandle(rows[0][0], "@"); id != "" {
		m.warnUnclaimedHandle(runtimeWindow, id)
	}
	if id := exactTmuxHandle(rows[0][1], "%"); id != "" {
		m.warnUnclaimedHandle(runtimePane, id)
	}
}

// splitPlacementFlag maps the closed placement enum onto its tmux split axis.
func splitPlacementFlag(placement string) string {
	if placement == placementDown {
		return "-v"
	}
	return "-h"
}

// splitPane splits an anchor pane detached and returns the new pane id.
//
// `-d` is the whole point: tmux leaves the previously active pane active, so
// the split is a pure structural mutation with no focus side effect.
func (m *materializer) splitPane(ctx context.Context, anchorPaneID, placement, cwd string, command []string) (string, error) {
	before, beforeErr := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
	if beforeErr != nil {
		return "", tmuxError("list tmux panes around %q before split: %v", anchorPaneID, beforeErr)
	}
	args := []string{"split-window", "-d", "-P", "-F", "#{pane_id}", splitPlacementFlag(placement), "-t", anchorPaneID}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, command...)
	id, err := m.read(ctx, args...)
	if err != nil {
		after, listErr := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if listErr == nil {
			id = errorCreatedHandle(id, "%", before, after)
			if id == "" {
				m.warnUnclaimedRuntime("pane", before, after)
			}
		} else {
			id = ""
		}
		return id, tmuxError("split tmux pane %q: %v", anchorPaneID, err)
	}
	id = exactTmuxHandle(id, "%")
	if id == "" {
		return "", fmt.Errorf("split tmux pane %q: tmux returned no pane id", anchorPaneID)
	}
	return id, nil
}

// equalizeSplitLayout applies the same scoped, best-effort sizing used by the
// legacy AI split. It intentionally returns no error: layout observation is
// outside the create transaction's failure and rollback contract.
func (m *materializer) equalizeSplitLayout(ctx context.Context, anchorPaneID, placement string) {
	if m == nil || m.runner == nil {
		return
	}
	applyEvenSplitLayout(anchorPaneID, placement,
		func(args ...string) ([]byte, error) { return m.runner.Run(ctx, "tmux", args...) },
		func(args ...string) error {
			_, err := m.runner.Run(ctx, "tmux", args...)
			return err
		})
}

func (m *materializer) warnUnclaimedRuntime(kind string, before, after map[string]bool) {
	if m.warn == nil {
		return
	}
	var residual []string
	for id := range after {
		if !before[id] {
			residual = append(residual, id)
		}
	}
	slices.Sort(residual)
	if len(residual) > 0 {
		fmt.Fprintf(m.warn, "projmux: create failed with unclaimed tmux %s drift; preserved %s\n", kind, strings.Join(residual, ", "))
	}
}

func (m *materializer) runtimeIDs(ctx context.Context, command, target, format, prefix string) (map[string]bool, error) {
	out, err := m.read(ctx, command, "-t", target, "-F", format)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if id := exactTmuxHandle(line, prefix); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

func exactTmuxHandle(output, prefix string) string {
	output = strings.TrimSpace(output)
	if len(output) < 2 || !strings.HasPrefix(output, prefix) {
		return ""
	}
	for _, r := range strings.TrimPrefix(output, prefix) {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return output
}

func errorCreatedHandle(output, prefix string, before, after map[string]bool) string {
	var candidate string
	for line := range strings.SplitSeq(output, "\n") {
		id := exactTmuxHandle(line, prefix)
		if id == "" {
			continue
		}
		if candidate != "" && candidate != id {
			return ""
		}
		candidate = id
	}
	if candidate == "" || before[candidate] || !after[candidate] {
		return ""
	}
	return candidate
}

// The field separator of the materializer's own list queries, in the two
// spellings tmux distinguishes.
//
// A format must carry the escaped spelling. tmux renders a non-printable byte in
// list output as its octal escape, so a raw 0x1F in the format comes back raw on
// tmux 3.6 and as the four literal characters `\037` on tmux 3.5a, while the
// escaped spelling comes back identically on both. Parsing folds the escaped
// spelling back to the raw byte, which also accepts the raw form.
const (
	tmuxRowSep       = "\x1f"
	tmuxRowSepFormat = "\\037"
)

// tmuxRowFormat joins format fields with the separator tmux prints verbatim.
func tmuxRowFormat(fields ...string) string {
	return strings.Join(fields, tmuxRowSepFormat)
}

// splitTmuxRows parses a tmux list output into rows of exactly want fields.
func splitTmuxRows(output string, want int) [][]string {
	output = strings.ReplaceAll(output, tmuxRowSepFormat, tmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != want {
			continue
		}
		rows = append(rows, fields)
	}
	return rows
}

// strictTmuxRows is the identity-boundary variant of splitTmuxRows. Inventory
// attribution must not silently discard a malformed row and then conclude an
// identity is absent or unique from the incomplete set.
func strictTmuxRows(output string, want int) ([][]string, error) {
	output = strings.ReplaceAll(output, tmuxRowSepFormat, tmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != want {
			return nil, fmt.Errorf("row has %d fields, want %d", len(fields), want)
		}
		rows = append(rows, fields)
	}
	return rows, nil
}
