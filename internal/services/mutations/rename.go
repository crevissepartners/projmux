package mutations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

type RenameOriginGuard func(context.Context, coremetadata.Registry, coremetadata.Mutator) error

type RenameMirror interface {
	FindSessionForProjectUID(context.Context, string) (string, bool, error)
	FindPaneTargetForUID(context.Context, string) (string, bool, error)
	RenameProject(context.Context, string, string) error
	RenamePane(context.Context, string, string) error
}

// Renamer owns the one Registry commit and live mirror path. Transport adapters
// supply route authority; generated CLI intents supply their locked origin guard.
type Renamer struct {
	Store              Store
	Load               func() (coremetadata.Registry, error)
	Mirror             RenameMirror
	ResolveWindowRoute func(context.Context) (WindowRenameRoute, error)
	// StrictMirror makes explicit SDK authority failures observable after commit.
	StrictMirror bool
}

func (c Renamer) CommitRename(ctx context.Context, kind coremetadata.Kind, uid, name string, guard RenameOriginGuard) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	ctx = context.WithoutCancel(ctx)
	if c.Store.Update == nil || c.Store.Mutator == nil {
		return false, "", errors.New("resource registry store is not configured")
	}
	_, err := c.Store.Update(func(working *coremetadata.Registry) error {
		if _, _, ok := resources.ResourceFor(*working, kind, uid); !ok {
			return fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)
		}
		mutator := c.Store.Mutator()
		if guard != nil {
			if err := guard(ctx, working.Clone(), mutator); err != nil {
				return err
			}
		}
		switch kind {
		case coremetadata.KindProject:
			_, err := mutator.RenameProject(working, uid, name)
			return err
		case coremetadata.KindWindow:
			_, err := mutator.RenameWindow(working, uid, name)
			return err
		case coremetadata.KindPane:
			_, err := mutator.RenamePane(working, uid, name)
			return err
		case coremetadata.KindAgent:
			_, err := mutator.RenameAgent(working, uid, name)
			return err
		default:
			return fmt.Errorf("rename %s: unsupported kind %q", strings.ToLower(string(kind)), kind)
		}
	})
	if err != nil {
		return false, "", err
	}
	notice, err := c.MirrorRenamed(ctx, kind, uid, name)
	return true, notice, err
}

func CommittedMirrorError(verb string, kind coremetadata.Kind, uid string, err error) error {
	return fmt.Errorf("%s %s %q committed Registry state but could not converge its exact live tmux mirror: %w; retry on the same exact socket with `projmux reconcile resources`", verb, strings.ToLower(string(kind)), uid, err)
}

// WindowRenameRoute binds every observation and action to the captured server.
type WindowRenameRoute struct {
	SocketPath string
	Runner     intmetadata.Runner
	Check      func(context.Context, WindowRenameStep) error
	Execute    func(context.Context, []WindowRenameStep) error
}
type WindowRenameStep struct {
	Verb, WindowID, UID, Parent string
	Operands                    []string
	TargetRouteGuard            func(context.Context) error
	Guard                       func(context.Context) error
	Apply                       func(context.Context) error
	Reobserve                   func(context.Context) (bool, error)
}

var ErrWindowRunnerNotConfigured = errors.New("typed runtime mutation runner is not configured")

var ErrEffectConverged = errors.New("runtime mutation plan: expected effect already present at guard")

func RenameReceipt(kind coremetadata.Kind, uid, name string) receipt.OperationReceipt {
	operation := receipt.OperationRenameProject
	switch kind {
	case coremetadata.KindWindow:
		operation = receipt.OperationRenameWindow
	case coremetadata.KindPane:
		operation = receipt.OperationRenamePane
	case coremetadata.KindAgent:
		operation = receipt.OperationRenameAgent
	}
	out := receipt.NewReceipt(operation, receipt.ReceiptTarget{Kind: string(kind), UID: uid, Name: name}, receipt.ReceiptEffects{
		Identity:     receipt.IdentityUnchanged,
		Address:      receipt.AddressRenamed,
		Topology:     receipt.TopologyUnchanged,
		DesiredState: receipt.DesiredStateUnchanged,
		Runtime:      receipt.RuntimeUnchanged,
		Focus:        receipt.FocusUnchanged,
	})
	out.Add(string(kind), uid, name, receipt.ActionRenamed)
	return out
}

func WindowDisplayNotConvergedNotice(uid, name string) string {
	return fmt.Sprintf("rename window: window/%s: committed the Registry name %q; this invocation has no live tmux route, so the tmux tab keeps its old name until a rename runs inside that runtime", uid, name)
}

