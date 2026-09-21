package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// This file resolves the Registry `uid:` selectors that the canonical
// `focus project|window|pane` routes accept.
//
// Only a value spelled with the `uid:` prefix is resolved through the Registry.
// An unprefixed value keeps its runtime meaning (a live tmux name or id) and
// never causes a Registry read. A Registry value is only ever a candidate: the
// id it yields must still be found in the live tmux inventory on the resolved
// session before the client moves, and a `uid:` string never reaches tmux.

// focusNavUsesUID reports whether any of the three canonical positions carries
// a `uid:` selector. It is the one gate that decides whether the Registry is
// read at all.
func focusNavUsesUID(opts focusOptions) bool {
	if opts.NavKind == "" {
		return false
	}
	for _, value := range []string{opts.NavRef, opts.NavProject, opts.NavWindow} {
		if strings.HasPrefix(value, selector.UIDPrefix) {
			return true
		}
	}
	return false
}

// focusMetadataKind maps a canonical focus kind onto its Registry kind.
func focusMetadataKind(kind string) coremetadata.Kind {
	switch kind {
	case "project":
		return coremetadata.KindProject
	case "window":
		return coremetadata.KindWindow
	default:
		return coremetadata.KindPane
	}
}

// focusUIDSubject names the uid a request is about, for error text: the
// positional ref when it is a uid, otherwise the innermost uid scope flag.
type focusUIDSubject struct {
	kind string
	raw  string
}

func (s focusUIDSubject) String() string { return s.kind + " " + s.raw }

// focusUIDNotInRegistry is message (2): the uid names no Registry resource of
// the requested kind.
func focusUIDNotInRegistry(reg *coremetadata.Registry, kind coremetadata.Kind, raw, uid string) error {
	detail := ""
	if other, ok := focusRegistryKindOf(reg, uid); ok && other != kind {
		detail = fmt.Sprintf("; it names a %s, not a %s", other, kind)
	}
	return newFocusNotResolved("%s %s is not in the Registry%s", strings.ToLower(string(kind)), raw, detail)
}

// focusUIDNoLiveRuntime is message (3): the uid is in the Registry, but it has
// no live runtime the client could be moved to.
func focusUIDNoLiveRuntime(subject focusUIDSubject, format string, args ...any) error {
	return newFocusNotResolved("%s is in the Registry but has no live runtime: %s", subject, fmt.Sprintf(format, args...))
}

// focusUIDScopeMismatch is message (4): an explicit --project, --window, or
// --socket disagrees with the scope the Registry gives the uid.
func focusUIDScopeMismatch(subject focusUIDSubject, format string, args ...any) error {
	return newFocusNotResolved("%s does not match the requested scope: %s", subject, fmt.Sprintf(format, args...))
}

// focusRegistryKindOf reports which kind of Registry resource holds uid.
func focusRegistryKindOf(reg *coremetadata.Registry, uid string) (coremetadata.Kind, bool) {
	if _, ok := reg.Project(uid); ok {
		return coremetadata.KindProject, true
	}
	if _, ok := reg.Window(uid); ok {
		return coremetadata.KindWindow, true
	}
	if _, ok := reg.Pane(uid); ok {
		return coremetadata.KindPane, true
	}
	if _, ok := reg.Agent(uid); ok {
		return coremetadata.KindAgent, true
	}
	if _, ok := reg.ControlSession(uid); ok {
		return coremetadata.KindControlSession, true
	}
	return "", false
}

// focusPaneWindowUID follows a Pane's ownerRef to its Window. An Agent-owned
// Pane reaches its Window through the Agent, the same chain the Registry
// validator walks.
func focusPaneWindowUID(reg *coremetadata.Registry, pane coremetadata.Pane) (string, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", false
	}
	switch owner.Kind {
	case coremetadata.KindWindow:
		_, ok := reg.Window(owner.UID)
		return owner.UID, ok
	case coremetadata.KindAgent:
		agent, ok := reg.Agent(owner.UID)
		if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
			return "", false
		}
		_, ok = reg.Window(agent.Metadata.OwnerRef.UID)
		return agent.Metadata.OwnerRef.UID, ok
	default:
		return "", false
	}
}

// focusWindowProject follows a Window's ownerRef to its Project. A Window
// owned by anything else (a ControlSession) has no Project scope to focus in.
func focusWindowProject(reg *coremetadata.Registry, window coremetadata.Window) (*coremetadata.Project, bool) {
	owner := window.Metadata.OwnerRef
	if owner == nil || owner.Kind != coremetadata.KindProject {
		return nil, false
	}
	return reg.Project(owner.UID)
}

