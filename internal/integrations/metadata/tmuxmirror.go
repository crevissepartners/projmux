package metadata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// ErrAmbiguousMirror marks a UID claimed by more than one live tmux object.
// Callers must fail closed rather than choosing one transport target.
var ErrAmbiguousMirror = errors.New("ambiguous tmux resource mirror")

// fieldSep matches the separator convention used by the existing tmux
// inventory adapter: an ASCII unit separator, escaped for the tmux format
// language.
//
// The two spellings are not interchangeable. tmux renders a non-printable byte
// in list output as its octal escape, so a format carrying the escaped spelling
// comes back as the four literal characters `\037` on every supported version,
// while a format carrying the raw byte comes back raw on tmux 3.6 and escaped on
// tmux 3.5a. Formats therefore always use escapedFieldSep, and parsing folds
// that spelling back to fieldSep before splitting.
const (
	fieldSep        = "\x1f"
	escapedFieldSep = "\\037"
)

// Runner executes tmux commands. It matches the runner shape already used by
// the tmux integration so tests inject a recorded fake.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Mirror writes Projmux resource identity into tmux options and resolves
// tmux raw targets back to uids.
type Mirror struct {
	Runner Runner
}

// NewMirror builds a mirror over runner.
func NewMirror(runner Runner) Mirror { return Mirror{Runner: runner} }

func (m Mirror) run(ctx context.Context, args ...string) ([]byte, error) {
	if m.Runner == nil {
		return nil, fmt.Errorf("metadata: tmux runner is required")
	}
	return m.Runner.Run(ctx, "tmux", args...)
}

// MirrorProject writes the Project uid and name onto its persistent session.
// The session gets no identity of its own; these options only let the adapter
// resolve the session back to the owning Project.
func (m Mirror) MirrorProject(ctx context.Context, sessionName string, project coremetadata.Project) error {
	if strings.TrimSpace(sessionName) == "" {
		return fmt.Errorf("metadata: session name is required to mirror project %s", project.Metadata.UID)
	}
	if _, err := m.run(ctx, "set-option", "-t", sessionName, "-q", tmuxopts.ProjectUIDSession, project.Metadata.UID); err != nil {
		return fmt.Errorf("metadata: mirror project uid: %w", err)
	}
	if _, err := m.run(ctx, "set-option", "-t", sessionName, "-q", tmuxopts.ProjectNameSession, project.Metadata.Name); err != nil {
		return fmt.Errorf("metadata: mirror project name: %w", err)
	}
	return nil
}

// RenameProject writes only the Project stable-name projection. It deliberately
// leaves the tmux session name, uid mirror, and project-path anchor untouched.
func (m Mirror) RenameProject(ctx context.Context, sessionName, name string) error {
	if strings.TrimSpace(sessionName) == "" {
		return fmt.Errorf("metadata: session name is required to rename project mirror")
	}
	if _, err := m.run(ctx, "set-option", "-t", sessionName, "-q", tmuxopts.ProjectNameSession, name); err != nil {
		return fmt.Errorf("metadata: mirror project name: %w", err)
	}
	return nil
}

// RebindProject writes only the exact session's Project root anchor. It does
// not rename the session or touch any filesystem path.
func (m Mirror) RebindProject(ctx context.Context, sessionName, root string) error {
	if strings.TrimSpace(sessionName) == "" {
		return fmt.Errorf("metadata: session name is required to rebind project mirror")
	}
	if _, err := m.run(ctx, "set-option", "-t", sessionName, "-q", tmuxopts.ProjectPathSession, root); err != nil {
		return fmt.Errorf("metadata: mirror project path: %w", err)
	}
	return nil
}

// ControlSessionMarkers is the writer-side evidence about one exact session on
// one exact server, read before any control marker is written.
//
// These fields are refusals waiting to happen, not capabilities:
//
//   - AppOwned is `@projmux_app == "1"` on the server. Writing a control role
//     onto a server projmux did not start would let projmux claim a role on the
//     operator's own tmux, which is exactly what the reader-side host check in
//     internal/core/resourcegraph refuses to honor. Writing a marker no reader
//     will ever trust is worse than not writing it: it leaves an option behind
//     on someone else's server.
//   - Ephemeral is `@projmux_ephemeral == "1"` on the session. Ephemeral grants
//     nothing, and the resolved graph fails closed on a session carrying both
//     markers. The writer must therefore never be the thing that produces that
//     pair.
type ControlSessionMarkers struct {
	AppOwned  bool
	Ephemeral bool
	// Role is the exact current session-role value. An empty value is repairable;
	// every non-control value is contradictory evidence and is refused.
	Role string
	// ProjectUID is the mutually exclusive Project identity claim on this exact
	// session. Control convergence never clears or overwrites it.
	ProjectUID string
}

