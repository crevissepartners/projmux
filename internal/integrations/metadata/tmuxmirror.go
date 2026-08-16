package metadata

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

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
	return m.writePaneName(ctx, paneID, pane.Metadata.Name)
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
	rows, err := m.livePaneRows(ctx)
	if err != nil {
		return "", err
	}
	for _, fields := range rows {
		if fields[0] == uid && fields[0] != "" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("metadata: no live pane mirrors uid %q", uid)
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

// liveWindowRows reads the (mirrored Window uid, session name, window index)
// row of every window on the server.
//
// It is the single query behind both the uid -> target lookup and the
// mirrored-uid inventory, the same way livePaneRows is for panes, so the two
// can never disagree about which Windows still have a transport binding.
func (m Mirror) liveWindowRows(ctx context.Context) ([][]string, error) {
	out, err := m.run(ctx, "list-windows", "-a", "-F", tmuxFormat("#{"+tmuxopts.WindowUID+"}", "#{session_name}", "#{window_index}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list windows: %w", err)
	}
	return parseRows(string(out), 3), nil
}

// WindowTargetForUID scans every live window for the mirrored uid and returns
// its tmux `session:index` target.
func (m Mirror) WindowTargetForUID(ctx context.Context, uid string) (string, error) {
	rows, err := m.liveWindowRows(ctx)
	if err != nil {
		return "", err
	}
	for _, fields := range rows {
		if fields[0] == uid && fields[0] != "" {
			return fields[1] + ":" + fields[2], nil
		}
	}
	return "", fmt.Errorf("metadata: no live window mirrors uid %q", uid)
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

// SessionForProjectUID scans every live session for the mirrored Project uid.
func (m Mirror) SessionForProjectUID(ctx context.Context, uid string) (string, error) {
	out, err := m.run(ctx, "list-sessions", "-F", tmuxFormat("#{"+tmuxopts.ProjectUIDSession+"}", "#{session_name}"))
	if err != nil {
		return "", fmt.Errorf("metadata: list sessions: %w", err)
	}
	for _, fields := range parseRows(string(out), 2) {
		if fields[0] == uid && fields[0] != "" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("metadata: no live session mirrors project uid %q", uid)
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
		"#{window_id}",
		"#{"+tmuxopts.WindowUID+"}",
	))
	if err != nil {
		return coremetadata.LegacySession{}, LegacyTargets{}, fmt.Errorf("metadata: list session windows: %w", err)
	}
	indexOrder := map[string]int{}
	// tmux lists windows in window_index ascending order, which is the ordinal
	// the adoption rule pairs against.
	for _, fields := range parseRows(string(windowsOut), 5) {
		indexOrder[fields[0]] = len(legacy.Windows)
		legacy.Windows = append(legacy.Windows, coremetadata.LegacyWindow{
			Name:            fields[1],
			AutomaticRename: tmuxTruthyOption(fields[2]),
			UID:             strings.TrimSpace(fields[4]),
		})
		targets.Windows = append(targets.Windows, fields[3])
		targets.Panes = append(targets.Panes, nil)
	}

	panesOut, err := m.run(ctx, "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{"+tmuxopts.PaneName+"}",
		"#{"+tmuxopts.AgentProviderPane+"}",
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
	for _, fields := range parseRows(string(panesOut), 11) {
		position, ok := indexOrder[fields[0]]
		if !ok {
			continue
		}
		legacy.Windows[position].Panes = append(legacy.Windows[position].Panes, coremetadata.LegacyPane{
			Label:     fields[1],
			Provider:  fields[2],
			Topic:     fields[3],
			Command:   fields[4],
			Title:     fields[5],
			CWD:       fields[6],
			UID:       strings.TrimSpace(fields[8]),
			SessionID: strings.TrimSpace(fields[9]),
			ThreadID:  strings.TrimSpace(fields[10]),
		})
		targets.Panes[position] = append(targets.Panes[position], fields[7])
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
