package mutations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// ExplicitSocketRenamer admits no inherited transport. Each operation captures
// the exact server generation after commit and re-proves it before every read
// and write. CLI invocation authority remains in its route adapter.
func ExplicitSocketRenamer(s Store, load func() (metadata.Registry, error), socket string, runner intmetadata.Runner) Renamer {
	c := Renamer{Store: s, Load: load}
	if socket == "" {
		return c
	}
	c.StrictMirror = true
	r := &renameSocket{path: socket, runner: runner, load: load}
	c.Mirror = r
	c.ResolveWindowRoute = func(ctx context.Context) (WindowRenameRoute, error) {
		if err := r.check(ctx); err != nil {
			return WindowRenameRoute{}, err
		}
		return WindowRenameRoute{SocketPath: socket, Runner: r, Check: func(ctx context.Context, _ WindowRenameStep) error { return r.check(ctx) }, Execute: executeWindowRenameSteps}, nil
	}
	return c
}

type renameSocket struct {
	path, pid           string
	runner              intmetadata.Runner
	load                func() (metadata.Registry, error)
	projectUID, paneUID string
}

func (r *renameSocket) raw(ctx context.Context, args ...string) ([]byte, error) {
	if r.runner == nil {
		return nil, errors.New("rename requires a tmux runner")
	}
	return r.runner.Run(ctx, "tmux", append([]string{"-S", r.path}, args...)...)
}
func (r *renameSocket) check(ctx context.Context) error {
	if !filepath.IsAbs(r.path) {
		return errors.New("SocketPath must be absolute")
	}
	out, err := r.raw(ctx, "display-message", "-p", "-F", tmuxRowFormat("#{socket_path}", "#{pid}"))
	if err != nil {
		return err
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || filepath.Clean(rows[0][0]) != filepath.Clean(r.path) {
		return errors.New("rename socket identity drifted")
	}
	pid := rows[0][1]
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		return errors.New("rename server PID is unreadable")
	}
	if r.pid == "" {
		r.pid = pid
	} else if r.pid != pid {
		return errors.New("rename server PID drifted")
	}
	return nil
}
func (r *renameSocket) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	if err := r.check(ctx); err != nil {
		return nil, err
	}
	out, err := r.raw(ctx, args...)
	if err != nil {
		return out, err
	}
	if err := r.check(ctx); err != nil {
		return out, err
	}
	return out, nil
}
func (r *renameSocket) FindSessionForProjectUID(ctx context.Context, uid string) (string, bool, error) {
	r.projectUID = uid
	return r.proveProject(ctx, uid)
}
func (r *renameSocket) proveProject(ctx context.Context, uid string) (string, bool, error) {
	out, err := r.Run(ctx, "tmux", "list-sessions", "-F", tmuxRowFormat("#{session_id}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return "", false, err
	}
	rows, err := strictRenameRows(string(out), 3)
	if err != nil {
		return "", false, err
	}
	var found []string
	for _, row := range rows {
		if row[1] != uid {
			continue
		}
		if row[2] != "" || exactTmuxHandle(row[0], "$") == "" {
			return "", false, errors.New("project runtime containment drifted")
		}
		found = append(found, row[0])
	}
	if len(found) != 1 {
		return "", false, fmt.Errorf("project runtime UID must match exactly one session, got %d", len(found))
	}
	return found[0], true, nil
}
func (r *renameSocket) FindPaneTargetForUID(ctx context.Context, uid string) (string, bool, error) {
	r.paneUID = uid
	reg, err := r.load()
	if err != nil {
		return "", false, err
	}
	if pane, ok := reg.Pane(uid); ok && pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
		// A process Pane can be renamed with the same best-effort inventory
		// lookup as the CLI. Once captured, server drift remains a refusal.
		if err := r.check(ctx); err != nil {
			return "", false, nil
		}
	}
	return r.provePane(ctx, uid)
}
func (r *renameSocket) provePane(ctx context.Context, uid string) (string, bool, error) {
	reg, err := r.load()
	if err != nil {
		return "", false, err
	}
	pane, ok := reg.Pane(uid)
	if !ok {
		return "", false, errors.New("renamed Pane disappeared from Registry")
	}
	w, ok := WindowForPane(reg, pane.Metadata.UID)
	if !ok {
		return "", false, errors.New("pane Window disappeared from Registry")
	}
	root := w.Metadata.OwnerUID()
	if _, ok := reg.Project(root); !ok {
		return "", false, errors.New("pane requires a Project root")
	}
	out, err := r.Run(ctx, "tmux", "list-panes", "-a", "-F", tmuxRowFormat("#{pane_id}", "#{"+tmuxopts.PaneUID+"}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}", "#{session_id}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return "", false, err
	}
	rows, err := strictRenameRows(string(out), 7)
	if err != nil {
		return "", false, err
	}
	var found []string
	for _, row := range rows {
		if row[1] != uid {
			continue
		}
		if exactTmuxHandle(row[0], "%") == "" || exactTmuxHandle(row[2], "@") == "" || exactTmuxHandle(row[4], "$") == "" || row[3] != w.Metadata.UID || row[5] != root || row[6] != "" {
			return "", false, errors.New("pane Project containment drifted")
		}
		found = append(found, row[0])
	}
	if len(found) == 0 && pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
		return "", false, nil
	}
	if len(found) != 1 {
		return "", false, fmt.Errorf("pane runtime UID must match exactly one pane, got %d", len(found))
	}
	return found[0], true, nil
}
func (r *renameSocket) RenameProject(ctx context.Context, target, name string) error {
	current, _, err := r.proveProject(ctx, r.projectUID)
	if err != nil {
		return err
	}
	if current != target {
		return errors.New("project runtime identity drifted before rename")
	}
	if err := intmetadata.NewMirror(r).RenameProject(ctx, target, name); err != nil {
		return err
	}
	current, _, err = r.proveProject(ctx, r.projectUID)
	if err != nil {
		return err
	}
	if current != target {
		return errors.New("project runtime identity drifted after rename")
	}
	out, err := r.Run(ctx, "tmux", "show-options", "-t", target, "-qv", tmuxopts.ProjectNameSession)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != name {
		return errors.New("project mirror name did not converge")
	}
	return nil
}
func (r *renameSocket) RenamePane(ctx context.Context, target, name string) error {
	current, _, err := r.provePane(ctx, r.paneUID)
	if err != nil {
		return err
	}
	if current != target {
		return errors.New("pane runtime identity drifted before rename")
	}
	if err := intmetadata.NewMirror(r).RenamePane(ctx, target, name); err != nil {
		return err
	}
	current, _, err = r.provePane(ctx, r.paneUID)
	if err != nil {
		return err
	}
	if current != target {
		return errors.New("pane runtime identity drifted after rename")
	}
	out, err := r.Run(ctx, "tmux", "show-options", "-p", "-t", target, "-qv", tmuxopts.PaneName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != name {
		return errors.New("pane mirror name did not converge")
	}
	return nil
}

func strictRenameRows(out string, want int) ([][]string, error) {
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\\037", "\x1f"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(strings.Split(strings.TrimRight(line, "\r"), "\x1f")) != want {
			return nil, errors.New("rename runtime inventory is malformed")
		}
	}
	return splitTmuxRows(out, want), nil
}