// ControlSessionEligible reports whether this session may carry the control
// role. It is the writer-side half of the reader's two-fact rule.
func (c ControlSessionMarkers) ControlSessionEligible() bool {
	return c.AppOwned && !c.Ephemeral
}

// ObserveControlSessionMarkers reads the ownership and identity facts that
// decide whether a session may be marked as the control session. It performs no
// writes.
//
// An unset option is an observation, not a failure, exactly as it is for the
// resolved-graph host read: tmux answers a read of a never-set user option with
// a non-zero "invalid option", and a server with no `@projmux_app` is simply a
// server projmux did not start.
func (m Mirror) ObserveControlSessionMarkers(ctx context.Context, sessionName string) (ControlSessionMarkers, error) {
	if strings.TrimSpace(sessionName) == "" {
		return ControlSessionMarkers{}, fmt.Errorf("metadata: session name is required to observe control session markers")
	}
	app, err := m.run(ctx, "show-options", "-gv", tmuxopts.AppGlobal)
	switch {
	case err != nil && optionUnset(err):
		app = nil
	case err != nil:
		return ControlSessionMarkers{}, fmt.Errorf("metadata: read app marker: %w", err)
	}
	ephemeral, err := m.run(ctx, "display-message", "-p", "-t", sessionName, "-F", "#{"+tmuxopts.EphemeralSession+"}")
	if err != nil {
		return ControlSessionMarkers{}, fmt.Errorf("metadata: read session ephemeral marker: %w", err)
	}
	identity, err := m.run(ctx, "display-message", "-p", "-t", sessionName, "-F", tmuxFormat(
		"#{"+tmuxopts.SessionRole+"}",
		"#{"+tmuxopts.ProjectUIDSession+"}",
	))
	if err != nil {
		return ControlSessionMarkers{}, fmt.Errorf("metadata: read control session identity claims: %w", err)
	}
	fields := parseRows(string(identity), 2)
	var role, projectUID string
	if len(fields) == 1 {
		role = strings.TrimSpace(fields[0][0])
		projectUID = strings.TrimSpace(fields[0][1])
	}
	return ControlSessionMarkers{
		AppOwned:   strings.TrimSpace(string(app)) == resourcegraph.AppOwnedMarker,
		Ephemeral:  strings.TrimSpace(string(ephemeral)) == resourcegraph.EphemeralMarker,
		Role:       role,
		ProjectUID: projectUID,
	}, nil
}

// MirrorControlSessionRole writes the control role onto one exact session.
//
// It names one `-t <session>` target and never a pattern, a group, or `-g`. That
// is the whole of contract row 4's "config apply must not mutate unrelated
// sessions in bulk": there is no spelling of this call that can reach a second
// session. The declarative controller calls it only when the marker is missing,
// so a converged second pass performs no tmux write.
func (m Mirror) MirrorControlSessionRole(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return fmt.Errorf("metadata: session name is required to mirror the control session role")
	}
	if _, err := m.run(ctx, "set-option", "-t", sessionName, "-q", tmuxopts.SessionRole, resourcegraph.ControlSessionRole); err != nil {
		return fmt.Errorf("metadata: mirror control session role: %w", err)
	}
	return nil
}