func (c Renamer) MirrorRenamed(ctx context.Context, kind coremetadata.Kind, uid, name string) (string, error) {
	if kind == coremetadata.KindAgent {
		return "", nil
	}
	if kind == coremetadata.KindWindow {
		if c.Mirror == nil {
			return WindowDisplayNotConvergedNotice(uid, name), nil
		}
		return c.RenameRuntimeWindow(ctx, uid, name)
	}
	if c.Mirror == nil {
		return "", nil
	}
	var (
		target string
		found  bool
		err    error
	)
	switch kind {
	case coremetadata.KindProject:
		target, found, err = c.Mirror.FindSessionForProjectUID(ctx, uid)
	case coremetadata.KindPane:
		target, found, err = c.Mirror.FindPaneTargetForUID(ctx, uid)
	}
	if err != nil {
		if c.StrictMirror || errors.Is(err, intmetadata.ErrAmbiguousMirror) {
			return "", CommittedMirrorError("rename", kind, uid, err)
		}
		// An unavailable inventory cannot prove this resource is live. Preserve
		// the authoritative Registry result for later exact-socket reconcile.
		return "", nil
	}
	if !found {
		if c.StrictMirror {
			return "", CommittedMirrorError("rename", kind, uid, errors.New("resource runtime identity was not found"))
		}
		return "", nil
	}
	switch kind {
	case coremetadata.KindProject:
		err = c.Mirror.RenameProject(ctx, target, name)
	case coremetadata.KindPane:
		err = c.Mirror.RenamePane(ctx, target, name)
	}
	if err != nil {
		return "", CommittedMirrorError("rename", kind, uid, err)
	}
	return "", nil
}

type runtimeWindowRenameObservation struct {
	windowID   string
	sessionID  string
	projectUID string
	role       string
	stableName string
	windowName string
}

