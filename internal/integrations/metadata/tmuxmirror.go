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

// MirrorWindow writes the Window uid and name onto a live tmux window, turns
// automatic-rename off for that window, and mirrors the name into the tmux
// window_name.
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
	return m.writeWindowName(ctx, target, window.Metadata.Name)
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

func (m Mirror) writeWindowName(ctx context.Context, target, name string) error {
	if _, err := m.run(ctx, "set-option", "-w", "-t", target, "-q", tmuxopts.WindowName, name); err != nil {
		return fmt.Errorf("metadata: mirror window name: %w", err)
	}
	if _, err := m.run(ctx, "rename-window", "-t", target, name); err != nil {
		return fmt.Errorf("metadata: rename tmux window: %w", err)
	}
	return nil
}

// RenameWindow applies `rename window` semantics: it changes the Window
// metadata.name mirror and the tmux window_name, and keeps automatic-rename
// off so the new name survives a focus change.
func (m Mirror) RenameWindow(ctx context.Context, target, name string) error {
	if err := m.disableAutomaticRename(ctx, target); err != nil {
		return err
	}
	return m.writeWindowName(ctx, target, name)
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

// PaneTargetForUID scans every live pane for the mirrored uid and returns its
// tmux pane id.
func (m Mirror) PaneTargetForUID(ctx context.Context, uid string) (string, error) {
	out, err := m.run(ctx, "list-panes", "-a", "-F", tmuxFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return "", fmt.Errorf("metadata: list panes: %w", err)
	}
	for _, fields := range parseRows(string(out), 2) {
		if fields[0] == uid && fields[0] != "" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("metadata: no live pane mirrors uid %q", uid)
}

// WindowTargetForUID scans every live window for the mirrored uid and returns
// its tmux `session:index` target.
func (m Mirror) WindowTargetForUID(ctx context.Context, uid string) (string, error) {
	out, err := m.run(ctx, "list-windows", "-a", "-F", tmuxFormat("#{"+tmuxopts.WindowUID+"}", "#{session_name}", "#{window_index}"))
	if err != nil {
		return "", fmt.Errorf("metadata: list windows: %w", err)
	}
	for _, fields := range parseRows(string(out), 3) {
		if fields[0] == uid && fields[0] != "" {
			return fields[1] + ":" + fields[2], nil
		}
	}
	return "", fmt.Errorf("metadata: no live window mirrors uid %q", uid)
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

// ObserveLegacySession reads one live session's pre-v2 naming state so it can
// be imported into the resource registry. It performs no writes.
func (m Mirror) ObserveLegacySession(ctx context.Context, sessionName string) (coremetadata.LegacySession, error) {
	rootOut, err := m.run(ctx, "display-message", "-p", "-t", sessionName, "-F", "#{"+tmuxopts.ProjectPathSession+"}")
	if err != nil {
		return coremetadata.LegacySession{}, fmt.Errorf("metadata: read session project path: %w", err)
	}
	legacy := coremetadata.LegacySession{
		Session: sessionName,
		Root:    strings.TrimSpace(string(rootOut)),
	}

	windowsOut, err := m.run(ctx, "list-windows", "-t", sessionName, "-F", tmuxFormat("#{window_index}", "#{window_name}", "#{"+tmuxopts.AutomaticRenameWindow+"}"))
	if err != nil {
		return coremetadata.LegacySession{}, fmt.Errorf("metadata: list session windows: %w", err)
	}
	indexOrder := map[string]int{}
	for _, fields := range parseRows(string(windowsOut), 3) {
		indexOrder[fields[0]] = len(legacy.Windows)
		legacy.Windows = append(legacy.Windows, coremetadata.LegacyWindow{
			Name:            fields[1],
			AutomaticRename: tmuxTruthyOption(fields[2]),
		})
	}

	panesOut, err := m.run(ctx, "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{"+tmuxopts.PaneName+"}",
		"#{"+tmuxopts.AgentProviderPane+"}",
		"#{"+tmuxopts.AgentTopicPane+"}",
		"#{pane_current_command}",
		"#{pane_title}",
		"#{pane_current_path}",
	))
	if err != nil {
		return coremetadata.LegacySession{}, fmt.Errorf("metadata: list session panes: %w", err)
	}
	for _, fields := range parseRows(string(panesOut), 7) {
		position, ok := indexOrder[fields[0]]
		if !ok {
			continue
		}
		legacy.Windows[position].Panes = append(legacy.Windows[position].Panes, coremetadata.LegacyPane{
			Label:    fields[1],
			Provider: fields[2],
			Topic:    fields[3],
			Command:  fields[4],
			Title:    fields[5],
			CWD:      fields[6],
		})
	}
	return legacy, nil
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