// ObserveControlSession reads one live control session's windows and panes,
// together with the tmux ids the caller must mirror the bound uids onto. It
// performs no writes.
//
// It is a separate reader from ObserveLegacySessionTargets rather than a reuse of
// it for one reason: that one starts by reading `@projmux_project_path` as the
// Project root, and a control session has no root. Everything a control session
// binding needs is the identity a live object already carries plus the display
// sources -- and specifically not the provider conversation ids, because no
// Agent is ever minted below a control session.
func (m Mirror) ObserveControlSession(ctx context.Context, sessionName string) (coremetadata.ControlSessionObservation, LegacyTargets, error) {
	if strings.TrimSpace(sessionName) == "" {
		return coremetadata.ControlSessionObservation{}, LegacyTargets{}, fmt.Errorf("metadata: session name is required to observe a control session")
	}
	observed := coremetadata.ControlSessionObservation{Session: sessionName}
	var targets LegacyTargets

	windowsOut, err := m.run(ctx, "list-windows", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{window_name}",
		"#{session_id}",
		"#{window_id}",
		"#{"+tmuxopts.WindowUID+"}",
	))
	if err != nil {
		return coremetadata.ControlSessionObservation{}, LegacyTargets{}, fmt.Errorf("metadata: list control session windows: %w", err)
	}
	// tmux lists windows in window_index ascending order, which is the ordinal
	// the adoption rule pairs against.
	indexOrder := map[string]int{}
	for _, fields := range parseRows(string(windowsOut), 5) {
		indexOrder[fields[0]] = len(observed.Windows)
		observed.Windows = append(observed.Windows, coremetadata.ControlSessionWindow{
			DisplayName: fields[1], RuntimeSessionID: fields[2], RuntimeID: fields[3],
			UID: strings.TrimSpace(fields[4]),
		})
		targets.Windows = append(targets.Windows, fields[3])
		targets.Panes = append(targets.Panes, nil)
	}

	panesOut, err := m.run(ctx, "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{"+tmuxopts.PaneName+"}",
		"#{pane_current_command}",
		"#{pane_title}",
		"#{pane_current_path}",
		"#{pane_id}",
		"#{"+tmuxopts.PaneUID+"}",
	))
	if err != nil {
		return coremetadata.ControlSessionObservation{}, LegacyTargets{}, fmt.Errorf("metadata: list control session panes: %w", err)
	}
	for _, fields := range parseRows(string(panesOut), 7) {
		position, ok := indexOrder[fields[0]]
		if !ok {
			continue
		}
		observed.Windows[position].Panes = append(observed.Windows[position].Panes, coremetadata.ControlSessionPane{
			UID:     strings.TrimSpace(fields[6]),
			Name:    fields[1],
			Command: fields[2],
			Title:   fields[3],
			CWD:     fields[4],
		})
		targets.Panes[position] = append(targets.Panes[position], fields[5])
	}
	return observed, targets, nil
}

// MirrorWindow writes stable Window identity onto window-scoped tmux options,
// turns automatic-rename off, and writes the duplicate-allowed displayName to
// tmux window_name. A pre-projection Window with no displayName safely uses its
// stable metadata.name as the runtime display fallback.
func (m Mirror) MirrorWindow(ctx context.Context, target string, window coremetadata.Window) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("metadata: window target is required to mirror window %s", window.Metadata.UID)
	}
	if err := m.disableAutomaticRename(ctx, target); err != nil {
		return err
	}
	if _, err := m.run(ctx, "set-option", "-w", "-t", target, "-q", tmuxopts.WindowUID, window.Metadata.UID); err != nil {
		return fmt.Errorf("metadata: mirror window uid: %w", err)
	}
	if err := m.writeWindowIdentityName(ctx, target, window.Metadata.Name); err != nil {
		return err
	}
	return m.writeWindowDisplayName(ctx, target, window.DisplayName())
}

// DisableAutomaticRename turns automatic-rename off for one managed window.
// The global `automatic-rename on` default is untouched, so unmanaged windows
// keep their existing behavior.
func (m Mirror) DisableAutomaticRename(ctx context.Context, target string) error {
	return m.disableAutomaticRename(ctx, target)
}

func (m Mirror) disableAutomaticRename(ctx context.Context, target string) error {
	if _, err := m.run(ctx, "set-option", "-w", "-t", target, tmuxopts.AutomaticRenameWindow, "off"); err != nil {
		return fmt.Errorf("metadata: disable automatic-rename: %w", err)
	}
	return nil
}

func (m Mirror) writeWindowIdentityName(ctx context.Context, target, name string) error {
	if _, err := m.run(ctx, "set-option", "-w", "-t", target, "-q", tmuxopts.WindowName, name); err != nil {
		return fmt.Errorf("metadata: mirror window name: %w", err)
	}
	return nil
}

// RenameWindow writes only the stable Window name mirror. The duplicate-
// allowed displayName and raw tmux window_name remain unchanged.
func (m Mirror) RenameWindow(ctx context.Context, target, name string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("metadata: window target is required to rename window mirror")
	}
	return m.writeWindowIdentityName(ctx, target, name)
}