// The SDK executor preserves the CLI plan's admission order: observe all,
// guard all, apply in order, then prove all effects. Renames own no undo.
func executeWindowRenameSteps(ctx context.Context, steps []WindowRenameStep) error {
	pending := make([]WindowRenameStep, 0, len(steps))
	for _, s := range steps {
		if err := s.TargetRouteGuard(ctx); err != nil {
			return err
		}
		ok, err := s.Reobserve(ctx)
		if err != nil {
			return err
		}
		if !ok {
			pending = append(pending, s)
		}
	}
	guarded := make([]WindowRenameStep, 0, len(pending))
	for _, s := range pending {
		if err := s.Guard(ctx); err != nil {
			if errors.Is(err, ErrEffectConverged) {
				continue
			}
			return err
		}
		guarded = append(guarded, s)
	}
	for _, s := range guarded {
		// Re-prove object identity at each write, including between the stable
		// name and tab writes. Same-value convergence belongs to its writer.
		if err := s.Guard(ctx); err != nil {
			if errors.Is(err, ErrEffectConverged) {
				continue
			}
			return err
		}
		if err := s.Apply(ctx); err != nil {
			return err
		}
	}
	for _, s := range guarded {
		ok, err := s.Reobserve(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("runtime mutation plan: expected effects were not fully observed")
		}
	}
	return nil
}
