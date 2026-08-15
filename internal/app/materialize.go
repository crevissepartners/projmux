package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	created []runtimeObject
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
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
		if got := m.option(ctx, entry.ID, "#{"+entry.ownershipOption()+"}"); got != entry.UID {
			// Either the object is gone or it no longer belongs to this
			// operation. Both mean "not ours to remove".
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
) (created bool, err error) {
	exists, err := m.sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return false, tmuxError("check tmux session %q: %v", sessionName, err)
	}
	if exists {
		return false, nil
	}
	if err := m.sessions.EnsureSession(ctx, sessionName, project.Spec.Root); err != nil {
		// A pre-create hook refusal lands here with nothing created, so the
		// ledger stays empty and the caller's transaction is discarded whole.
		return false, tmuxError("materialize tmux session %q: %v", sessionName, err)
	}
	sessionID := m.option(ctx, sessionName, "#{session_id}")
	if err := m.mirror.MirrorProject(ctx, sessionName, project); err != nil {
		// The session exists but is unidentifiable, so record it under the uid
		// the caller expects before surfacing the failure; otherwise rollback
		// would refuse to remove the session this operation just created.
		m.recordSessionForRollback(ctx, ledger, sessionID, project.Metadata.UID)
		return true, err
	}
	ledger.record(runtimeSession, sessionID, project.Metadata.UID)
	return true, nil
}

// recordSessionForRollback mirrors the Project uid onto a session whose normal
// mirror failed, so the ownership-checked rollback can still recognize it.
func (m *materializer) recordSessionForRollback(ctx context.Context, ledger *runtimeLedger, sessionID, uid string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	_, _ = m.runner.Run(ctx, "tmux", "set-option", "-t", sessionID, "-q", tmuxopts.ProjectUIDSession, uid)
	ledger.record(runtimeSession, sessionID, uid)
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

// newWindow creates one detached tmux window and returns its stable window id.
func (m *materializer) newWindow(ctx context.Context, sessionName, name, cwd string, command []string) (string, error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", sessionName + ":"}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, command...)
	id, err := m.read(ctx, args...)
	if err != nil {
		return "", tmuxError("create tmux window in session %q: %v", sessionName, err)
	}
	if id == "" {
		return "", fmt.Errorf("create tmux window in session %q: tmux returned no window id", sessionName)
	}
	return id, nil
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
	args := []string{"split-window", "-d", "-P", "-F", "#{pane_id}", splitPlacementFlag(placement), "-t", anchorPaneID}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, command...)
	id, err := m.read(ctx, args...)
	if err != nil {
		return "", tmuxError("split tmux pane %q: %v", anchorPaneID, err)
	}
	if id == "" {
		return "", fmt.Errorf("split tmux pane %q: tmux returned no pane id", anchorPaneID)
	}
	return id, nil
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