// resolveUIDNavigation resolves a canonical focus request in which at least
// one position carries a `uid:` selector. It returns the tmux coordinate and
// the socket the dispatch must use.
//
// Resolution: a Project uid yields status.session.name, a Window uid yields
// status.runtimeID (`@N`), and a Pane uid yields status.activation.runtimeID
// (`%N`). A positional uid defines its own scope through the ownerRef chain
// Pane -> Window -> Project, so --project and --window are optional for it.
//
// Scope flag rule, when a flag is given alongside a Registry-derived scope:
//   - a `uid:` flag must be the very uid the ownerRef chain reaches;
//   - a plain --project must equal the resolved Project's session name, which
//     is what a plain --project means on the name path;
//   - a plain --window must resolve, by the name path's own live match in the
//     resolved session, to the same `@N` the Registry Window records.
//
// Anything else is a scope mismatch. Socket rule: the Project's
// status.session.socketPath is the default socket, and an explicit --socket
// that names a different path is a mismatch rather than a search of another
// server. A Project with no recorded socket path keeps the name path's socket
// rule (--socket, else $TMUX).
func (c *focusCommand) resolveUIDNavigation(ctx context.Context, opts focusOptions) (string, string, error) {
	fallbackSocket := c.resolveSocket(opts.Socket)
	if c.loadRegistry == nil {
		return "", fallbackSocket, errors.New("focus: resource registry loader is not configured")
	}
	loaded, err := c.loadRegistry()
	if err != nil {
		return "", fallbackSocket, fmt.Errorf("focus: read resource registry: %w", err)
	}
	reg := &loaded

	windowRef := navWindowRef(opts)
	projectRef := opts.NavProject
	if opts.NavKind == "project" {
		projectRef = opts.NavRef
	}
	var paneRef string
	if opts.NavKind == "pane" {
		paneRef = opts.NavRef
	}
	isUID := func(value string) bool { return strings.HasPrefix(value, selector.UIDPrefix) }
	uidOf := func(value string) string { return strings.TrimPrefix(value, selector.UIDPrefix) }

	subject := focusUIDSubject{kind: opts.NavKind, raw: opts.NavRef}
	if !isUID(opts.NavRef) {
		if isUID(opts.NavWindow) {
			subject = focusUIDSubject{kind: "window", raw: opts.NavWindow}
		} else {
			subject = focusUIDSubject{kind: "project", raw: opts.NavProject}
		}
	}

	var (
		project *coremetadata.Project
		window  *coremetadata.Window
		pane    *coremetadata.Pane
	)
	if isUID(paneRef) {
		found, ok := reg.Pane(uidOf(paneRef))
		if !ok {
			return "", fallbackSocket, focusUIDNotInRegistry(reg, coremetadata.KindPane, paneRef, uidOf(paneRef))
		}
		pane = found
		windowUID, ok := focusPaneWindowUID(reg, *pane)
		if !ok {
			return "", fallbackSocket, focusUIDNoLiveRuntime(subject, "its ownerRef reaches no Window in the Registry")
		}
		window, _ = reg.Window(windowUID)
	}
	if isUID(windowRef) {
		found, ok := reg.Window(uidOf(windowRef))
		if !ok {
			return "", fallbackSocket, focusUIDNotInRegistry(reg, coremetadata.KindWindow, windowRef, uidOf(windowRef))
		}
		if window != nil && window.Metadata.UID != found.Metadata.UID {
			return "", fallbackSocket, focusUIDScopeMismatch(subject, "--window %s is not its owning Window uid:%s", windowRef, window.Metadata.UID)
		}
		window = found
	}
	if window != nil {
		owner, ok := focusWindowProject(reg, *window)
		if !ok {
			return "", fallbackSocket, focusUIDNoLiveRuntime(subject, "Window uid:%s is not owned by a Project in the Registry", window.Metadata.UID)
		}
		project = owner
	}
	if isUID(projectRef) {
		found, ok := reg.Project(uidOf(projectRef))
		if !ok {
			return "", fallbackSocket, focusUIDNotInRegistry(reg, coremetadata.KindProject, projectRef, uidOf(projectRef))
		}
		if project != nil && project.Metadata.UID != found.Metadata.UID {
			return "", fallbackSocket, focusUIDScopeMismatch(subject, "--project %s is not its owning Project uid:%s", projectRef, project.Metadata.UID)
		}
		project = found
	}
	if project == nil {
		// Unreachable: some position carries a uid, and every uid position
		// either fails above or derives a Project.
		return "", fallbackSocket, focusUIDNoLiveRuntime(subject, "no owning Project in the Registry")
	}

	session := project.Status.Session
	if session == nil || strings.TrimSpace(session.Name) == "" {
		return "", fallbackSocket, focusUIDNoLiveRuntime(subject, "Project uid:%s records no status.session.name", project.Metadata.UID)
	}
	sessionName := strings.TrimSpace(session.Name)
	if projectRef != "" && !isUID(projectRef) && projectRef != sessionName {
		return "", fallbackSocket, focusUIDScopeMismatch(subject, "--project %q is not its Project uid:%s session %q", projectRef, project.Metadata.UID, sessionName)
	}

	socket := fallbackSocket
	if recorded := strings.TrimSpace(session.SocketPath); recorded != "" {
		if explicit := strings.TrimSpace(opts.Socket); explicit != "" && filepath.Clean(explicit) != filepath.Clean(recorded) {
			return "", fallbackSocket, focusUIDScopeMismatch(subject, "--socket %q is not its Project uid:%s socket %q", explicit, project.Metadata.UID, recorded)
		}
		socket = recorded
	}

	// Live check: the session must exist under exactly this name. tmux's own
	// -t session lookup also accepts a prefix, so the exact name is confirmed
	// from list-sessions before any -t target is built from it.
	inventory, err := c.listSessionInventory(ctx, socket)
	if err != nil {
		return "", socket, focusUIDNoLiveRuntime(subject, "session %q could not be listed on socket %q: %v", sessionName, socket, err)
	}
	sessionLive := false
	for _, candidate := range inventory {
		if candidate.Name == sessionName {
			sessionLive = true
			break
		}
	}
	if !sessionLive {
		return "", socket, focusUIDNoLiveRuntime(subject, "session %q of Project uid:%s is not live on socket %q", sessionName, project.Metadata.UID, socket)
	}
	if opts.NavKind == "project" {
		return sessionName, socket, nil
	}

	windowID, err := c.resolveUIDWindow(ctx, socket, sessionName, subject, window, windowRef)
	if err != nil {
		return "", socket, err
	}
	if opts.NavKind == "window" {
		return sessionName + ":" + windowID, socket, nil
	}

	var paneID string
	if pane != nil {
		runtimeID := strings.TrimSpace(pane.Status.Activation.RuntimeID)
		if runtimeID == "" {
			return "", socket, focusUIDNoLiveRuntime(subject, "Pane uid:%s records no status.activation.runtimeID", pane.Metadata.UID)
		}
		rows, listErr := c.listTargets(ctx, socket, "list-panes", sessionName+":"+windowID, focusPaneListFormat())
		if listErr != nil {
			return "", socket, focusUIDNoLiveRuntime(subject, "panes of window %s in session %q could not be listed: %v", windowID, sessionName, listErr)
		}
		if paneID, err = pickLiveTarget("pane", runtimeID, sessionName+":"+windowID, rows); err != nil {
			return "", socket, focusUIDNoLiveRuntime(subject, "pane %s is not live in %s:%s", runtimeID, sessionName, windowID)
		}
	} else if paneID, err = c.resolveLivePane(ctx, socket, sessionName, windowID, paneRef); err != nil {
		return "", socket, err
	}
	return sessionName + ":" + windowID + "." + paneID, socket, nil
}