// renameRuntimeWindow converges the live projection of one committed Window
// rename. Inside a proven runtime route the Registry name, the
// `@projmux_window_name` mirror and the tmux tab (`#{window_name}`) are one
// value: the stable-name option is written first and the display follows
// through the canonical `rename-window -t @N -- <name>`, the same argv shape
// and validator the rename key and the Window menu Rename item go through, so a
// leading-dash name cannot be read as a flag. Reobservation requires the tab to
// carry the new name; a display that did not move is a committed-mirror failure,
// not a success.
//
// With no route there is no tab to converge and the rename stays
// Registry-authoritative, so the caller is handed the display notice instead of
// an error.
func (c Renamer) RenameRuntimeWindow(ctx context.Context, uid, name string) (string, error) {
	if c.ResolveWindowRoute == nil {
		return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, errors.New("typed runtime mutation runner is not configured"))
	}
	registry, err := c.Load()
	if err != nil {
		return "", err
	}
	window, ok := registry.Window(uid)
	if !ok {
		return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, errors.New("renamed Window disappeared from Registry"))
	}
	rootUID := window.Metadata.OwnerUID()
	rootKind := coremetadata.KindProject
	if _, ok := registry.Project(rootUID); !ok {
		if _, ok := registry.ControlSession(rootUID); !ok {
			return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, errors.New("Window owner root disappeared from Registry"))
		}
		rootKind = coremetadata.KindControlSession
	}
	route, err := c.ResolveWindowRoute(ctx)
	if err != nil {
		if c.StrictMirror || errors.Is(err, ErrWindowRunnerNotConfigured) {
			return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, err)
		}
		// Preserve the established metadata-authoritative behavior when no live
		// runtime can be proven. A later controller pass projects the name.
		return WindowDisplayNotConvergedNotice(uid, name), nil
	}
	if route.SocketPath == "" {
		return WindowDisplayNotConvergedNotice(uid, name), nil
	}
	routed := route.Runner
	observe := func(ctx context.Context) (runtimeWindowRenameObservation, bool, error) {
		out, err := routed.Run(ctx, "tmux", "list-windows", "-a", "-F", tmuxRowFormat(
			"#{window_id}", "#{"+tmuxopts.WindowUID+"}", "#{session_id}",
			"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}",
			"#{"+tmuxopts.WindowName+"}", "#{window_name}"))
		if err != nil {
			return runtimeWindowRenameObservation{}, false, err
		}
		rows := splitTmuxRows(string(out), 7)
		if c.StrictMirror {
			var e error
			rows, e = strictRenameRows(string(out), 7)
			if e != nil {
				return runtimeWindowRenameObservation{}, false, e
			}
		}
		var found []runtimeWindowRenameObservation
		for _, row := range rows {
			if row[1] != uid {
				continue
			}
			found = append(found, runtimeWindowRenameObservation{windowID: row[0], sessionID: row[2], projectUID: row[3], role: row[4], stableName: row[5], windowName: row[6]})
		}
		if len(found) == 0 {
			return runtimeWindowRenameObservation{}, false, nil
		}
		if len(found) != 1 || exactTmuxHandle(found[0].windowID, "@") == "" || exactTmuxHandle(found[0].sessionID, "$") == "" {
			return runtimeWindowRenameObservation{}, false, errors.New("Window runtime identity is ambiguous")
		}
		if rootKind == coremetadata.KindProject {
			if found[0].projectUID != rootUID || found[0].role != "" {
				return runtimeWindowRenameObservation{}, false, errors.New("Window Project containment drifted")
			}
		} else if found[0].projectUID != "" || found[0].role != resourcegraph.ControlSessionRole {
			return runtimeWindowRenameObservation{}, false, errors.New("Window ControlSession containment drifted")
		}
		return found[0], true, nil
	}
	observed, found, err := observe(ctx)
	if err != nil {
		return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, err)
	}
	if !found {
		if c.StrictMirror {
			return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, errors.New("window runtime identity was not found"))
		}
		return "", nil
	}
	parent := string(rootKind) + "/" + rootUID + "/" + observed.sessionID
	stable := WindowRenameStep{Verb: "write-stable-name", WindowID: observed.windowID, UID: uid, Parent: parent,
		Operands: []string{"-w", "-t", observed.windowID, "-q", tmuxopts.WindowName, name}}
	display := WindowRenameStep{Verb: "rename-window", WindowID: observed.windowID, UID: uid, Parent: parent,
		Operands: []string{"-t", observed.windowID, "--", name}}
	// The identity half of every step is the same: the exact live Window still
	// carries this UID inside this root on this server generation. Only the
	// field each step converges differs.
	// Both steps write their field to the same planned name. At the guard each
	// written field may sit at its snapshot value or at name, and every other
	// field must still equal the snapshot.
	written := []func(runtimeWindowRenameObservation) string{
		func(o runtimeWindowRenameObservation) string { return o.stableName },
		func(o runtimeWindowRenameObservation) string { return o.windowName },
	}
	step := func(action WindowRenameStep, own func(runtimeWindowRenameObservation) string) WindowRenameStep {
		action.TargetRouteGuard = func(ctx context.Context) error { return route.Check(ctx, action) }
		action.Reobserve = func(ctx context.Context) (bool, error) {
			if err := route.Check(ctx, action); err != nil {
				return false, err
			}
			current, ok, err := observe(ctx)
			if err != nil {
				return false, err
			}
			return ok && current.windowID == action.WindowID && current.sessionID == observed.sessionID && own(current) == name, nil
		}
		action.Guard = func(ctx context.Context) error {
			if err := route.Check(ctx, action); err != nil {
				return err
			}
			current, ok, err := observe(ctx)
			if err != nil {
				return err
			}
			drifted := !ok
			if ok {
				unwritten := current
				unwritten.stableName, unwritten.windowName = observed.stableName, observed.windowName
				drifted = unwritten != observed
				for _, field := range written {
					drifted = drifted || (field(current) != field(observed) && field(current) != name)
				}
			}
			if drifted {
				return errors.New("Window runtime identity drifted before rename")
			}
			if own(current) != own(observed) && own(current) == name {
				return fmt.Errorf("window rename %s: %w", action.Verb, ErrEffectConverged)
			}
			return nil
		}
		action.Apply = func(ctx context.Context) error {
			if err := route.Check(ctx, action); err != nil {
				return err
			}
			command := "set-option"
			if action.Verb == "rename-window" {
				command = "rename-window"
			}
			_, err := routed.Run(ctx, "tmux", append([]string{command}, action.Operands...)...)
			return err
		}
		return action
	}
	err = route.Execute(ctx, []WindowRenameStep{
		step(stable, written[0]),
		step(display, written[1]),
	})
	if err != nil {
		return "", CommittedMirrorError("rename", coremetadata.KindWindow, uid, err)
	}
	return "", nil
}

func tmuxRowFormat(fields ...string) string { return strings.Join(fields, "\\037") }
func splitTmuxRows(output string, want int) [][]string {
	output = strings.ReplaceAll(output, "\\037", "\x1f")
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\x1f")
		if len(fields) == want {
			rows = append(rows, fields)
		}
	}
	return rows
}
func exactTmuxHandle(raw, prefix string) string {
	if !strings.HasPrefix(raw, prefix) || len(raw) == len(prefix) {
		return ""
	}
	for _, c := range raw[len(prefix):] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return raw
}

// WindowForPane follows the Registry ownership predicate, including Agent-owned
// Panes, rather than treating every Pane owner as a Window.
func WindowForPane(reg coremetadata.Registry, uid string) (coremetadata.Window, bool) {
	for _, w := range reg.Windows {
		if _, ok := reg.PaneInWindow(w.Metadata.UID, uid); ok {
			return w, true
		}
	}
	return coremetadata.Window{}, false
}