func (m Mirror) writeWindowDisplayName(ctx context.Context, target, displayName string) error {
	if _, err := m.run(ctx, "rename-window", "-t", target, displayName); err != nil {
		return fmt.Errorf("metadata: rename tmux window: %w", err)
	}
	return nil
}

// MirrorPane writes the Pane uid and mirrors metadata.name into the legacy
// pane-name option. The raw tmux pane_title is never written.
func (m Mirror) MirrorPane(ctx context.Context, paneID string, pane coremetadata.Pane) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("metadata: pane id is required to mirror pane %s", pane.Metadata.UID)
	}
	if _, err := m.run(ctx, "set-option", "-p", "-t", paneID, "-q", tmuxopts.PaneUID, pane.Metadata.UID); err != nil {
		return fmt.Errorf("metadata: mirror pane uid: %w", err)
	}
	if err := m.writePaneName(ctx, paneID, pane.Metadata.Name); err != nil {
		return err
	}
	if pane.Spec.Role == coremetadata.PaneRoleAgent {
		if _, err := m.run(ctx, "set-option", "-p", "-t", paneID, tmuxopts.RemainOnExitPane, "on"); err != nil {
			return fmt.Errorf("metadata: protect managed Agent pane lifecycle: %w", err)
		}
	}
	return nil
}

func (m Mirror) writePaneName(ctx context.Context, paneID, name string) error {
	if _, err := m.run(ctx, "set-option", "-p", "-t", paneID, "-q", tmuxopts.PaneName, name); err != nil {
		return fmt.Errorf("metadata: mirror pane name: %w", err)
	}
	return nil
}

// RenamePane applies `rename pane` semantics: it changes only the Pane
// metadata.name mirror. It never writes the raw tmux pane_title.
func (m Mirror) RenamePane(ctx context.Context, paneID, name string) error {
	return m.writePaneName(ctx, paneID, name)
}