// resolveUIDWindow picks the live `@N` of a uid request. A Registry Window
// contributes its status.runtimeID, which must be live in the session; a plain
// --window alongside it must match that same id. Without a Registry Window the
// plain reference resolves exactly as on the name path.
func (c *focusCommand) resolveUIDWindow(ctx context.Context, socket, sessionName string, subject focusUIDSubject, window *coremetadata.Window, windowRef string) (string, error) {
	if window == nil {
		return c.resolveLiveWindow(ctx, socket, sessionName, windowRef)
	}
	runtimeID := strings.TrimSpace(window.Status.RuntimeID)
	if runtimeID == "" {
		return "", focusUIDNoLiveRuntime(subject, "Window uid:%s records no status.runtimeID", window.Metadata.UID)
	}
	rows, err := c.listTargets(ctx, socket, "list-windows", sessionName, focusWindowListFormat())
	if err != nil {
		return "", focusUIDNoLiveRuntime(subject, "windows of session %q could not be listed: %v", sessionName, err)
	}
	windowID, err := pickLiveTarget("window", runtimeID, sessionName, rows)
	if err != nil {
		return "", focusUIDNoLiveRuntime(subject, "window %s of Window uid:%s is not live in session %q", runtimeID, window.Metadata.UID, sessionName)
	}
	if windowRef != "" && !strings.HasPrefix(windowRef, selector.UIDPrefix) {
		named, nameErr := pickLiveTarget("window", windowRef, sessionName, rows)
		if nameErr != nil || named != windowID {
			return "", focusUIDScopeMismatch(subject, "--window %q is not its owning Window uid:%s (live %s)", windowRef, window.Metadata.UID, windowID)
		}
	}
	return windowID, nil
}

// focusWindowListFormat is the list-windows row resolveLiveWindow matches on.
func focusWindowListFormat() string {
	return strings.Join([]string{"#{window_id}", "#{window_name}", "#{@" + strings.TrimPrefix(tmuxopts.WindowName, "@") + "}"}, focusFieldSeparator)
}

// focusPaneListFormat is the list-panes row resolveLivePane matches on.
func focusPaneListFormat() string {
	return strings.Join([]string{"#{pane_id}", "#{@" + strings.TrimPrefix(tmuxopts.PaneName, "@") + "}"}, focusFieldSeparator)
}