// ResolvePaneUID returns the Projmux Pane uid mirrored onto a tmux pane id.
func (m Mirror) ResolvePaneUID(ctx context.Context, paneID string) (string, error) {
	out, err := m.run(ctx, "display-message", "-p", "-t", paneID, "-F", "#{"+tmuxopts.PaneUID+"}")
	if err != nil {
		return "", fmt.Errorf("metadata: read pane uid: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveWindowUID returns the Projmux Window uid mirrored onto a tmux window
// target.
func (m Mirror) ResolveWindowUID(ctx context.Context, target string) (string, error) {
	out, err := m.run(ctx, "display-message", "-p", "-t", target, "-F", "#{"+tmuxopts.WindowUID+"}")
	if err != nil {
		return "", fmt.Errorf("metadata: read window uid: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// livePaneRows reads the (mirrored Pane uid, tmux pane id) row of every pane on
// the server.
//
// It is the single query behind both the uid -> target lookup and the
// mirrored-uid inventory, so the two can never disagree about which Panes still
// have a transport binding.
func (m Mirror) livePaneRows(ctx context.Context) ([][]string, error) {
	out, err := m.run(ctx, "list-panes", "-a", "-F", tmuxFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list panes: %w", err)
	}
	return parseRows(string(out), 2), nil
}

// PaneTargetForUID scans every live pane for the mirrored uid and returns its
// tmux pane id.
func (m Mirror) PaneTargetForUID(ctx context.Context, uid string) (string, error) {
	target, found, err := m.FindPaneTargetForUID(ctx, uid)
	if err != nil {
		return "", err
	}
	if found {
		return target, nil
	}
	return "", fmt.Errorf("metadata: no live pane mirrors uid %q", uid)
}

// FindPaneTargetForUID returns the exact live target for uid. No match is an
// offline result; duplicate live claims are an error and are never guessed.
func (m Mirror) FindPaneTargetForUID(ctx context.Context, uid string) (string, bool, error) {
	rows, err := m.livePaneRows(ctx)
	if err != nil {
		return "", false, err
	}
	return exactUIDTarget(rows, uid, 1, "pane")
}

// LivePaneUIDs returns the set of Projmux Pane uids that a live tmux pane still
// mirrors.
//
// A pane carrying no mirrored uid contributes nothing, so the result is exactly
// the Panes the resource model still owns a transport binding for. That makes
// it the inventory half of a registry-versus-machine diff: a Pane uid the
// registry holds but this set does not is a Pane whose tmux pane is gone.
func (m Mirror) LivePaneUIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := m.livePaneRows(ctx)
	if err != nil {
		return nil, err
	}
	uids := make(map[string]bool, len(rows))
	for _, fields := range rows {
		if fields[0] != "" {
			uids[fields[0]] = true
		}
	}
	return uids, nil
}

// DeadPaneUIDs returns only exact mirrored Pane uids whose retained tmux Pane
// reports pane_dead=1. Agent panes opt into per-pane remain-on-exit, so this is
// positive same-socket evidence that the supervisor process ended while the
// Window/session anchor still exists; ordinary absence is deliberately not
// represented here.
func (m Mirror) DeadPaneUIDs(ctx context.Context) (map[string]bool, error) {
	out, err := m.run(ctx, "list-panes", "-a", "-F", tmuxFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_dead}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list dead Panes: %w", err)
	}
	dead := map[string]bool{}
	for _, fields := range parseRows(string(out), 2) {
		if fields[0] != "" && fields[1] == "1" {
			dead[fields[0]] = true
		}
	}
	return dead, nil
}

// LivePaneCount reports whether a successful exact-host observation saw any
// Pane at all, including unmirrored control/sibling Panes. This distinguishes a
// valid empty managed-uid set from an unavailable or truly empty server at the
// last-Pane teardown boundary.
func (m Mirror) LivePaneCount(ctx context.Context) (int, error) {
	out, err := m.run(ctx, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return 0, fmt.Errorf("metadata: count live Panes: %w", err)
	}
	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// liveWindowRows reads the (mirrored Window uid, stable tmux window id, session
// name, window index) row of every window on the server.
//
// It is the single query behind both the uid -> target lookup and the
// mirrored-uid inventory, the same way livePaneRows is for panes, so the two
// can never disagree about which Windows still have a transport binding.
func (m Mirror) liveWindowRows(ctx context.Context) ([][]string, error) {
	out, err := m.run(ctx, "list-windows", "-a", "-F", tmuxFormat("#{"+tmuxopts.WindowUID+"}", "#{window_id}", "#{session_name}", "#{window_index}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list windows: %w", err)
	}
	return parseRows(string(out), 4), nil
}

// WindowTargetForUID scans every live window for the mirrored uid and returns
// its tmux `session:index` target.
func (m Mirror) WindowTargetForUID(ctx context.Context, uid string) (string, error) {
	rows, err := m.liveWindowRows(ctx)
	if err != nil {
		return "", err
	}
	var targets [][]string
	for _, fields := range rows {
		if fields[0] == uid && fields[0] != "" {
			targets = append(targets, []string{fields[0], fields[2] + ":" + fields[3]})
		}
	}
	target, found, err := exactUIDTarget(targets, uid, 1, "window")
	if err != nil {
		return "", err
	}
	if found {
		return target, nil
	}
	return "", fmt.Errorf("metadata: no live window mirrors uid %q", uid)
}

// FindWindowTargetForUID returns the stable tmux window id for uid. The id does
// not retarget when a window is reordered or its session/window name changes
// between lookup and write. No match is offline; duplicate claims fail closed.
func (m Mirror) FindWindowTargetForUID(ctx context.Context, uid string) (string, bool, error) {
	rows, err := m.liveWindowRows(ctx)
	if err != nil {
		return "", false, err
	}
	return exactUIDTarget(rows, uid, 1, "window")
}

// LiveWindowUIDs returns the set of Projmux Window uids that a live tmux window
// still mirrors.
//
// It is the Window half of the registry-versus-machine diff, exactly as
// LivePaneUIDs is the Pane half: a window carrying no mirrored uid contributes
// nothing, so a Window uid the registry holds but this set does not is a Window
// whose tmux window is gone.
//
// This is a read. It never writes, re-mirrors, or adopts a uid onto a live tmux
// window -- reattaching a lost binding is a separate concern with its own
// failure modes, and doing it here would silently turn an observation into a
// mutation on every read verb.
func (m Mirror) LiveWindowUIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := m.liveWindowRows(ctx)
	if err != nil {
		return nil, err
	}
	uids := make(map[string]bool, len(rows))
	for _, fields := range rows {
		if fields[0] != "" {
			uids[fields[0]] = true
		}
	}
	return uids, nil
}

// LiveWindowSessionCounts counts every live Window by exact tmux session id.
// Unlike LiveWindowUIDs it deliberately includes unmirrored Windows, because a
// final-root teardown must fail closed while any sibling runtime Window remains.
func (m Mirror) LiveWindowSessionCounts(ctx context.Context) (map[string]int, error) {
	out, err := m.run(ctx, "list-windows", "-a", "-F", "#{session_id}")
	if err != nil {
		return nil, fmt.Errorf("metadata: list Window sessions: %w", err)
	}
	counts := map[string]int{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if session := strings.TrimSpace(line); session != "" {
			counts[session]++
		}
	}
	return counts, nil
}

// SessionForProjectUID scans every live session for the mirrored Project uid.
func (m Mirror) SessionForProjectUID(ctx context.Context, uid string) (string, error) {
	rows, err := m.liveProjectSessionRows(ctx)
	if err != nil {
		return "", err
	}
	var names [][]string
	for _, fields := range rows {
		if fields[0] == uid && fields[0] != "" {
			names = append(names, []string{fields[0], fields[2]})
		}
	}
	target, found, err := exactUIDTarget(names, uid, 1, "project session")
	if err != nil {
		return "", err
	}
	if found {
		return target, nil
	}
	return "", fmt.Errorf("metadata: no live session mirrors project uid %q", uid)
}

func (m Mirror) liveProjectSessionRows(ctx context.Context) ([][]string, error) {
	out, err := m.run(ctx, "list-sessions", "-F", tmuxFormat("#{"+tmuxopts.ProjectUIDSession+"}", "#{session_id}", "#{session_name}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list sessions: %w", err)
	}
	return parseRows(string(out), 3), nil
}

// FindSessionForProjectUID returns the stable tmux session id carrying uid. The
// id cannot be redirected by a concurrent session rename or name reuse between
// lookup and write. No match is an offline Project; duplicate claims fail
// closed.
func (m Mirror) FindSessionForProjectUID(ctx context.Context, uid string) (string, bool, error) {
	rows, err := m.liveProjectSessionRows(ctx)
	if err != nil {
		return "", false, err
	}
	return exactUIDTarget(rows, uid, 1, "project session")
}

func exactUIDTarget(rows [][]string, uid string, targetIndex int, kind string) (string, bool, error) {
	var target string
	for _, fields := range rows {
		if len(fields) <= targetIndex || fields[0] != uid || fields[0] == "" {
			continue
		}
		if target != "" {
			return "", false, fmt.Errorf("%w: multiple live %s targets mirror uid %q", ErrAmbiguousMirror, kind, uid)
		}
		target = fields[targetIndex]
	}
	return target, target != "", nil
}

// LegacyTargets carries the tmux transport handles of one observed legacy
// session, positionally aligned with the LegacySession the same observation
// produced.
//
// The alignment is what lets a migration mirror the uids it just allocated back
// onto exactly the tmux objects it imported. Without it the import result only
// knows list positions, and a position is not a tmux target: window_index is
// sparse and reorderable.
type LegacyTargets struct {
	// Windows[i] is the tmux window id of LegacySession.Windows[i].
	Windows []string
	// Panes[i][j] is the tmux pane id of LegacySession.Windows[i].Panes[j].
	Panes [][]string
}

// ObserveLegacySession reads one live session's pre-v2 naming state so it can
// be imported into the resource registry. It performs no writes.
func (m Mirror) ObserveLegacySession(ctx context.Context, sessionName string) (coremetadata.LegacySession, error) {
	legacy, _, err := m.ObserveLegacySessionTargets(ctx, sessionName)
	return legacy, err
}

// ObserveLegacySessionTargets reads one live session's pre-v2 naming state
// together with the tmux ids of every observed window and pane. It performs no
// writes.
//
// It also reads the `@projmux_window_uid` / `@projmux_pane_uid` each live
// object already carries. That is what lets the caller tell three states apart
// without a second query: a blank object, which adoption may pair with an
// unbound registry object; an object still carrying a uid the registry knows,
// whose binding is simply reapplied; and an object carrying a foreign uid,
// which is evidence of "not ours" and is left untouched.
//
// It reads the pane-scoped provider conversation ids in the same pass, for the
// same reason: agent runtime linkage has to tell "this live agent pane belongs
// to an Agent resource that already records this conversation" from "this
// conversation has no Agent yet", and paying for a second per-pane query to
// answer that would double the reconciler's tmux budget.
func (m Mirror) ObserveLegacySessionTargets(ctx context.Context, sessionName string) (coremetadata.LegacySession, LegacyTargets, error) {
	rootOut, err := m.run(ctx, "display-message", "-p", "-t", sessionName, "-F", "#{"+tmuxopts.ProjectPathSession+"}")
	if err != nil {
		return coremetadata.LegacySession{}, LegacyTargets{}, fmt.Errorf("metadata: read session project path: %w", err)
	}
	legacy := coremetadata.LegacySession{
		Session: sessionName,
		Root:    strings.TrimSpace(string(rootOut)),
	}
	var targets LegacyTargets

	windowsOut, err := m.run(ctx, "list-windows", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{window_name}",
		"#{"+tmuxopts.AutomaticRenameWindow+"}",
		"#{session_id}",
		"#{window_id}",
		"#{"+tmuxopts.WindowUID+"}",
	))
	if err != nil {
		return coremetadata.LegacySession{}, LegacyTargets{}, fmt.Errorf("metadata: list session windows: %w", err)
	}
	indexOrder := map[string]int{}
	// tmux lists windows in window_index ascending order, which is the ordinal
	// the adoption rule pairs against.
	for _, fields := range parseRows(string(windowsOut), 6) {
		indexOrder[fields[0]] = len(legacy.Windows)
		legacy.Windows = append(legacy.Windows, coremetadata.LegacyWindow{
			Name:             fields[1],
			AutomaticRename:  tmuxTruthyOption(fields[2]),
			RuntimeSessionID: fields[3], RuntimeID: fields[4],
			UID: strings.TrimSpace(fields[5]),
		})
		targets.Windows = append(targets.Windows, fields[4])
		targets.Panes = append(targets.Panes, nil)
	}

	panesOut, err := m.run(ctx, "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{"+tmuxopts.PaneName+"}",
		"#{"+tmuxopts.AgentProviderPane+"}",
		"#{"+tmuxopts.AgentLaunchAuthorshipPane+"}",
		"#{"+tmuxopts.AgentTopicPane+"}",
		"#{pane_current_command}",
		"#{pane_title}",
		"#{pane_current_path}",
		"#{pane_id}",
		"#{"+tmuxopts.PaneUID+"}",
		"#{"+tmuxopts.AgentSessionIDPane+"}",
		"#{"+tmuxopts.AgentThreadIDPane+"}",
	))
	if err != nil {
		return coremetadata.LegacySession{}, LegacyTargets{}, fmt.Errorf("metadata: list session panes: %w", err)
	}
	for _, fields := range parseRows(string(panesOut), 12) {
		position, ok := indexOrder[fields[0]]
		if !ok {
			continue
		}
		legacy.Windows[position].Panes = append(legacy.Windows[position].Panes, coremetadata.LegacyPane{
			Label:            fields[1],
			Provider:         fields[2],
			LaunchAuthorship: strings.TrimSpace(fields[3]),
			Topic:            fields[4],
			Command:          fields[5],
			Title:            fields[6],
			CWD:              fields[7],
			UID:              strings.TrimSpace(fields[9]),
			SessionID:        strings.TrimSpace(fields[10]),
			ThreadID:         strings.TrimSpace(fields[11]),
		})
		targets.Panes[position] = append(targets.Panes[position], fields[8])
	}
	return legacy, targets, nil
}

// tmuxTruthyOption reads a tmux boolean option value.
func tmuxTruthyOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "1", "yes", "true":
		return true
	default:
		return false
	}
}

func tmuxFormat(fields ...string) string {
	return strings.Join(fields, escapedFieldSep)
}

func parseRows(output string, want int) [][]string {
	// Fold the escaped separator tmux prints back into the raw byte. A field
	// value that literally contains the four characters `\037` would be split
	// here; no projmux-owned option, tmux id, or path ever does.
	output = strings.ReplaceAll(output, escapedFieldSep, fieldSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, fieldSep)
		if len(fields) != want {
			continue
		}
		rows = append(rows, fields)
	}
	return rows
}

// WindowTarget renders the canonical tmux window target for a session and
// window index.
func WindowTarget(session string, index int) string {
	return session + ":" + strconv.Itoa(index)
}
